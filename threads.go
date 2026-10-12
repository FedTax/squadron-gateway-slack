package main

import (
	"context"
	"fmt"
	"log"

	"github.com/slack-go/slack"
)

// slackThreadInput is the mission input that carries a request's thread
// timestamp. A mission that declares it can pass it on when it routes to
// another mission, which is the only way a chained run finds its way back
// to the thread: squadron records no link between the two runs.
const slackThreadInput = "slack_thread"

// threadForMission returns the timestamp of the mission's request thread —
// the one top-level message a request owns, with everything else about it
// posted as replies. A run the gateway didn't start itself (chained,
// resumed, or started elsewhere) is looked up through MCP by its
// slack_thread input.
func (g *slackGateway) threadForMission(ctx context.Context, missionID string) (string, bool) {
	g.mu.Lock()
	ts, ok := g.missionThreads[missionID]
	m := g.mcp
	g.mu.Unlock()
	if ok || missionID == "" || m == nil {
		return ts, ok
	}
	details, err := m.getRunDetails(ctx, missionID)
	if err != nil {
		log.Printf("look up thread for mission %s: %v", missionID, err)
		return "", false
	}
	ts, _ = details.Inputs[slackThreadInput].(string)
	if ts == "" {
		return "", false
	}
	g.rememberThread(missionID, ts)
	return ts, true
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
	g.rememberThread(missionID, ts)
	return ts, nil
}

func (g *slackGateway) rememberThread(missionID, ts string) {
	g.mu.Lock()
	g.missionThreads[missionID] = ts
	g.mu.Unlock()
	g.saveCheckpoint()
}

// rememberRequester is recorded before the mission starts, so a question the
// run asks before run_mission even returns still tags the requester.
func (g *slackGateway) rememberRequester(ts, userID string) {
	g.mu.Lock()
	g.requesters[ts] = userID
	g.mu.Unlock()
	g.saveCheckpoint()
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
