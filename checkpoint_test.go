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

	g.rememberRequester("1700000000.000100", "U1")
	g.rememberRun("mission-1", "1700000000.000100")
	g.rememberKey("DEV-123", "1700000000.000100")

	cp := g.readCheckpoint()
	if cp.MissionThreads["mission-1"] != "1700000000.000100" {
		t.Errorf("mission thread not persisted: %+v", cp.MissionThreads)
	}
	if cp.KeyThreads["DEV-123"] != "1700000000.000100" {
		t.Errorf("key thread not persisted: %+v", cp.KeyThreads)
	}
	if cp.Requesters["1700000000.000100"] != "U1" {
		t.Errorf("requester not persisted: %+v", cp.Requesters)
	}
	if got := g.requesterLine("1700000000.000100"); got != "Requester: <@U1>" {
		t.Errorf("requesterLine: got %q", got)
	}
	if got := g.requesterLine("1700000000.999999"); got != "" {
		t.Errorf("requesterLine unknown thread: got %q", got)
	}

	g.forgetKey("DEV-123")
	if got := g.threadForKey("DEV-123"); got != "" {
		t.Errorf("forgotten key still maps to %q", got)
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
