package main

import (
	"strings"
	"testing"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

func TestDisplayResponder(t *testing.T) {
	if got := displayResponder(""); got != "another operator" {
		t.Errorf("empty responder: got %q, want %q", got, "another operator")
	}
	if got := displayResponder("slack:alice"); got != "slack:alice" {
		t.Errorf("non-empty responder: got %q, want input back", got)
	}
}

// truncate's contract: result is at most `max` bytes (not runes).
// "…" is 3 bytes in UTF-8 so a naive `s[:max-1] + "…"` overshoots by 2.
func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than max returns input verbatim", in: "short", max: 10, want: "short"},
		{name: "exact-fit returns input verbatim", in: "exactly10!", max: 10, want: "exactly10!"},
		{name: "longer than max gets ellipsis with byte budget reserved", in: "exactly11!!", max: 10, want: "exactly…"},
		{name: "truncated to fit including the ellipsis bytes", in: "long string here", max: 5, want: "lo…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncate(%q, %d): got %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if len(got) > tc.max {
				t.Errorf("truncate(%q, %d) returned %d bytes; must be ≤ %d", tc.in, tc.max, len(got), tc.max)
			}
		})
	}
}

func TestFormatResolvedResponseExpandsMultiSelectJSON(t *testing.T) {
	cases := []struct {
		name string
		rec  gatewaysdk.HumanInputRecord
		want string
	}{
		{
			name: "single-select returns response verbatim",
			rec:  gatewaysdk.HumanInputRecord{Response: "Option A"},
			want: "Option A",
		},
		{
			name: "multi-select expands JSON array to comma list",
			rec:  gatewaysdk.HumanInputRecord{MultiSelect: true, Response: `["A","C"]`},
			want: "A, C",
		},
		{
			name: "multi-select with malformed JSON falls back to raw",
			rec:  gatewaysdk.HumanInputRecord{MultiSelect: true, Response: "not json"},
			want: "not json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatResolvedResponse(tc.rec); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildMessageBody(t *testing.T) {
	cases := []struct {
		name        string
		rec         gatewaysdk.HumanInputRecord
		wantContain []string
		wantOmit    []string
	}{
		{
			name: "short summary becomes the bold title (single asterisks for slack mrkdwn)",
			rec: gatewaysdk.HumanInputRecord{
				ShortSummary: "Vibe?",
				Question:     "Pick a vibe.",
			},
			wantContain: []string{"*Vibe?*", "Pick a vibe."},
			wantOmit:    []string{"**Vibe?**"},
		},
		{
			name: "multi-select adds the dropdown hint",
			rec: gatewaysdk.HumanInputRecord{
				Question:    "Pick any.",
				Choices:     []string{"A", "B"},
				MultiSelect: true,
			},
			wantContain: []string{"Pick one or more from the dropdown"},
			wantOmit:    []string{"Click Answer"},
		},
		{
			name: "single-select does NOT add the dropdown hint",
			rec: gatewaysdk.HumanInputRecord{
				Question: "Pick one.",
				Choices:  []string{"A", "B"},
			},
			wantOmit: []string{"dropdown", "Click Answer"},
		},
		{
			name: "free-text (no choices) prompts the Answer button",
			rec: gatewaysdk.HumanInputRecord{
				Question: "What do you think?",
			},
			wantContain: []string{"Click Answer"},
		},
		{
			name: "additional context renders under a Context header",
			rec: gatewaysdk.HumanInputRecord{
				Question:          "Q",
				AdditionalContext: "background _markdown_",
			},
			wantContain: []string{"_Context_", "background _markdown_"},
		},
		{
			name: "mission and task render as a code-fenced trail",
			rec: gatewaysdk.HumanInputRecord{
				Question:    "Q",
				MissionName: "trip_plan",
				TaskName:    "interview",
			},
			wantContain: []string{"`trip_plan › interview`"},
		},
		{
			name: "mission alone (no task) renders without separator",
			rec: gatewaysdk.HumanInputRecord{
				Question:    "Q",
				MissionName: "trip_plan",
			},
			wantContain: []string{"`trip_plan`"},
			wantOmit:    []string{"›"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildMessageBody(tc.rec)
			for _, sub := range tc.wantContain {
				if !strings.Contains(body, sub) {
					t.Errorf("body missing %q\nfull body:\n%s", sub, body)
				}
			}
			for _, sub := range tc.wantOmit {
				if strings.Contains(body, sub) {
					t.Errorf("body should NOT contain %q\nfull body:\n%s", sub, body)
				}
			}
		})
	}
}

func TestBuildResolvedBodyStrikesThroughOriginal(t *testing.T) {
	body := buildResolvedBody(gatewaysdk.HumanInputRecord{
		ShortSummary:    "Vibe?",
		Question:        "Pick a vibe.",
		Response:        "Relaxed",
		ResponderUserID: "slack:alice",
	})
	// Slack mrkdwn uses single tildes for strikethrough; ensure we
	// don't emit Discord-style double tildes.
	for _, want := range []string{"~*Vibe?*~", "✅", "Relaxed", "slack:alice"} {
		if !strings.Contains(body, want) {
			t.Errorf("resolved body missing %q\nfull body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "~~") {
		t.Errorf("resolved body uses Discord-style ~~ instead of Slack ~; got:\n%s", body)
	}
}

func TestBuildResolvedBodyExpandsMultiSelect(t *testing.T) {
	body := buildResolvedBody(gatewaysdk.HumanInputRecord{
		ShortSummary: "Avoid?",
		Response:     `["crowds","long drives"]`,
		MultiSelect:  true,
	})
	if !strings.Contains(body, "crowds, long drives") {
		t.Errorf("multi-select resolved body should expand JSON to comma list, got:\n%s", body)
	}
	if strings.Contains(body, "[") {
		t.Errorf("resolved body should NOT show raw JSON brackets, got:\n%s", body)
	}
}

func TestBuildBlocksFreeTextHasAnswerButton(t *testing.T) {
	blocks := buildBlocks(gatewaysdk.HumanInputRecord{
		ToolCallID: "tc",
		Question:   "What do you think?",
	}, "")
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (section + Answer button), got %d", len(blocks))
	}
	actions, ok := blocks[1].(*slack.ActionBlock)
	if !ok {
		t.Fatalf("expected ActionBlock, got %T", blocks[1])
	}
	if len(actions.Elements.ElementSet) != 1 {
		t.Fatalf("expected 1 button, got %d", len(actions.Elements.ElementSet))
	}
	btn, ok := actions.Elements.ElementSet[0].(*slack.ButtonBlockElement)
	if !ok {
		t.Fatalf("expected ButtonBlockElement, got %T", actions.Elements.ElementSet[0])
	}
	if tc, ok := decodeAnswerCustomID(btn.ActionID); !ok || tc != "tc" {
		t.Errorf("Answer button action_id %q must decode to tc, got %q ok=%v", btn.ActionID, tc, ok)
	}
}

func TestBuildBlocksPrependsMention(t *testing.T) {
	blocks := buildBlocks(gatewaysdk.HumanInputRecord{ToolCallID: "tc", Question: "Q?"}, "<@U1> ")
	section := blocks[0].(*slack.SectionBlock)
	if !strings.HasPrefix(section.Text.Text, "<@U1> ") {
		t.Errorf("section text should start with the mention, got %q", section.Text.Text)
	}
}

func TestBuildBlocksSingleSelectUsesButtons(t *testing.T) {
	blocks := buildBlocks(gatewaysdk.HumanInputRecord{
		ToolCallID: "tc-1",
		Question:   "Pick one.",
		Choices:    []string{"A", "B", "C"},
	}, "")
	// section + actions = 2 blocks
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (section + actions), got %d", len(blocks))
	}
	actions, ok := blocks[1].(*slack.ActionBlock)
	if !ok {
		t.Fatalf("expected ActionBlock, got %T", blocks[1])
	}
	if got := len(actions.Elements.ElementSet); got != 3 {
		t.Fatalf("expected 3 buttons, got %d", got)
	}
	for i, el := range actions.Elements.ElementSet {
		if _, ok := el.(*slack.ButtonBlockElement); !ok {
			t.Errorf("element %d is %T, want *slack.ButtonBlockElement", i, el)
		}
	}
}

// Multi-select must ride as a section accessory — Slack returns
// invalid_blocks if a multi_static_select element appears inside an
// actions block.
func TestBuildBlocksMultiSelectIsSectionAccessory(t *testing.T) {
	blocks := buildBlocks(gatewaysdk.HumanInputRecord{
		ToolCallID:  "tc-1",
		Question:    "Pick any.",
		Choices:     []string{"A", "B", "C"},
		MultiSelect: true,
	}, "")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (section with accessory), got %d", len(blocks))
	}
	section, ok := blocks[0].(*slack.SectionBlock)
	if !ok {
		t.Fatalf("expected SectionBlock, got %T", blocks[0])
	}
	if section.Accessory == nil || section.Accessory.MultiSelectElement == nil {
		t.Fatalf("expected multi-select accessory, got %+v", section.Accessory)
	}
	menu := section.Accessory.MultiSelectElement
	if menu.Type != slack.MultiOptTypeStatic {
		t.Errorf("expected static multi-select, got %v", menu.Type)
	}
	if len(menu.Options) != 3 {
		t.Errorf("expected 3 options, got %d", len(menu.Options))
	}
	if _, ok := decodeSelectMenuCustomID(menu.ActionID); !ok {
		t.Errorf("multi-select action_id %q must round-trip through decodeSelectMenuCustomID", menu.ActionID)
	}
}

func TestBuildButtonsBlockCapsAt25Buttons(t *testing.T) {
	choices := make([]string, 30)
	for i := range choices {
		choices[i] = "C" + string(rune('A'+i%26))
	}
	block := buildButtonsBlock(gatewaysdk.HumanInputRecord{
		ToolCallID: "tc-1",
		Choices:    choices,
	})
	actions := block.(*slack.ActionBlock)
	if got := len(actions.Elements.ElementSet); got != maxActionElements {
		t.Errorf("expected exactly %d buttons (Slack action-element cap), got %d", maxActionElements, got)
	}
}

func TestBuildMultiSelectTruncatesLongChoices(t *testing.T) {
	long := strings.Repeat("a", 200)
	menu := buildMultiSelect(gatewaysdk.HumanInputRecord{
		ToolCallID:  "tc",
		Choices:     []string{long},
		MultiSelect: true,
	})
	if len(menu.Options[0].Text.Text) > maxSelectOptionText {
		t.Errorf("multi-select option label %d bytes; Slack caps at %d",
			len(menu.Options[0].Text.Text), maxSelectOptionText)
	}
}

func TestBuildFallbackText(t *testing.T) {
	if got := buildFallbackText(gatewaysdk.HumanInputRecord{ShortSummary: "Vibe?"}); got != "Vibe?" {
		t.Errorf("short_summary should become the fallback, got %q", got)
	}
	if got := buildFallbackText(gatewaysdk.HumanInputRecord{Question: "Pick a vibe."}); got != "Pick a vibe." {
		t.Errorf("question should fall back when no summary, got %q", got)
	}
}
