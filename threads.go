package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/slack-go/slack"
)

// conversation holds the Slack destination shared by a mission thread
// (a chain of Squadron runs). Root is a Slack channel/timestamp reference.
type conversation struct {
	mu          sync.Mutex
	Root        string `json:"root,omitempty"`
	Requester   string `json:"requester,omitempty"`
	MissionLine string `json:"mission_line,omitempty"`
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
	posted, ts, err := client.PostMessageContext(ctx, channel, slack.MsgOptionText(title, false))
	if err != nil {
		return "", fmt.Errorf("post thread root: %w", err)
	}
	root := slackThreadRef(posted, ts)
	g.rememberThread(missionThreadID, root)
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

func (g *slackGateway) rememberRequest(missionThreadID, root, userID, missionLine string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.conversations[missionThreadID]
	if c == nil {
		c = &conversation{}
		g.conversations[missionThreadID] = c
	}
	c.Root, c.Requester, c.MissionLine = root, userID, missionLine
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
		var lines []string
		if c.MissionLine != "" {
			lines = append(lines, c.MissionLine)
		}
		if c.Requester != "" {
			lines = append(lines, requesterText(c.Requester))
		}
		return strings.Join(lines, "\n")
	}
	return ""
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
