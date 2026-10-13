package main

import (
	"strings"
	"testing"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
)

func TestFallbackThreadTitleNamesMissionAndQuestion(t *testing.T) {
	got := fallbackThreadTitle(gatewaysdk.HumanInputRecord{MissionName: "rate_triage", ShortSummary: "Need the CO rate source"})
	if !strings.Contains(got, "rate_triage") || !strings.Contains(got, "Need the CO rate source") {
		t.Errorf("got %q", got)
	}
}

func TestThreadForPostJoinsRunToKeyThread(t *testing.T) {
	g := newSlackGateway()
	g.checkpointPath = ""
	g.keyThreads["DEV-123"] = "1700000000.000100"

	if got := g.threadForPost("run-2", "DEV-123"); got != "1700000000.000100" {
		t.Fatalf("got %q", got)
	}
	if got := g.threadForMission("run-2"); got != "1700000000.000100" {
		t.Errorf("a run that posts under a key must keep that thread for its questions; got %q", got)
	}
	if got := g.threadForPost("run-3", ""); got != "" {
		t.Errorf("no run mapping and no key must start a new thread; got %q", got)
	}
}

func TestThreadForPostFilesKeyUnderRunThread(t *testing.T) {
	g := newSlackGateway()
	g.checkpointPath = ""
	g.missionThreads["run-1"] = "1700000000.000100"

	if got := g.threadForPost("run-1", "DEV-123"); got != "1700000000.000100" {
		t.Fatalf("got %q", got)
	}
	if got := g.threadForPost("run-2", "DEV-123"); got != "1700000000.000100" {
		t.Errorf("a later run posting the same key must join the started run's thread; got %q", got)
	}
}
