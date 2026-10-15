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
	dir := t.TempDir()
	g := newSlackGateway()
	g.checkpointPath = filepath.Join(dir, "cp.json")
	thread := threadID("CTEST", "1700000000.000100")

	g.rememberRequest(thread, "U1", "*rate_triage*")
	g.rememberRun("mission-1", thread)
	g.rememberKey("DEV-123", thread)

	cp := g.readCheckpoint()
	if cp.MissionThreads["mission-1"] != thread {
		t.Errorf("mission thread not persisted: %+v", cp.MissionThreads)
	}
	if cp.KeyThreads["DEV-123"] != thread {
		t.Errorf("key thread not persisted: %+v", cp.KeyThreads)
	}
	if cp.Requests[thread].Requester != "U1" {
		t.Errorf("requester not persisted: %+v", cp.Requests)
	}
	if got := g.requestLines(thread); got != "*rate_triage*\nRequester: <@U1>" {
		t.Errorf("requesterLine: got %q", got)
	}
	if got := g.requestLines(threadID("COTHER", "1700000000.000100")); got != "" {
		t.Errorf("the same ts in another channel is another request; got %q", got)
	}
}

func TestSplitThreadIDGivesBackChannelAndTS(t *testing.T) {
	channel, ts := splitThreadID(threadID("D0APPDM", "1700000000.000100"))
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
