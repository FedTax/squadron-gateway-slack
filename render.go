package main

import (
	"encoding/json"
	"strings"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

// buildFallbackText is the plain-text fallback shown by Slack clients
// that can't render Block Kit (notifications, screen readers).
func buildFallbackText(rec gatewaysdk.HumanInputRecord) string {
	if rec.ShortSummary != "" {
		return rec.ShortSummary
	}
	return truncate(rec.Question, 200)
}

// buildBlocks renders the message body + interactive picker as Block
// Kit blocks. Returned slice is ready to pass to MsgOptionBlocks.
//
// Layout depends on picker shape:
//   - free-text → section block + an Answer button that opens a modal
//   - single-choice → section block + actions block of buttons
//   - multi-select → section block whose accessory is the multi_static_select
//     element (Slack rejects multi-selects inside actions blocks; they're
//     only valid as section accessories or in input blocks).
func buildBlocks(rec gatewaysdk.HumanInputRecord, topLevel bool) []slack.Block {
	bodyText := slack.NewTextBlockObject(slack.MarkdownType, buildMessageBody(rec, topLevel), false, false)

	if rec.MultiSelect && len(rec.Choices) > 0 {
		return []slack.Block{
			slack.NewSectionBlock(bodyText, nil, slack.NewAccessory(buildMultiSelect(rec))),
		}
	}

	blocks := []slack.Block{slack.NewSectionBlock(bodyText, nil, nil)}
	if len(rec.Choices) > 0 {
		blocks = append(blocks, buildButtonsBlock(rec))
	} else {
		blocks = append(blocks, buildAnswerButtonBlock(rec))
	}
	return blocks
}

// buildAnswerButtonBlock opens the answer modal. Free-text answers come from
// the modal rather than thread replies because a request thread can hold
// several open questions, and a reply can't say which one it answers.
func buildAnswerButtonBlock(rec gatewaysdk.HumanInputRecord) slack.Block {
	btn := slack.NewButtonBlockElement(
		encodeAnswerCustomID(rec.ToolCallID),
		"",
		slack.NewTextBlockObject(slack.PlainTextType, "Answer", false, false),
	)
	return slack.NewActionBlock("sq-answer-"+rec.ToolCallID, btn)
}

// fallbackThreadTitle is the top-level message for a run that asks before
// it has posted one: the mission and what it is asking, so the thread can
// still be found by searching the channel until the mission rewrites it.
func fallbackThreadTitle(rec gatewaysdk.HumanInputRecord) string {
	name := rec.MissionName
	if name == "" {
		name = "Mission run " + rec.MissionID
	}
	return "*" + name + "*: " + buildFallbackText(rec)
}

// buildMessageBody renders the question + context as Slack mrkdwn.
// Slack's mrkdwn dialect:
//   - bold:          *text*    (single asterisks, NOT **text**)
//   - italics:       _text_
//   - inline code:   `text`
//   - strikethrough: ~text~    (single tildes)
//
// The summary heads a question only when it's posted top-level; in a
// request thread the question itself is enough.
func buildMessageBody(rec gatewaysdk.HumanInputRecord, topLevel bool) string {
	var b strings.Builder
	if topLevel && rec.ShortSummary != "" {
		b.WriteString("*")
		b.WriteString(rec.ShortSummary)
		b.WriteString("*\n")
	}
	b.WriteString(rec.Question)
	if rec.MultiSelect && len(rec.Choices) > 0 {
		b.WriteString("\n_Pick one or more from the dropdown below._")
	} else if len(rec.Choices) == 0 {
		b.WriteString("\n_Click Answer to reply._")
	}
	if rec.AdditionalContext != "" {
		b.WriteString("\n\n_Context_\n")
		b.WriteString(rec.AdditionalContext)
	}
	if rec.MissionName != "" {
		b.WriteString("\n\n`")
		b.WriteString(rec.MissionName)
		if rec.TaskName != "" {
			b.WriteString(" › ")
			b.WriteString(rec.TaskName)
		}
		b.WriteString("`")
	}
	return b.String()
}

func buildResolvedBody(rec gatewaysdk.HumanInputRecord) string {
	var b strings.Builder
	b.WriteString("~")
	b.WriteString(truncate(rec.Question, 200))
	b.WriteString("~\n")
	b.WriteString("✅ ")
	b.WriteString(formatResolvedResponse(rec))
	if rec.ResponderUserID != "" {
		b.WriteString(" — _")
		b.WriteString(displayResponder(rec.ResponderUserID))
		b.WriteString("_")
	}
	return b.String()
}

// formatResolvedResponse expands a multi-select JSON array into a
// comma list. Falls back to the raw response on parse failure so the
// audit trail keeps whatever squadron stored.
func formatResolvedResponse(rec gatewaysdk.HumanInputRecord) string {
	if !rec.MultiSelect {
		return rec.Response
	}
	var picks []string
	if err := json.Unmarshal([]byte(rec.Response), &picks); err != nil || len(picks) == 0 {
		return rec.Response
	}
	return strings.Join(picks, ", ")
}

// Slack Block Kit limits.
const (
	maxActionElements   = 25 // elements per ActionsBlock
	maxButtonLabelBytes = 75
	maxSelectOptionText = 75
)

func buildButtonsBlock(rec gatewaysdk.HumanInputRecord) slack.Block {
	elements := make([]slack.BlockElement, 0, len(rec.Choices))
	for i, choice := range rec.Choices {
		if i >= maxActionElements {
			break
		}
		label := truncate(choice, maxButtonLabelBytes)
		btn := slack.NewButtonBlockElement(
			encodeCustomID(rec.ToolCallID, choice),
			choice,
			slack.NewTextBlockObject(slack.PlainTextType, label, false, false),
		)
		elements = append(elements, btn)
	}
	return slack.NewActionBlock("sq-actions-"+rec.ToolCallID, elements...)
}

// buildMultiSelect builds the multi_static_select element used as a
// section accessory for multi-select questions. Slack rejects this
// element type inside actions blocks (see buildBlocks for layout).
func buildMultiSelect(rec gatewaysdk.HumanInputRecord) *slack.MultiSelectBlockElement {
	options := make([]*slack.OptionBlockObject, 0, len(rec.Choices))
	for i, choice := range rec.Choices {
		if i >= maxActionElements {
			break
		}
		label := truncate(choice, maxSelectOptionText)
		options = append(options, slack.NewOptionBlockObject(
			choice,
			slack.NewTextBlockObject(slack.PlainTextType, label, false, false),
			nil,
		))
	}
	return slack.NewOptionsMultiSelectBlockElement(
		slack.MultiOptTypeStatic,
		slack.NewTextBlockObject(slack.PlainTextType, "Pick one or more…", false, false),
		encodeSelectMenuCustomID(rec.ToolCallID),
		options...,
	)
}

// displayResponder substitutes a generic label when no responder id
// was recorded — happens when commander resolves with auth disabled.
func displayResponder(s string) string {
	if s == "" {
		return "another operator"
	}
	return s
}

// truncate caps a string at `max` BYTES (not runes), reserving space
// for the 3-byte UTF-8 ellipsis. Slack enforces text-object limits on
// bytes-on-the-wire.
func truncate(s string, max int) string {
	const ellipsis = "…"
	if len(s) <= max {
		return s
	}
	if max <= len(ellipsis) {
		return s[:max]
	}
	return s[:max-len(ellipsis)] + ellipsis
}

// notifyEmoji picks a leading glyph for the notification by event.
func notifyEmoji(event string) string {
	switch event {
	case "mission_completed":
		return "✅"
	case "mission_failed":
		return "❌"
	default:
		return "🔔"
	}
}

// buildNotificationBody renders a one-way mission-lifecycle notification.
func buildNotificationBody(rec gatewaysdk.NotificationRecord) string {
	var b strings.Builder
	b.WriteString(notifyEmoji(rec.Event))
	b.WriteString(" *")
	if rec.Title != "" {
		b.WriteString(rec.Title)
	} else {
		b.WriteString(rec.Event)
	}
	b.WriteString("*")
	if rec.Message != "" {
		b.WriteString("\n")
		b.WriteString(rec.Message)
	}
	if rec.Error != "" {
		b.WriteString("\n```\n")
		b.WriteString(truncate(rec.Error, 2000))
		b.WriteString("\n```")
	}
	return b.String()
}
