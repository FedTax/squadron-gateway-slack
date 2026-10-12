package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
	"unicode"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
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
			case socketmode.EventTypeEventsAPI:
				eapi, ok := evt.Data.(slackevents.EventsAPIEvent)
				if !ok {
					log.Printf("events-api evt arrived but Data was %T", evt.Data)
					continue
				}
				if evt.Request != nil {
					g.socket.Ack(*evt.Request)
				}
				g.onEventsAPI(ctx, eapi)
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

// onEventsAPI handles the one Events API event the gateway uses: an
// @-mention that starts a mission.
func (g *slackGateway) onEventsAPI(ctx context.Context, evt slackevents.EventsAPIEvent) {
	if evt.Type != slackevents.CallbackEvent {
		return
	}
	if ev, ok := evt.InnerEvent.Data.(*slackevents.AppMentionEvent); ok {
		g.handleMention(ctx, ev)
	}
}

const startUsage = "Usage: `@squadron start <mission> {\"input\": \"value\", ...}`"

// handleMention starts the mission a top-level @-mention asks for. The
// mention becomes the request's thread. Its thread key is mapped before
// run_mission so the run's first question lands there even if it is asked
// before run_mission returns.
func (g *slackGateway) handleMention(ctx context.Context, ev *slackevents.AppMentionEvent) {
	if ev == nil || ev.User == "" || ev.User == g.botUserID || ev.Channel != g.channelID {
		return
	}
	if ev.ThreadTimeStamp != "" && ev.ThreadTimeStamp != ev.TimeStamp {
		return
	}
	mission, inputs, err := parseStartCommand(ev.Text)
	if err != nil {
		g.replyInThread(ctx, ev.TimeStamp, err.Error()+"\n"+startUsage)
		return
	}
	if g.mcp == nil {
		g.replyInThread(ctx, ev.TimeStamp, "Starting missions from Slack needs the gateway's `mcp_url` setting.")
		return
	}

	// A new request for a key that already has a thread takes the key over:
	// later runs for it belong to the request someone just asked for.
	key, _ := g.renderSpec(mission, inputs)
	previous := g.threadForKey(key)
	g.rememberRequester(ev.TimeStamp, ev.User)
	g.rememberKey(key, ev.TimeStamp)

	missionID, err := g.mcp.startMission(ctx, mission, inputs)
	if err != nil {
		if previous != "" {
			g.rememberKey(key, previous)
		} else {
			g.forgetKey(key)
		}
		g.replyInThread(ctx, ev.TimeStamp, fmt.Sprintf("Couldn't start `%s`: %v", mission, err))
		return
	}
	g.rememberRun(missionID, ev.TimeStamp)
	g.replyInThread(ctx, ev.TimeStamp, fmt.Sprintf("Started `%s` (run `%s`).", mission, missionID))
}

var slackLink = regexp.MustCompile(`<((?:https?|mailto):[^|>]+)(?:\|[^>]*)?>`)

// parseStartCommand reads "<@bot> start <mission> [{JSON inputs}]". Slack
// rewrites a URL typed in a message as <url|label> and escapes &, < and >,
// so both are undone before decoding; backticks from code formatting around
// the JSON are dropped. Numbers stay as written rather than becoming floats.
func parseStartCommand(text string) (mission string, inputs map[string]any, err error) {
	rest := strings.TrimSpace(text)
	for strings.HasPrefix(rest, "<@") {
		end := strings.Index(rest, ">")
		if end < 0 {
			break
		}
		rest = strings.TrimSpace(rest[end+1:])
	}
	verb, rest := cutWord(rest)
	if verb != "start" {
		return "", nil, errors.New("I only know how to start a mission.")
	}
	mission, rest = cutWord(rest)
	if mission == "" {
		return "", nil, errors.New("Which mission?")
	}

	raw := strings.TrimSpace(strings.Trim(strings.TrimSpace(rest), "`"))
	raw = html.UnescapeString(slackLink.ReplaceAllString(raw, "$1"))
	inputs = map[string]any{}
	if raw == "" {
		return mission, inputs, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&inputs); err != nil {
		return "", nil, fmt.Errorf("Inputs must be a JSON object: %v", err)
	}
	if dec.More() {
		return "", nil, errors.New("Inputs must be a single JSON object.")
	}
	return mission, inputs, nil
}

func cutWord(s string) (word, rest string) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

func (g *slackGateway) replyInThread(ctx context.Context, ts, text string) {
	if _, _, err := g.client.PostMessageContext(ctx, g.channelID,
		slack.MsgOptionText(text, false),
		slack.MsgOptionTS(ts),
	); err != nil {
		log.Printf("reply in thread %s: %v", ts, err)
	}
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
