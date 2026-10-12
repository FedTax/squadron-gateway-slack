package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Checkpoint = what a restart needs to pick up where it left off: the
// latest event timestamp processed, so catch-up replays only what it
// missed, plus the request threads, so a restarted gateway keeps posting
// each mission into the thread it started in and tagging its requester.
// Lost or corrupt files fall back to empty (replay everything in
// squadron's recent window; missions are looked up again through MCP).

type checkpoint struct {
	Latest time.Time `json:"latest"`
	// MissionThreads maps mission id → request thread ts.
	MissionThreads map[string]string `json:"mission_threads,omitempty"`
	// Requesters maps request thread ts → Slack user id of who started it.
	Requesters map[string]string `json:"requesters,omitempty"`
}

func (g *slackGateway) readCheckpoint() checkpoint {
	if g.checkpointPath == "" {
		return checkpoint{}
	}
	data, err := os.ReadFile(g.checkpointPath)
	if err != nil {
		return checkpoint{}
	}
	var cp checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return checkpoint{}
	}
	return cp
}

func (g *slackGateway) advanceCheckpoint(t time.Time) {
	if t.IsZero() {
		return
	}
	g.mu.Lock()
	g.latest = t
	g.mu.Unlock()
	g.saveCheckpoint()
}

// saveCheckpoint holds g.mu through the write so concurrent saves can't
// interleave and leave a stale thread map on disk.
func (g *slackGateway) saveCheckpoint() {
	if g.checkpointPath == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	data, err := json.Marshal(checkpoint{
		Latest:         g.latest,
		MissionThreads: g.missionThreads,
		Requesters:     g.requesters,
	})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(g.checkpointPath), 0755); err != nil {
		return
	}
	_ = os.WriteFile(g.checkpointPath, data, 0644)
}
