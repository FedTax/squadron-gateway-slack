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
// each run and thread key into the thread it started in and tagging its
// requester.
// Lost or corrupt files fall back to empty (replay everything in
// squadron's recent window; missions are looked up again through MCP).

type checkpoint struct {
	Latest time.Time `json:"latest"`
	// MissionThreads maps mission id → request thread ts.
	MissionThreads map[string]string `json:"mission_threads,omitempty"`
	// KeyThreads maps rendered thread key → request thread ts.
	KeyThreads map[string]string `json:"key_threads,omitempty"`
	// Requesters maps request thread ts → Slack user id of who started it.
	Requesters map[string]string `json:"requesters,omitempty"`
	// MissionLines maps request thread ts → the line naming the run it
	// started.
	MissionLines map[string]string `json:"mission_lines,omitempty"`
	// ThreadChannels maps request thread ts → channel, for requests
	// started outside the configured channel.
	ThreadChannels map[string]string `json:"thread_channels,omitempty"`
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

// loadCheckpoint fills the gateway's state from the checkpoint file under
// g.mu, so nothing reads half-loaded thread maps.
func (g *slackGateway) loadCheckpoint() {
	cp := g.readCheckpoint()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.latest = cp.Latest
	for id, ts := range cp.MissionThreads {
		g.missionThreads[id] = ts
	}
	for key, ts := range cp.KeyThreads {
		g.keyThreads[key] = ts
	}
	for ts, user := range cp.Requesters {
		g.requesters[ts] = user
	}
	for ts, line := range cp.MissionLines {
		g.missionLines[ts] = line
	}
	for ts, channel := range cp.ThreadChannels {
		g.threadChannels[ts] = channel
	}
}

// advanceCheckpoint only moves forward: catch-up and live events can arrive
// out of order, and a step back would replay events already handled.
func (g *slackGateway) advanceCheckpoint(t time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !t.After(g.latest) {
		return
	}
	g.latest = t
	g.writeCheckpointLocked()
}

// writeCheckpointLocked must run under g.mu, in the same critical section
// as the change it persists; otherwise a slower writer could overwrite a
// newer state on disk with its older snapshot.
func (g *slackGateway) writeCheckpointLocked() {
	if g.checkpointPath == "" {
		return
	}
	data, err := json.Marshal(checkpoint{
		Latest:         g.latest,
		MissionThreads: g.missionThreads,
		KeyThreads:     g.keyThreads,
		Requesters:     g.requesters,
		MissionLines:   g.missionLines,
		ThreadChannels: g.threadChannels,
	})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(g.checkpointPath), 0755); err != nil {
		return
	}
	_ = os.WriteFile(g.checkpointPath, data, 0644)
}
