package main

import "strings"

// action_id encoding for Block Kit interactions. Two shapes share one
// "sq::"-prefixed namespace:
//
//   sq::<tool_call_id>::<choice>     button click — choice is the value
//   sq::<tool_call_id>::__select__   multi-select — values arrive in
//                                                   the action payload
//   sq::<tool_call_id>::__answer__   free-text Answer button — opens
//                                                   the answer modal
//
// Slack caps action_id at 255 bytes, which gives us comfortably more
// headroom than Discord's 100 — but we still defensively truncate so a
// malicious or extremely long choice can't blow the limit.

const (
	customIDSep      = "::"
	customIDPrefix   = "sq"
	selectMenuMarker = "__select__"
	answerMarker     = "__answer__"
	customIDMaxLen   = 255
)

func encodeCustomID(toolCallID, choice string) string {
	id := customIDPrefix + customIDSep + toolCallID + customIDSep + choice
	if len(id) > customIDMaxLen {
		id = id[:customIDMaxLen]
	}
	return id
}

func decodeCustomID(s string) (toolCallID, choice string, ok bool) {
	parts := strings.SplitN(s, customIDSep, 3)
	if len(parts) != 3 || parts[0] != customIDPrefix {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func encodeSelectMenuCustomID(toolCallID string) string {
	return customIDPrefix + customIDSep + toolCallID + customIDSep + selectMenuMarker
}

func decodeSelectMenuCustomID(s string) (toolCallID string, ok bool) {
	tc, marker, decoded := decodeCustomID(s)
	if !decoded || marker != selectMenuMarker {
		return "", false
	}
	return tc, true
}

func encodeAnswerCustomID(toolCallID string) string {
	return customIDPrefix + customIDSep + toolCallID + customIDSep + answerMarker
}

func decodeAnswerCustomID(s string) (toolCallID string, ok bool) {
	tc, marker, decoded := decodeCustomID(s)
	if !decoded || marker != answerMarker {
		return "", false
	}
	return tc, true
}
