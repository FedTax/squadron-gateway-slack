package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	g := newSlackGateway()
	g.checkpointPath = filepath.Join(dir, "cp.json")

	if got := g.readCheckpoint().Latest; !got.IsZero() {
		t.Errorf("readCheckpoint with no file should return zero time, got %v", got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	g.advanceCheckpoint(now)

	if got := g.readCheckpoint().Latest; !got.Equal(now) {
		t.Errorf("readCheckpoint after advance: got %v, want %v", got, now)
	}
}

func TestCheckpointAdvanceIgnoresZeroTime(t *testing.T) {
	dir := t.TempDir()
	g := newSlackGateway()
	g.checkpointPath = filepath.Join(dir, "cp.json")

	now := time.Now().UTC().Truncate(time.Second)
	g.advanceCheckpoint(now)
	g.advanceCheckpoint(time.Time{})

	if got := g.readCheckpoint().Latest; !got.Equal(now) {
		t.Errorf("zero advanceCheckpoint should not clobber a previous valid value; got %v want %v", got, now)
	}
}

func TestCheckpointIsNoOpWhenPathEmpty(t *testing.T) {
	g := newSlackGateway()
	g.checkpointPath = ""

	g.advanceCheckpoint(time.Now())
	if got := g.readCheckpoint().Latest; !got.IsZero() {
		t.Errorf("read with empty path should return zero, got %v", got)
	}
}

func TestCheckpointReadIgnoresMalformedFile(t *testing.T) {
	dir := t.TempDir()
	g := newSlackGateway()
	g.checkpointPath = filepath.Join(dir, "cp.json")
	if err := os.WriteFile(g.checkpointPath, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := g.readCheckpoint().Latest; !got.IsZero() {
		t.Errorf("malformed checkpoint should fall back to zero time, got %v", got)
	}
}

func TestCheckpointPersistsThreads(t *testing.T) {
	g := newSlackGateway()
	g.checkpointPath = filepath.Join(t.TempDir(), "cp.json")
	root := slackThreadRef("CTEST", "1700000000.000100")
	g.rememberMission("chain-1", "run-1", "rate_triage", "")
	g.rememberRequest("chain-1", root, "U1")
	restored := newSlackGateway()
	restored.checkpointPath = g.checkpointPath
	restored.loadCheckpoint()
	if got := restored.threadForMissionThread("chain-1"); got != root {
		t.Fatalf("root: %q", got)
	}
	if got := restored.requestLines(root); got != "*rate_triage* (run `run-1`)\nRequester: <@U1>" {
		t.Fatalf("attribution: %q", got)
	}
}

func TestSplitThreadIDGivesBackChannelAndTS(t *testing.T) {
	channel, ts := splitSlackThreadRef(slackThreadRef("D0APPDM", "1700000000.000100"))
	if channel != "D0APPDM" || ts != "1700000000.000100" {
		t.Errorf("got channel=%q ts=%q", channel, ts)
	}
}

func TestCheckpointNeverMovesBackward(t *testing.T) {
	dir := t.TempDir()
	g := newSlackGateway()
	g.checkpointPath = filepath.Join(dir, "cp.json")

	now := time.Now().UTC().Truncate(time.Second)
	g.advanceCheckpoint(now)
	g.advanceCheckpoint(now.Add(-time.Hour))

	if got := g.readCheckpoint().Latest; !got.Equal(now) {
		t.Errorf("an older event must not move the checkpoint back; got %v want %v", got, now)
	}
}
