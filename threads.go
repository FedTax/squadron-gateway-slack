package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// conversation holds the Slack destination shared by a mission thread
// (a chain of Squadron runs). Root is a Slack channel/timestamp reference.
type conversation struct {
	mu          sync.Mutex
	Root        string          `json:"root,omitempty"`
	Requester   string          `json:"requester,omitempty"`
	Missions    []missionEntry  `json:"missions,omitempty"`
	RootText    string          `json:"root_text,omitempty"`
	RootBlocks  json.RawMessage `json:"root_blocks,omitempty"`
	FooterDirty bool            `json:"footer_dirty,omitempty"`
}

type missionEntry struct {
	ID   string `json:"id"`
	Line string `json:"line"`
}

// rememberMission keeps one entry per run, in first-seen order.
func (g *slackGateway) rememberMission(threadID, missionID, name string) {
	if threadID == "" || missionID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.conversations[threadID]
	if c == nil {
		c = &conversation{}
		g.conversations[threadID] = c
	}
	for _, m := range c.Missions {
		if m.ID == missionID {
			return
		}
	}
	if name == "" {
		name = missionID
	}
	c.Missions = append(c.Missions, missionEntry{ID: missionID, Line: g.missionLine(name, missionID)})
	c.FooterDirty = true
	g.writeCheckpointLocked()
}

// rememberRootContent retains the body separately so footer updates preserve it.
func (g *slackGateway) rememberRootContent(root, text string, blocks json.RawMessage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.conversations {
		if c.Root == root {
			c.RootText, c.RootBlocks, c.FooterDirty = text, append(json.RawMessage(nil), blocks...), false
			g.writeCheckpointLocked()
			return
		}
	}
}

// refreshRoot updates a changed mission list while retaining the root body.
// The caller holds the conversation lock through the Slack update.
func (g *slackGateway) refreshRoot(ctx context.Context, threadID string) error {
	g.mu.Lock()
	c := g.conversations[threadID]
	if c == nil || c.Root == "" || !c.FooterDirty {
		g.mu.Unlock()
		return nil
	}
	root, text, blocks, client := c.Root, c.RootText, c.RootBlocks, g.client
	g.mu.Unlock()
	channel, ts := splitSlackThreadRef(root)
	if _, _, _, err := client.UpdateMessageContext(ctx, channel, ts, messageOptions(text, blocks, g.requestLines(root))...); err != nil {
		return fmt.Errorf("update mission list: %w", err)
	}
	g.rememberRootContent(root, text, blocks)
	return nil
}

// lockConversation acquires exclusive access for a nonempty mission thread ID.
// Callers hold it through root lookup, creation and recording. The map lock is
// released before waiting so other mission threads can proceed.
func (g *slackGateway) lockConversation(missionThreadID string) func() {
	if missionThreadID == "" {
		return func() {}
	}
	g.mu.Lock()
	c := g.conversations[missionThreadID]
	if c == nil {
		c = &conversation{}
		g.conversations[missionThreadID] = c
	}
	g.mu.Unlock()
	c.mu.Lock()
	return c.mu.Unlock
}

func (g *slackGateway) threadForMissionThread(missionThreadID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c := g.conversations[missionThreadID]; c != nil {
		return c.Root
	}
	return ""
}

// startThread creates the root for a question-first conversation.
// The caller holds the mission thread's conversation lock.
func (g *slackGateway) startThread(ctx context.Context, missionThreadID, title string) (string, error) {
	g.mu.Lock()
	client, channel := g.client, g.channelID
	g.mu.Unlock()
	if client == nil {
		return "", fmt.Errorf("slack client not initialized")
	}
	posted, ts, err := client.PostMessageContext(ctx, channel, messageOptions(title, nil, g.threadLines(missionThreadID))...)
	if err != nil {
		return "", fmt.Errorf("post thread root: %w", err)
	}
	root := slackThreadRef(posted, ts)
	g.rememberThread(missionThreadID, root)
	g.rememberRootContent(root, title, nil)
	return root, nil
}

func (g *slackGateway) rememberThread(missionThreadID, root string) {
	if missionThreadID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.conversations[missionThreadID]
	if c == nil {
		c = &conversation{}
		g.conversations[missionThreadID] = c
	}
	c.Root = root
	g.writeCheckpointLocked()
}

// slackThreadRef includes the channel because Slack timestamps are only
// unique within a conversation.
func slackThreadRef(channel, ts string) string { return channel + "/" + ts }

func splitSlackThreadRef(ref string) (channel, ts string) {
	channel, ts, _ = strings.Cut(ref, "/")
	return channel, ts
}

func (g *slackGateway) rememberRequest(missionThreadID, root, userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.conversations[missionThreadID]
	if c == nil {
		c = &conversation{}
		g.conversations[missionThreadID] = c
	}
	c.Root, c.Requester = root, userID
	g.writeCheckpointLocked()
}

// requestLines preserves the gateway-owned attribution when a root is replaced.
func (g *slackGateway) requestLines(root string) string {
	if root == "" {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.conversations {
		if c.Root != root {
			continue
		}
		return conversationLines(c)
	}
	return ""
}

func (g *slackGateway) threadLines(threadID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return conversationLines(g.conversations[threadID])
}

func conversationLines(c *conversation) string {
	if c == nil {
		return ""
	}
	var lines []string
	for _, m := range c.Missions {
		lines = append(lines, m.Line)
	}
	if c.Requester != "" {
		lines = append(lines, requesterText(c.Requester))
	}
	return strings.Join(lines, "\n")
}

// missionLine names a run on its request's top-level message, linked to
// the run's page when run_url is set so it can be opened from Slack.
func (g *slackGateway) missionLine(mission, missionID string) string {
	if g.runURL == "" {
		return "*" + mission + "* (run `" + missionID + "`)"
	}
	return "*<" + strings.ReplaceAll(g.runURL, "{mission_id}", missionID) + "|" + mission + ">*"
}

func requesterText(user string) string {
	return "Requester: <@" + user + ">"
}
