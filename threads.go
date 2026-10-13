package main

import (
	"context"
	"fmt"

	"github.com/slack-go/slack"
)

// threadForMission returns the request thread the gateway has mapped the
// run to, or "" when the run hasn't started or joined one yet.
func (g *slackGateway) threadForMission(missionID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.missionThreads[missionID]
}

// threadForPost finds the request thread for a gateway.post: the posting
// run's own thread, else the one filed under the post's key. The run and an
// unfiled key are both recorded against that thread, so the run's later
// questions land there and a run started elsewhere under the same key (a
// route, a resume) joins it instead of opening a second top-level message.
func (g *slackGateway) threadForPost(missionID, key string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	ts := g.missionThreads[missionID]
	if ts == "" && key != "" {
		ts = g.keyThreads[key]
	}
	if ts == "" {
		return ""
	}
	changed := false
	if missionID != "" && g.missionThreads[missionID] != ts {
		g.missionThreads[missionID] = ts
		changed = true
	}
	if key != "" && g.keyThreads[key] == "" {
		g.keyThreads[key] = ts
		changed = true
	}
	if changed {
		g.writeCheckpointLocked()
	}
	return ts
}

// startThread posts the top-level message for a run that has no thread yet,
// so its questions and results still collect under a single message.
func (g *slackGateway) startThread(ctx context.Context, missionID, title string) (string, error) {
	g.mu.Lock()
	client := g.client
	channel := g.channelID
	g.mu.Unlock()
	if client == nil {
		return "", fmt.Errorf("slack client not initialized")
	}
	_, ts, err := client.PostMessageContext(ctx, channel, slack.MsgOptionText(title, false))
	if err != nil {
		return "", fmt.Errorf("post thread root: %w", err)
	}
	g.rememberRun(missionID, ts)
	return ts, nil
}

func (g *slackGateway) rememberRun(missionID, ts string) {
	if missionID == "" {
		return
	}
	g.mu.Lock()
	g.missionThreads[missionID] = ts
	g.writeCheckpointLocked()
	g.mu.Unlock()
}

func (g *slackGateway) rememberKey(key, ts string) {
	if key == "" {
		return
	}
	g.mu.Lock()
	g.keyThreads[key] = ts
	g.writeCheckpointLocked()
	g.mu.Unlock()
}

func (g *slackGateway) forgetKey(key string) {
	if key == "" {
		return
	}
	g.mu.Lock()
	delete(g.keyThreads, key)
	g.writeCheckpointLocked()
	g.mu.Unlock()
}

func (g *slackGateway) threadForKey(key string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.keyThreads[key]
}

// rememberRequest records who started a request and, when it lives outside
// the configured channel, where, so its posts after a restart still reach it.
func (g *slackGateway) rememberRequest(ts, channel, userID string) {
	g.mu.Lock()
	g.requesters[ts] = userID
	if channel != g.channelID {
		g.threadChannels[ts] = channel
	}
	g.writeCheckpointLocked()
	g.mu.Unlock()
}

// requestChannel is where a request thread lives: the requester's DM with
// the app when /squadron was run in a DM, otherwise the configured channel.
func (g *slackGateway) requestChannel(ts string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if channel := g.threadChannels[ts]; channel != "" {
		return channel
	}
	return g.channelID
}

// requesterLine names whoever started the request on its top-level
// message. Slack makes a user mentioned in a thread's parent follow the
// thread, so the requester hears about replies without each one tagging them.
func (g *slackGateway) requesterLine(ts string) string {
	g.mu.Lock()
	user := g.requesters[ts]
	g.mu.Unlock()
	if user == "" {
		return ""
	}
	return requesterText(user)
}

func requesterText(user string) string {
	return "Requester: <@" + user + ">"
}
