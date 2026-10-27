package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// /squadron opens a form instead of parsing typed text: the mission is
// picked from squadron's list and each input gets its own field, typed and
// marked required from the mission's own declaration.

const (
	startCallbackID     = "sq-start"
	startMissionBlockID = "sq-start-mission"
	startMissionAction  = "sq-start-mission-select"
	startInputPrefix    = "sq-in::"
)

// startForm travels in the form's private_metadata, because a submission
// doesn't say where /squadron was run.
type startForm struct {
	Mission string `json:"mission,omitempty"`
	// Channel is where /squadron was run, so the request stays there. A DM
	// keeps it private to the requester.
	Channel string `json:"channel,omitempty"`
}

func readStartForm(metadata string) startForm {
	var form startForm
	_ = json.Unmarshal([]byte(metadata), &form)
	return form
}

func (g *slackGateway) onSlashCommand(ctx context.Context, cmd slack.SlashCommand) {
	if g.mcp == nil {
		g.ephemeral(ctx, cmd.ChannelID, cmd.UserID, "Starting missions from Slack needs the gateway's `mcp_url` setting.")
		return
	}
	missions, err := g.mcp.listMissions(ctx)
	if err != nil {
		g.ephemeral(ctx, cmd.ChannelID, cmd.UserID, fmt.Sprintf("Couldn't list missions: %v", err))
		return
	}
	if _, err := g.client.OpenViewContext(ctx, cmd.TriggerID, startModal(missions, startForm{Channel: cmd.ChannelID}, nil)); err != nil {
		log.Printf("open start modal: %v", err)
	}
}

// onMissionPicked redraws the form with the picked mission's inputs.
func (g *slackGateway) onMissionPicked(ctx context.Context, cb slack.InteractionCallback, mission string) {
	form := readStartForm(cb.View.PrivateMetadata)
	form.Mission = mission
	missions, err := g.mcp.listMissions(ctx)
	if err != nil {
		log.Printf("list missions: %v", err)
		return
	}
	inputs, err := g.mcp.missionInputs(ctx, mission)
	if err != nil {
		log.Printf("mission inputs for %s: %v", mission, err)
		return
	}
	if _, err := g.client.UpdateViewContext(ctx, startModal(missions, form, inputs), "", cb.View.Hash, cb.View.ID); err != nil {
		log.Printf("update start modal: %v", err)
	}
}

func startModal(missions []missionSummary, form startForm, inputs []missionInput) slack.ModalViewRequest {
	picked := form.Mission
	options := make([]*slack.OptionBlockObject, 0, len(missions))
	var initial *slack.OptionBlockObject
	for _, m := range missions {
		opt := slack.NewOptionBlockObject(m.Name, slack.NewTextBlockObject(slack.PlainTextType, truncate(m.Name, maxSelectOptionText), false, false), nil)
		options = append(options, opt)
		if m.Name == picked {
			initial = opt
		}
	}
	sel := slack.NewOptionsSelectBlockElement(slack.OptTypeStatic,
		slack.NewTextBlockObject(slack.PlainTextType, "Pick a mission", false, false),
		startMissionAction, options...)
	sel.InitialOption = initial
	blocks := []slack.Block{slack.NewActionBlock(startMissionBlockID, sel)}

	for _, in := range inputs {
		if in.Protected {
			continue
		}
		el := slack.NewPlainTextInputBlockElement(nil, "value")
		if len(in.Default) > 0 {
			el.InitialValue = defaultText(in)
		}
		label := in.Name
		if in.Type != "" && in.Type != "string" {
			label += " (" + in.Type + ")"
		}
		var hint *slack.TextBlockObject
		if in.Description != "" {
			hint = slack.NewTextBlockObject(slack.PlainTextType, truncate(in.Description, 2000), false, false)
		}
		block := slack.NewInputBlock(startInputPrefix+in.Type+"::"+in.Name,
			slack.NewTextBlockObject(slack.PlainTextType, truncate(label, 2000), false, false), hint, el)
		block.Optional = !in.Required
		blocks = append(blocks, block)
	}

	metadata, _ := json.Marshal(form)
	m := slack.ModalViewRequest{
		Type:            slack.VTModal,
		CallbackID:      startCallbackID,
		PrivateMetadata: string(metadata),
		Title:           slack.NewTextBlockObject(slack.PlainTextType, "Start a mission", false, false),
		Close:           slack.NewTextBlockObject(slack.PlainTextType, "Cancel", false, false),
		Blocks:          slack.Blocks{BlockSet: blocks},
	}
	if picked != "" {
		m.Submit = slack.NewTextBlockObject(slack.PlainTextType, "Start", false, false)
	}
	return m
}

