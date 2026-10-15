package main

import (
	"context"
	"fmt"
	"strings"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
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
	posted, ts, err := client.PostMessageContext(ctx, channel, slack.MsgOptionText(title, false))
	if err != nil {
		return "", fmt.Errorf("post thread root: %w", err)
	}
	thread := threadID(posted, ts)
	g.rememberRun(missionID, thread)
	return thread, nil
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

// Link joins a run to the request filed under key, so a run whose first
// message is a question or notification still lands in that request instead
// of opening its own. A key with no request yet is filed under the run's
// thread, if it has one, the same as a post would.
func (g *slackGateway) Link(_ context.Context, req gatewaysdk.LinkRequest) error {
	if req.Key == "" || req.MissionID == "" {
		return fmt.Errorf("link needs a key and the run's mission id")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if thread := g.keyThreads[req.Key]; thread != "" {
		g.missionThreads[req.MissionID] = thread
	} else if thread := g.missionThreads[req.MissionID]; thread != "" {
		g.keyThreads[req.Key] = thread
	} else {
		return fmt.Errorf("no request is filed under %q yet; post with that thread key to open one", req.Key)
	}
	g.writeCheckpointLocked()
	return nil
}

func (g *slackGateway) threadForKey(key string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.keyThreads[key]
}

// threadID names a request thread by its channel and its top-level
// message's Slack ts. Slack only guarantees a ts is unique within one
// conversation (docs.slack.dev/messaging/retrieving-messages), so a ts on
// its own could match requests in two channels.
func threadID(channel, ts string) string {
	return channel + "/" + ts
}

// splitThreadID gives back the channel every post for the request must go
// to, and the ts its replies hang off.
func splitThreadID(thread string) (channel, ts string) {
	channel, ts, _ = strings.Cut(thread, "/")
	return channel, ts
}

// request is a /squadron start, filed under its request thread ID. Its
// requester and mission line are put back on each root rewrite.
type request struct {
	Requester   string `json:"requester"`
	MissionLine string `json:"mission_line"`
}

func (g *slackGateway) rememberRequest(thread, userID, missionLine string) {
	g.mu.Lock()
	g.requests[thread] = request{Requester: userID, MissionLine: missionLine}
	g.writeCheckpointLocked()
	g.mu.Unlock()
}

// requestLines are the gateway's own lines on a request's top-level
// message: the run it started and who asked. A mission rewriting that
// message doesn't know either, so they're put back on every rewrite.
func (g *slackGateway) requestLines(ts string) string {
	g.mu.Lock()
	req := g.requests[ts]
	g.mu.Unlock()
	run, user := req.MissionLine, req.Requester
	var lines []string
	if run != "" {
		lines = append(lines, run)
	}
	if user != "" {
		lines = append(lines, requesterText(user))
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
