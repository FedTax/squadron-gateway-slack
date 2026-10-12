package main

import (
	"strings"
	"testing"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
)

func TestParseThreadSpecs(t *testing.T) {
	specs, err := parseThreadSpecs(`{"rate_triage": {"key": "{{.issue}}", "title": "{{.issue}}: rate ticket"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if specs["rate_triage"].Key != "{{.issue}}" {
		t.Errorf("got %+v", specs)
	}
	if specs, err := parseThreadSpecs(""); err != nil || len(specs) != 0 {
		t.Errorf("empty setting: specs=%v err=%v", specs, err)
	}
	if _, err := parseThreadSpecs(`{"m": {"key": "{{.issue"}}`); err == nil {
		t.Error("a template that doesn't parse must fail Configure")
	}
	if _, err := parseThreadSpecs(`not json`); err == nil {
		t.Error("invalid JSON must fail Configure")
	}
}

func TestRenderSpecSkipsMissingInputs(t *testing.T) {
	g := newSlackGateway()
	g.threadSpecs = map[string]threadSpec{"rate_triage": {Key: "{{.issue}}", Title: "{{.issue}}: rate ticket"}}

	key, title := g.renderSpec("rate_triage", map[string]any{"issue": "DEV-123"})
	if key != "DEV-123" || title != "DEV-123: rate ticket" {
		t.Errorf("got key=%q title=%q", key, title)
	}
	if key, _ := g.renderSpec("rate_triage", map[string]any{}); key != "" {
		t.Errorf("a missing input must not render a key, got %q", key)
	}
	if key, title := g.renderSpec("unconfigured", map[string]any{"issue": "x"}); key != "" || title != "" {
		t.Errorf("unconfigured mission: key=%q title=%q", key, title)
	}
}

func TestFallbackThreadTitleNamesMissionAndQuestion(t *testing.T) {
	got := fallbackThreadTitle(gatewaysdk.HumanInputRecord{ShortSummary: "Need the CO rate source"}, "rate_triage")
	if !strings.Contains(got, "rate_triage") || !strings.Contains(got, "Need the CO rate source") {
		t.Errorf("got %q", got)
	}
}
