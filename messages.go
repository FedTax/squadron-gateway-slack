package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

type postedMessage struct {
	channel, ts string
}

// questionChannel is where a question was posted, so a note about its answer
// reaches the same place; questions from before a restart fall back to the
// configured channel.
func (g *slackGateway) questionChannel(toolCallID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if posted, ok := g.messages[toolCallID]; ok {
		return posted.channel
	}
	return g.channelID
}

// postQuestion posts a fresh request to Slack. Idempotent on
// tool_call_id so catch-up replays don't double-post.
func (g *slackGateway) postQuestion(ctx context.Context, rec gatewaysdk.HumanInputRecord) error {
	g.mu.Lock()
	if _, exists := g.messages[rec.ToolCallID]; exists {
		g.mu.Unlock()
		return nil
	}
	client := g.client
	channel := g.channelID
	g.mu.Unlock()
	if client == nil {
		return fmt.Errorf("slack client not initialized")
	}

	thread := g.threadForMission(rec.MissionID)
	if thread == "" && rec.MissionID != "" {
		var err error
		if thread, err = g.startThread(ctx, rec.MissionID, fallbackThreadTitle(rec)); err != nil {
			return err
		}
	}

	if thread != "" {
		channel = g.requestChannel(thread)
	}

	blocks := buildBlocks(rec)
	opts := []slack.MsgOption{
		slack.MsgOptionText(buildFallbackText(rec), false),
		slack.MsgOptionBlocks(blocks...),
	}
	if thread != "" {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	_, ts, err := client.PostMessageContext(ctx, channel, opts...)
	if err != nil {
		return fmt.Errorf("post message: %w", err)
	}

	g.mu.Lock()
	g.messages[rec.ToolCallID] = postedMessage{channel: channel, ts: ts}
	g.mu.Unlock()

	g.advanceCheckpoint(rec.RequestedAt)
	return nil
}

// postNotification posts a one-way mission-lifecycle notification. No
// idempotency map — there is nothing to track or edit. A per-mission channel
// override is resolved here, falling back to the configured default channel.
func (g *slackGateway) postNotification(ctx context.Context, rec gatewaysdk.NotificationRecord) error {
	g.mu.Lock()
	client := g.client
	channel := g.channelID
	g.mu.Unlock()
	if client == nil {
		return fmt.Errorf("slack client not initialized")
	}
	body := buildNotificationBody(rec)
	var thread string
	if rec.Channel != "" {
		channel = g.resolveNotifyChannel(ctx, client, rec.Channel, channel)
	} else {
		// A per-mission channel override posts top-level there instead.
		// A run with no request thread stays silent, so its failure can't
		// add a second top-level message for the request.
		thread = g.threadForMission(rec.MissionID)
		if thread == "" {
			return nil
		}
		channel = g.requestChannel(thread)
	}

	blocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, body, false, false),
			nil, nil,
		),
	}
	opts := []slack.MsgOption{
		slack.MsgOptionText(body, false),
		slack.MsgOptionBlocks(blocks...),
	}
	if thread != "" {
		opts = append(opts, slack.MsgOptionTS(thread))
	}
	if _, _, err := client.PostMessageContext(ctx, channel, opts...); err != nil {
		return fmt.Errorf("post notification: %w", err)
	}
	return nil
}

