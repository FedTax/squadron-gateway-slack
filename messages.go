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
	unlock := g.lockConversation(rec.ThreadID)
	defer unlock()
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

	thread := g.threadForMissionThread(rec.ThreadID)
	if thread == "" && rec.ThreadID != "" {
		var err error
		if thread, err = g.startThread(ctx, rec.ThreadID, fallbackThreadTitle(rec)); err != nil {
			return err
		}
	}

	var threadTS string
	if thread != "" {
		channel, threadTS = splitSlackThreadRef(thread)
	}

	blocks := buildBlocks(rec, thread == "")
	opts := []slack.MsgOption{
		slack.MsgOptionText(buildFallbackText(rec), false),
		slack.MsgOptionBlocks(blocks...),
	}
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
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

// postNotification replies in the mission thread's Slack conversation.
// The first notification creates its root in the override or default channel.
func (g *slackGateway) postNotification(ctx context.Context, rec gatewaysdk.NotificationRecord) error {
	unlock := g.lockConversation(rec.ThreadID)
	defer unlock()
	g.mu.Lock()
	client := g.client
	channel := g.channelID
	g.mu.Unlock()
	if client == nil {
		return fmt.Errorf("slack client not initialized")
	}
	body := buildNotificationBody(rec)
	thread := g.threadForMissionThread(rec.ThreadID)
	var threadTS string
	if thread != "" {
		channel, threadTS = splitSlackThreadRef(thread)
	} else if rec.Channel != "" {
		channel = g.resolveNotifyChannel(ctx, client, rec.Channel, channel)
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
	if threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}
	posted, ts, err := client.PostMessageContext(ctx, channel, opts...)
	if err != nil {
		return fmt.Errorf("post notification: %w", err)
	}
	if thread == "" {
		g.rememberThread(rec.ThreadID, slackThreadRef(posted, ts))
	}
	return nil
}

// slackPostDescription + slackPostSchema are advertised to squadron via
// MessageToolSpec so the LLM knows how to format a Slack post.
const slackPostDescription = "Post a Slack message. " +
	"`text` is the message body and supports Slack mrkdwn (*bold*, _italics_, `code`, > quotes, <url|label> links). " +
	"Messages share the conversation of the current mission thread (chain of related runs). Set `root` to replace its complete top-level content instead of replying. " +
	"The result gives the message's id, which builtins.gateway.edit takes to change it later. " +
	"`channel` optionally picks where a new request's top-level message goes — a channel name (with or without a leading #) or id. Once the mission thread has a Slack conversation, posts stay in it. " +
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
    "channel": {"type": "string", "description": "Optional channel name or id to open the request's thread in. Used only when the mission thread has no Slack conversation yet; otherwise the post goes in the existing thread."},
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
	Text string `json:"text"`
	// Channel chooses the destination only when creating a new conversation.
	Channel string          `json:"channel,omitempty"`
	Root    bool            `json:"root,omitempty"`
	Blocks  json.RawMessage `json:"blocks,omitempty"`
}