// defaultText shows a string default as plain text and anything else as
// the JSON the mission would receive.
func defaultText(in missionInput) string {
	var s string
	if err := json.Unmarshal(in.Default, &s); err == nil {
		return s
	}
	return string(in.Default)
}

// startInputsFromSubmission turns the form back into mission inputs. A
// non-string input is decoded as JSON so lists and objects arrive typed;
// one that isn't valid JSON is passed as text for squadron to reject.
func startInputsFromSubmission(cb slack.InteractionCallback) (form startForm, inputs map[string]any, ok bool) {
	form = readStartForm(cb.View.PrivateMetadata)
	if cb.View.CallbackID != startCallbackID || form.Mission == "" || cb.View.State == nil {
		return startForm{}, nil, false
	}
	inputs = map[string]any{}
	for blockID, actions := range cb.View.State.Values {
		rest, isInput := strings.CutPrefix(blockID, startInputPrefix)
		if !isInput {
			continue
		}
		typ, name, _ := strings.Cut(rest, "::")
		raw := strings.TrimSpace(actions["value"].Value)
		if raw == "" {
			continue
		}
		if typ == "" || typ == "string" {
			inputs[name] = raw
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			inputs[name] = raw
			continue
		}
		inputs[name] = v
	}
	return form, inputs, true
}

// onStartSubmitted starts the run a submitted start form describes. Slack
// only takes field errors in the submission's ack, so the ack waits on
// run_mission; a bad value then shows on its field and the form stays open
// for the user to correct instead of being filled in again.
func (g *slackGateway) onStartSubmitted(ctx context.Context, req *socketmode.Request, cb slack.InteractionCallback) {
	form, inputs, ok := startInputsFromSubmission(cb)
	if !ok {
		g.ack(req)
		return
	}
	channel := form.Channel
	if channel == "" {
		channel = g.channelID
	}
	dm := strings.HasPrefix(channel, "D")
	missionThreadID := uuid.NewString()
	unlock := g.lockConversation(missionThreadID)
	defer unlock()
	missionID, err := g.mcp.startMission(ctx, form.Mission, inputs, missionThreadID)
	if err != nil {
		if errs := startFieldErrors(cb, err); errs != nil {
			g.ack(req, slack.NewErrorsViewSubmissionResponse(errs))
			return
		}
		g.ack(req)
		text := fmt.Sprintf("Couldn't start `%s`: %v", form.Mission, err)
		if dm {
			g.postToDM(ctx, cb.User.ID, text)
		} else {
			g.ephemeral(ctx, channel, cb.User.ID, text)
		}
		return
	}
	g.ack(req)
	// Slack delivers a post addressed to a user ID to that user's DM with
	// the app; the DM's own channel ID comes back for the thread's later posts.
	target := channel
	if dm {
		target = cb.User.ID
	}
	run := g.missionLine(form.Mission, missionID)
	channel, ts, err := g.client.PostMessageContext(ctx, target,
		slack.MsgOptionText(run+"\n"+requesterText(cb.User.ID), false))
	if err != nil {
		log.Printf("post request message for %s: %v", missionID, err)
		return
	}
	thread := slackThreadRef(channel, ts)
	g.rememberRequest(missionThreadID, thread, cb.User.ID, run)
}

func (g *slackGateway) postToDM(ctx context.Context, user, text string) {
	if _, _, err := g.client.PostMessageContext(ctx, user, slack.MsgOptionText(text, false)); err != nil {
		log.Printf("dm to %s: %v", user, err)
	}
}

// squadron names the input it rejects as `input 'name'` or
// `required input 'name'` (config/mission.go ResolveInputValues).
var rejectedInput = regexp.MustCompile(`input '([^']+)'`)

// startFieldErrors places a run_mission error on the form field it is
// about, or returns nil when it isn't about one of the form's inputs.
func startFieldErrors(cb slack.InteractionCallback, err error) map[string]string {
	m := rejectedInput.FindStringSubmatch(err.Error())
	if m == nil || cb.View.State == nil {
		return nil
	}
	for blockID := range cb.View.State.Values {
		if strings.HasPrefix(blockID, startInputPrefix) && strings.HasSuffix(blockID, "::"+m[1]) {
			return map[string]string{blockID: err.Error()}
		}
	}
	return nil
}

func (g *slackGateway) ack(req *socketmode.Request, payload ...any) {
	if req != nil {
		g.socket.Ack(*req, payload...)
	}
}

func (g *slackGateway) ephemeral(ctx context.Context, channel, user, text string) {
	if _, err := g.client.PostEphemeralContext(ctx, channel, user, slack.MsgOptionText(text, false)); err != nil {
		log.Printf("ephemeral to %s: %v", user, err)
	}
}