// slackPostDescription + slackPostSchema are advertised to squadron via
// MessageToolSpec so the LLM knows how to format a Slack post.
const slackPostDescription = "Post a Slack message. " +
	"`text` is the message body and supports Slack mrkdwn (*bold*, _italics_, `code`, > quotes, <url|label> links). " +
	"`channel` optionally overrides the destination — a channel name (with or without a leading #) or id. " +
	"`thread` is a key naming the request (e.g. its ticket key): the post replies in that request's thread, or becomes its top-level message when there is none yet. Set `root` to replace the request's top-level message with this post instead of replying. " +
	"`blocks` is an optional Slack Block Kit array for rich layout; each element is a block object with a `type`. Common types: " +
	"`header` = {\"type\":\"header\",\"text\":{\"type\":\"plain_text\",\"text\":\"...\"}}; " +
	"`section` = {\"type\":\"section\",\"text\":{\"type\":\"mrkdwn\",\"text\":\"...\"}} (or use \"fields\":[{\"type\":\"mrkdwn\",\"text\":\"...\"}] for a two-column grid); " +
	"`divider` = {\"type\":\"divider\"}; " +
	"`context` = {\"type\":\"context\",\"elements\":[{\"type\":\"mrkdwn\",\"text\":\"...\"}]}; " +
	"`image` = {\"type\":\"image\",\"image_url\":\"https://...\",\"alt_text\":\"...\"}; " +
	"`actions` = {\"type\":\"actions\",\"elements\":[{\"type\":\"button\",\"text\":{\"type\":\"plain_text\",\"text\":\"...\"},\"url\":\"https://...\"}]}. " +
	"When `blocks` is set it drives the layout and `text` is used as the notification/preview fallback."

const slackPostSchema = `{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "Message body (Slack mrkdwn supported). When blocks are set, this is the notification/preview fallback."},
    "channel": {"type": "string", "description": "Optional channel name or id override."},
    "thread": {"type": "string", "description": "Optional key naming the request, e.g. its ticket key. Runs that post under the same key share one thread. Ignored when channel is set."},
    "root": {"type": "boolean", "description": "Replace the request's top-level message with this post, or post it when the request has none, instead of replying in its thread."},
    "blocks": {
      "type": "array",
      "description": "Optional Slack Block Kit blocks for rich layout. Each item is a block object with a 'type'. Common types: header, section (text or fields), divider, context, image, actions (buttons). Example: [{\"type\":\"header\",\"text\":{\"type\":\"plain_text\",\"text\":\"Deploy\"}},{\"type\":\"section\",\"text\":{\"type\":\"mrkdwn\",\"text\":\"*v2* shipped to prod\"}},{\"type\":\"divider\"},{\"type\":\"context\",\"elements\":[{\"type\":\"mrkdwn\",\"text\":\"3m 12s\"}]}]",
      "items": {"type": "object"}
    }
  },
  "required": ["text"]
}`

type slackPostPayload struct {
	Text    string          `json:"text"`
	Channel string          `json:"channel,omitempty"`
	Thread  string          `json:"thread,omitempty"`
	Root    bool            `json:"root,omitempty"`
	Blocks  json.RawMessage `json:"blocks,omitempty"`
}

// postMessage renders a builtins.gateway.post payload (text + mrkdwn, Block
// Kit blocks) plus squadron-supplied file attachments and posts it, honoring
// an optional channel override (falling back to the configured default channel).
func (g *slackGateway) postMessage(ctx context.Context, req gatewaysdk.PostMessageRequest) error {
	var p slackPostPayload
	if err := json.Unmarshal([]byte(req.Payload), &p); err != nil {
		return fmt.Errorf("parse message payload: %w", err)
	}
	g.mu.Lock()
	client := g.client
	channel := g.channelID
	g.mu.Unlock()
	if client == nil {
		return fmt.Errorf("slack client not initialized")
	}
	var thread string
	if p.Channel != "" {
		channel = g.resolveNotifyChannel(ctx, client, p.Channel, channel)
	} else {
		thread = g.threadForPost(req.MissionID, p.Thread)
		if thread != "" {
			channel = g.requestChannel(thread)
		}
	}

	text := p.Text
	var blocks []slack.Block
	if len(p.Blocks) > 0 {
		var b slack.Blocks
		if err := json.Unmarshal(p.Blocks, &b); err == nil {
			blocks = b.BlockSet
		} else {
			log.Printf("slack blocks parse: %v", err)
		}
	}
	// A mission rewriting the top-level message doesn't know who asked, so
	// the requester line is put back each time or they'd stop following.
	if p.Root {
		if line := g.requesterLine(thread); line != "" {
			text += "\n" + line
			if blocks != nil {
				blocks = append(blocks, slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, line, false, false)))
			}
		}
	}
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if blocks != nil {
		opts = append(opts, slack.MsgOptionBlocks(blocks...))
	}

	switch {
	case p.Root && p.Channel == "" && thread != "":
		// The top-level message is rewritten in place, so it always reads as
		// the request's current state rather than its first.
		if _, _, _, err := client.UpdateMessageContext(ctx, channel, thread, opts...); err != nil {
			return fmt.Errorf("update request message: %w", err)
		}
	case thread != "" && !p.Root:
		if _, _, err := client.PostMessageContext(ctx, channel, append(opts, slack.MsgOptionTS(thread))...); err != nil {
			return fmt.Errorf("post message: %w", err)
		}
	default:
		_, ts, err := client.PostMessageContext(ctx, channel, opts...)
		if err != nil {
			return fmt.Errorf("post message: %w", err)
		}
		// A request with no thread yet takes this post as its one
		// top-level message.
		if p.Channel == "" {
			thread = ts
			g.rememberRun(req.MissionID, ts)
			g.rememberKey(p.Thread, ts)
		}
	}

	for _, a := range req.Attachments {
		g.uploadAttachment(ctx, client, channel, thread, a)
	}
	return nil
}

