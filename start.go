package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

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
	if _, err := g.client.OpenViewContext(ctx, cmd.TriggerID, startModal(missions, "", nil)); err != nil {
		log.Printf("open start modal: %v", err)
	}
}

// onMissionPicked redraws the form with the picked mission's inputs.
func (g *slackGateway) onMissionPicked(ctx context.Context, cb slack.InteractionCallback, mission string) {
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
	if _, err := g.client.UpdateViewContext(ctx, startModal(missions, mission, inputs), "", cb.View.Hash, cb.View.ID); err != nil {
		log.Printf("update start modal: %v", err)
	}
}

func startModal(missions []missionSummary, picked string, inputs []missionInput) slack.ModalViewRequest {
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

	m := slack.ModalViewRequest{
		Type:            slack.VTModal,
		CallbackID:      startCallbackID,
		PrivateMetadata: picked,
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
func startInputsFromSubmission(cb slack.InteractionCallback) (mission string, inputs map[string]any, ok bool) {
	mission = cb.View.PrivateMetadata
	if cb.View.CallbackID != startCallbackID || mission == "" || cb.View.State == nil {
		return "", nil, false
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
	return mission, inputs, true
}

// onStartSubmitted starts the run a submitted start form describes. Slack
// only takes field errors in the submission's ack, so the ack waits on
// run_mission; a bad value then shows on its field and the form stays open
// for the user to correct instead of being filled in again.
func (g *slackGateway) onStartSubmitted(ctx context.Context, req *socketmode.Request, cb slack.InteractionCallback) {
	mission, inputs, ok := startInputsFromSubmission(cb)
	if !ok {
		g.ack(req)
		return
	}
	missionID, err := g.mcp.startMission(ctx, mission, inputs)
	if err != nil {
		if errs := startFieldErrors(cb, err); errs != nil {
			g.ack(req, slack.NewErrorsViewSubmissionResponse(errs))
			return
		}
		g.ack(req)
		g.ephemeral(ctx, g.channelID, cb.User.ID, fmt.Sprintf("Couldn't start `%s`: %v", mission, err))
		return
	}
	g.ack(req)
	_, ts, err := g.client.PostMessageContext(ctx, g.channelID,
		slack.MsgOptionText(fmt.Sprintf("*%s* (run `%s`)\n%s", mission, missionID, requesterText(cb.User.ID)), false))
	if err != nil {
		log.Printf("post request message for %s: %v", missionID, err)
		return
	}
	g.rememberRequester(ts, cb.User.ID)
	g.rememberRun(missionID, ts)
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
