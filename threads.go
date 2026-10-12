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
// run's own thread, else the one filed under the post's key. A run that
// joins by key is mapped to it, so its later questions land there too.
func (g *slackGateway) threadForPost(missionID, key string) string {
	g.mu.Lock()
	ts := g.missionThreads[missionID]
	if ts == "" && key != "" {
		ts = g.keyThreads[key]
	}
	g.mu.Unlock()
	if ts != "" && missionID != "" {
		g.rememberRun(missionID, ts)
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

// rememberRequester is recorded before the mission starts, so a question the
// run asks before run_mission even returns still tags the requester.
func (g *slackGateway) rememberRequester(ts, userID string) {
	g.mu.Lock()
	g.requesters[ts] = userID
	g.writeCheckpointLocked()
	g.mu.Unlock()
}

// mentionFor returns the tag that notifies whoever started the thread's
// request, or "" for a thread nobody started from Slack.
func (g *slackGateway) mentionFor(ts string) string {
	g.mu.Lock()
	user := g.requesters[ts]
	g.mu.Unlock()
	if user == "" {
		return ""
	}
	return "<@" + user + "> "
}
