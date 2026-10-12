package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"text/template"

	"github.com/slack-go/slack"
)

// threadSpec is one mission's entry in the `threads` setting. Both fields
// are text/template strings over the run's inputs, e.g. "{{.issue}}".
type threadSpec struct {
	// Key names the request a run belongs to. Runs whose keys render the
	// same share one thread, which is what keeps a chained or resumed run in
	// its request's thread: squadron records no link between those runs.
	Key string `json:"key"`
	// Title is the top-level message when the gateway has to start the
	// thread itself, so the thread can be found by searching the channel.
	Title string `json:"title"`
}

func parseThreadSpecs(raw string) (map[string]threadSpec, error) {
	specs := map[string]threadSpec{}
	if raw == "" {
		return specs, nil
	}
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	for mission, s := range specs {
		for _, t := range []string{s.Key, s.Title} {
			if _, err := template.New("").Parse(t); err != nil {
				return nil, fmt.Errorf("threads.%s: %w", mission, err)
			}
		}
	}
	return specs, nil
}

// render fails on an input the run doesn't have rather than printing
// "<no value>", so a misconfigured key can't merge unrelated runs.
func render(tmpl string, inputs map[string]any) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, inputs); err != nil {
		return "", err
	}
	return b.String(), nil
}

// renderSpec renders a mission's key and title, logging rather than
// failing: a run whose key can't render still gets a thread of its own.
func (g *slackGateway) renderSpec(mission string, inputs map[string]any) (key, title string) {
	spec, ok := g.threadSpecs[mission]
	if !ok {
		return "", ""
	}
	key, err := render(spec.Key, inputs)
	if err != nil {
		log.Printf("threads.%s key: %v", mission, err)
		key = ""
	}
	title, err = render(spec.Title, inputs)
	if err != nil {
		log.Printf("threads.%s title: %v", mission, err)
		title = ""
	}
	return key, title
}

// runThread is where a run's messages go. ts is "" when the run has no
// thread yet; key and title are then what a new thread is filed under.
type runThread struct {
	ts          string
	key         string
	title       string
	missionName string
}

// threadForMission finds the run's request thread: one the gateway already
// mapped the run to, or, through MCP, the thread of the run's key.
func (g *slackGateway) threadForMission(ctx context.Context, missionID string) runThread {
	g.mu.Lock()
	ts := g.missionThreads[missionID]
	m := g.mcp
	g.mu.Unlock()
	if ts != "" || missionID == "" || m == nil {
		return runThread{ts: ts}
	}
	details, err := m.getRunDetails(ctx, missionID)
	if err != nil {
		log.Printf("look up thread for mission %s: %v", missionID, err)
		return runThread{}
	}
	key, title := g.renderSpec(details.MissionName, details.Inputs)
	rt := runThread{key: key, title: title, missionName: details.MissionName}
	if key == "" {
		return rt
	}
	g.mu.Lock()
	rt.ts = g.keyThreads[key]
	g.mu.Unlock()
	if rt.ts != "" {
		g.rememberRun(missionID, rt.ts)
	}
	return rt
}

// startThread posts the top-level message for a run that has no thread yet,
// so its questions and results still collect under a single message.
func (g *slackGateway) startThread(ctx context.Context, missionID string, rt runThread, fallbackTitle string) (string, error) {
	title := rt.title
	if title == "" {
		title = fallbackTitle
	}
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
	g.rememberKey(rt.key, ts)
	return ts, nil
}

func (g *slackGateway) rememberRun(missionID, ts string) {
	if missionID == "" {
		return
	}
	g.mu.Lock()
	g.missionThreads[missionID] = ts
	g.mu.Unlock()
	g.saveCheckpoint()
}

func (g *slackGateway) rememberKey(key, ts string) {
	if key == "" {
		return
	}
	g.mu.Lock()
	g.keyThreads[key] = ts
	g.mu.Unlock()
	g.saveCheckpoint()
}

func (g *slackGateway) forgetKey(key string) {
	if key == "" {
		return
	}
	g.mu.Lock()
	delete(g.keyThreads, key)
	g.mu.Unlock()
	g.saveCheckpoint()
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
