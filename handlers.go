package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// runSocketEvents pumps the socket-mode Events channel until the run
// context is canceled. Each event is acked immediately (Slack expects
// an ack within ~3s) and routed to a typed handler.
func (g *slackGateway) runSocketEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-g.socket.Events:
			if !ok {
				return
			}
			switch evt.Type {
			case socketmode.EventTypeInteractive:
				cb, ok := evt.Data.(slack.InteractionCallback)
				if !ok {
					continue
				}
				if evt.Request != nil {
					g.socket.Ack(*evt.Request)
				}
				g.onInteraction(ctx, cb)
			case socketmode.EventTypeSlashCommand:
				cmd, ok := evt.Data.(slack.SlashCommand)
				if !ok {
					continue
				}
				if evt.Request != nil {
					g.socket.Ack(*evt.Request)
				}
				g.onSlashCommand(ctx, cmd)
			case socketmode.EventTypeConnecting, socketmode.EventTypeConnected, socketmode.EventTypeHello:
				// status events — nothing to do
			case socketmode.EventTypeErrorBadMessage, socketmode.EventTypeErrorWriteFailed,
				socketmode.EventTypeIncomingError, socketmode.EventTypeDisconnect:
				log.Printf("socket-mode event: %s", evt.Type)
			}
		}
	}
}

// onInteraction routes a Block Kit action (button click or multi-select
// submit) into ResolveHumanInput, opens the answer modal for a free-text
// question, and resolves the question when that modal is submitted.
func (g *slackGateway) onInteraction(ctx context.Context, cb slack.InteractionCallback) {
	switch cb.Type {
	case slack.InteractionTypeViewSubmission:
		if cb.View.CallbackID == startCallbackID {
			g.startFromSubmission(ctx, cb)
			return
		}
		toolCallID, answer, ok := answerFromSubmission(cb)
		if !ok {
			return
		}
		g.resolve(ctx, toolCallID, answer, slackResponderID(cb), g.channelID, cb.User.ID)
		return
	case slack.InteractionTypeBlockActions:
	default:
		return
	}
	if len(cb.ActionCallback.BlockActions) == 0 {
		return
	}
	action := cb.ActionCallback.BlockActions[0]

	if action.ActionID == startMissionAction {
		g.onMissionPicked(ctx, cb, action.SelectedOption.Value)
		return
	}

	if toolCallID, ok := decodeAnswerCustomID(action.ActionID); ok {
		if _, err := g.client.OpenViewContext(ctx, cb.TriggerID, answerModal(toolCallID)); err != nil {
			log.Printf("open answer modal: %v", err)
		}
		return
	}

	toolCallID, response, ok := decodeInteractionResponse(action)
	if !ok {
		return
	}
	g.resolve(ctx, toolCallID, response, slackResponderID(cb), cb.Channel.ID, cb.User.ID)
}

// resolve sends an answer to squadron. On NotFound it surfaces a transient
// ephemeral note so the operator knows their answer didn't land. Other
// outcomes are confirmed by the message edit flowing through
// OnHumanInputResolved.
func (g *slackGateway) resolve(ctx context.Context, toolCallID, response, responder, channel, userID string) {
	res, err := g.api.ResolveHumanInput(ctx, toolCallID, response, responder)
	if err != nil {
		log.Printf("resolve %s: %v", toolCallID, err)
		return
	}
	if res.NotFound {
		_, err := g.client.PostEphemeralContext(ctx, channel, userID,
			slack.MsgOptionText("That request is no longer in squadron's store — the row may have been trimmed.", false))
		if err != nil {
			log.Printf("ephemeral not-found notice: %v", err)
		}
	}
}

// decodeInteractionResponse maps a Block Kit BlockAction into the
// (toolCallID, response) pair we send to squadron. Returns ok=false
// for any action_id that didn't originate here, and for the Answer
// button, which opens a modal instead of answering. Multi-select values
// are JSON-encoded so the agent gets a canonical array string instead
// of a Go-printed slice.
func decodeInteractionResponse(action *slack.BlockAction) (toolCallID, response string, ok bool) {
	if action == nil {
		return "", "", false
	}
	if tc, ok := decodeSelectMenuCustomID(action.ActionID); ok {
		picks := make([]string, 0, len(action.SelectedOptions))
		for _, opt := range action.SelectedOptions {
			picks = append(picks, opt.Value)
		}
		encoded, err := json.Marshal(picks)
		if err != nil {
			log.Printf("encode select-menu values: %v", err)
			return "", "", false
		}
		return tc, string(encoded), true
	}
	if tc, choice, ok := decodeCustomID(action.ActionID); ok && choice != answerMarker {
		return tc, choice, true
	}
	return "", "", false
}

const (
	answerCallbackID = "sq-answer"
	answerBlockID    = "sq-answer-block"
	answerActionID   = "sq-answer-text"
)

// answerModal carries the tool_call_id in private_metadata so the
// submission resolves exactly the question whose Answer button opened it.
func answerModal(toolCallID string) slack.ModalViewRequest {
	input := slack.NewPlainTextInputBlockElement(nil, answerActionID)
	input.Multiline = true
	return slack.ModalViewRequest{
		Type:            slack.VTModal,
		CallbackID:      answerCallbackID,
		PrivateMetadata: toolCallID,
		Title:           slack.NewTextBlockObject(slack.PlainTextType, "Answer", false, false),
		Submit:          slack.NewTextBlockObject(slack.PlainTextType, "Send", false, false),
		Close:           slack.NewTextBlockObject(slack.PlainTextType, "Cancel", false, false),
		Blocks: slack.Blocks{BlockSet: []slack.Block{
			slack.NewInputBlock(answerBlockID,
				slack.NewTextBlockObject(slack.PlainTextType, "Your answer", false, false),
				nil, input),
		}},
	}
}

func answerFromSubmission(cb slack.InteractionCallback) (toolCallID, answer string, ok bool) {
	if cb.View.CallbackID != answerCallbackID || cb.View.PrivateMetadata == "" || cb.View.State == nil {
		return "", "", false
	}
	answer = strings.TrimSpace(cb.View.State.Values[answerBlockID][answerActionID].Value)
	if answer == "" {
		return "", "", false
	}
	return cb.View.PrivateMetadata, answer, true
}

// slackResponderID prefers the user's display name when present;
// falls back to the user id (UXXXXXXXX) so the audit trail always has
// something stable.
func slackResponderID(cb slack.InteractionCallback) string {
	if cb.User.Name != "" {
		return "slack:" + cb.User.Name
	}
	if cb.User.ID != "" {
		return "slack:" + cb.User.ID
	}
	return "slack:unknown"
}
