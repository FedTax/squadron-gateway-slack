package main

import (
	"context"
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

func TestLinkJoinsRunToKeysRequest(t *testing.T) {
	g := newSlackGateway()
	g.keyThreads["DEV-123"] = "C1/1700000000.000100"
	g.missionThreads["run-2"] = "C9/1700000000.000999"
	if err := g.Link(context.Background(), gatewaysdk.LinkRequest{Key: "DEV-123", MissionID: "run-2"}); err != nil {
		t.Fatal(err)
	}
	if got := g.threadForMission("run-2"); got != "C1/1700000000.000100" {
		t.Errorf("a linked run must move to the key's request; got %q", got)
	}
}

func TestLinkFilesNewKeyUnderRunsThread(t *testing.T) {
	g := newSlackGateway()
	g.missionThreads["run-1"] = "C1/1700000000.000100"
	if err := g.Link(context.Background(), gatewaysdk.LinkRequest{Key: "DEV-123", MissionID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	if got := g.threadForKey("DEV-123"); got != "C1/1700000000.000100" {
		t.Errorf("got %q", got)
	}
}

func TestLinkWithNoRequestYetFails(t *testing.T) {
	g := newSlackGateway()
	if err := g.Link(context.Background(), gatewaysdk.LinkRequest{Key: "DEV-123", MissionID: "run-1"}); err == nil {
		t.Error("linking to a key no request is filed under must say so")
	}
	if g.threadForMission("run-1") != "" {
		t.Error("a failed link must not link the run")
	}
}
