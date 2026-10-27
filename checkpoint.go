package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Checkpoints preserve mission-thread conversations and the replay cursor across restarts.
type checkpoint struct {
	Latest        time.Time                `json:"latest"`
	Conversations map[string]*conversation `json:"conversations,omitempty"`
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
	for id, c := range cp.Conversations {
		if c != nil {
			g.conversations[id] = c
		}
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
		Latest:        g.latest,
		Conversations: g.conversations,
	})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(g.checkpointPath), 0755); err != nil {
		return
	}
	_ = os.WriteFile(g.checkpointPath, data, 0644)
}