// postMessage posts or replaces message content and uploads attachments
// in the mission thread's Slack conversation. The first post creates the root
// in the override or default channel. It returns the posted or updated message ID.
func (g *slackGateway) postMessage(ctx context.Context, req gatewaysdk.PostMessageRequest) (string, error) {
	unlock := g.lockConversation(req.ThreadID)
	defer unlock()
	var p slackPostPayload
	if err := json.Unmarshal([]byte(req.Payload), &p); err != nil {
		return "", fmt.Errorf("parse message payload: %w", err)
	}
	g.mu.Lock()
	client := g.client
	channel := g.channelID
	g.mu.Unlock()
	if client == nil {
		return "", fmt.Errorf("slack client not initialized")
	}
	thread := g.threadForMissionThread(req.ThreadID)
	var threadTS string
	if thread != "" {
		channel, threadTS = splitSlackThreadRef(thread)
	} else if p.Channel != "" {
		channel = g.resolveNotifyChannel(ctx, client, p.Channel, channel)
	}

	var lines string
	if p.Root {
		lines = g.requestLines(thread)
	}
	opts := messageOptions(p.Text, p.Blocks, lines)

	id := thread
	switch {
	case p.Root && thread != "":
		// Replace the root with the latest supplied content.
		if _, _, _, err := client.UpdateMessageContext(ctx, channel, threadTS, opts...); err != nil {
			return "", fmt.Errorf("update request message: %w", err)
		}
	case thread != "" && !p.Root:
		_, ts, err := client.PostMessageContext(ctx, channel, append(opts, slack.MsgOptionTS(threadTS))...)
		if err != nil {
			return "", fmt.Errorf("post message: %w", err)
		}
		id = slackThreadRef(channel, ts)
	default:
		posted, ts, err := client.PostMessageContext(ctx, channel, opts...)
		if err != nil {
			return "", fmt.Errorf("post message: %w", err)
		}
		// A request with no thread yet takes this post as its one
		// top-level message.
		channel, threadTS = posted, ts
		thread = slackThreadRef(posted, ts)
		g.rememberThread(req.ThreadID, thread)
		id = thread
	}

	for _, a := range req.Attachments {
		g.uploadAttachment(ctx, client, channel, threadTS, a)
	}
	return id, nil
}

// editMessage replaces content at the supplied message ID.
// Channel and root overrides are rejected because an edit preserves the destination.
func (g *slackGateway) editMessage(ctx context.Context, req gatewaysdk.EditMessageRequest) error {
	unlock := g.lockConversation(req.ThreadID)
	defer unlock()
	var p slackPostPayload
	if err := json.Unmarshal([]byte(req.Payload), &p); err != nil {
		return fmt.Errorf("parse message payload: %w", err)
	}
	if p.Channel != "" || p.Root {
		return fmt.Errorf("an edit changes only text, blocks and attachments; channel and root apply to posts")
	}
	channel, ts := splitSlackThreadRef(req.MessageID)
	if channel == "" || ts == "" {
		return fmt.Errorf("message id %q isn't one a post returned (<channel>/<ts>)", req.MessageID)
	}
	g.mu.Lock()
	client := g.client
	question := g.isQuestionLocked(channel, ts)
	g.mu.Unlock()
	if client == nil {
		return fmt.Errorf("slack client not initialized")
	}
	// The gateway rewrites a question itself once it's answered, so an edit
	// from the run would be lost or would hide the answer controls.
	if question {
		return fmt.Errorf("message %s is a question; it changes when it's answered", req.MessageID)
	}
	// A request's top-level message keeps the gateway's lines through an
	// edit, as it does through a root post.
	opts := messageOptions(p.Text, p.Blocks, g.requestLines(req.MessageID))
	if _, _, _, err := client.UpdateMessageContext(ctx, channel, ts, opts...); err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	// Upload attached files into the mission thread's Slack conversation.
	fileChannel, fileThread := channel, ts
	if thread := g.threadForMissionThread(req.ThreadID); thread != "" {
		fileChannel, fileThread = splitSlackThreadRef(thread)
	}
	for _, a := range req.Attachments {
		g.uploadAttachment(ctx, client, fileChannel, fileThread, a)
	}
	return nil
}

func (g *slackGateway) isQuestionLocked(channel, ts string) bool {
	for _, m := range g.messages {
		if m.channel == channel && m.ts == ts {
			return true
		}
	}
	return false
}

// messageOptions builds a post's or edit's Slack content, adding the
// gateway's lines after the mission's when there are any.
func messageOptions(text string, rawBlocks json.RawMessage, lines string) []slack.MsgOption {
	var blocks []slack.Block
	if len(rawBlocks) > 0 {
		var b slack.Blocks
		if err := json.Unmarshal(rawBlocks, &b); err == nil {
			blocks = b.BlockSet
		} else {
			log.Printf("slack blocks parse: %v", err)
		}
	}
	if lines != "" {
		text += "\n" + lines
		if blocks != nil {
			blocks = append(blocks, slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, lines, false, false)))
		}
	}
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if blocks != nil {
		opts = append(opts, slack.MsgOptionBlocks(blocks...))
	}
	return opts
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
		log.Printf("channel %q: %v — posting to the configured channel instead", override, err)
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