// uploadAttachment uploads a squadron-supplied file (already read from the
// mission's memory/scratchpad and shipped as raw bytes) to Slack so it appears
// as a native file. Best-effort: a failed attachment is logged, not fatal.
func (g *slackGateway) uploadAttachment(ctx context.Context, client *slack.Client, channel, thread string, a gatewaysdk.FileAttachment) {
	name := a.Filename
	if name == "" {
		name = "attachment"
	}
	if _, err := client.UploadFileV2Context(ctx, slack.UploadFileV2Parameters{
		Reader:          bytes.NewReader(a.Content),
		FileSize:        len(a.Content),
		Filename:        name,
		Channel:         channel,
		ThreadTimestamp: thread,
	}); err != nil {
		log.Printf("attachment %q upload: %v", name, err)
	}
}

// resolveNotifyChannel turns a per-mission channel override into a channel ID.
// A '#'-prefixed or name-shaped override is resolved by name; a Slack
// channel-ID-shaped override is used as-is. On failure it logs and falls back
// to def.
func (g *slackGateway) resolveNotifyChannel(ctx context.Context, client *slack.Client, override, def string) string {
	if !strings.HasPrefix(override, "#") && looksLikeSlackID(override) {
		return override
	}
	name := strings.TrimPrefix(override, "#")
	resolved, err := resolveChannelByName(ctx, client, name)
	if err != nil {
		log.Printf("notification channel override %q: %v — falling back to default", override, err)
		return def
	}
	return resolved
}

// looksLikeSlackID reports whether s has the shape of a Slack channel ID
// (C/G/D prefix followed by uppercase letters and digits).
func looksLikeSlackID(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch s[0] {
	case 'C', 'G', 'D':
	default:
		return false
	}
	for _, r := range s[1:] {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// markResolved edits the original message: strikethrough question +
// ✅ answer, blocks cleared so a stale click can't re-resolve.
// No-op if we never posted this question.
func (g *slackGateway) markResolved(ctx context.Context, rec gatewaysdk.HumanInputRecord) error {
	g.mu.Lock()
	posted, ok := g.messages[rec.ToolCallID]
	client := g.client
	g.mu.Unlock()
	if !ok || client == nil {
		return nil
	}

	body := buildResolvedBody(rec)
	resolvedBlocks := []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, body, false, false),
			nil, nil,
		),
	}
	if _, _, _, err := client.UpdateMessageContext(ctx, posted.channel, posted.ts,
		slack.MsgOptionText(body, false),
		slack.MsgOptionBlocks(resolvedBlocks...),
	); err != nil {
		return fmt.Errorf("update message: %w", err)
	}
	return nil
}
