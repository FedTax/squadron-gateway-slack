package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

type recordedCall struct{ method, channel, ts, threadTS, text string }

// editTestGateway serves Slack's API locally, giving each new message the
// next ts, and records every call.
func editTestGateway(t *testing.T) (*slackGateway, func() []recordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c := recordedCall{
			method:   strings.TrimPrefix(r.URL.Path, "/"),
			channel:  r.PostForm.Get("channel"),
			ts:       r.PostForm.Get("ts"),
			threadTS: r.PostForm.Get("thread_ts"),
			text:     r.PostForm.Get("text"),
		}
		mu.Lock()
		if c.ts == "" {
			c.ts = "1700000000.00000" + string(rune('1'+len(calls)))
		}
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": c.channel, "ts": c.ts})
	}))
	t.Cleanup(api.Close)
	g := newSlackGateway()
	g.client = slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/"), slack.OptionHTTPClient(api.Client()))
	g.channelID = "CTEST"
	return g, func() []recordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedCall(nil), calls...)
	}
}

func TestPostReturnsTheIDOfTheMessageItCreatedOrReplaced(t *testing.T) {
	g, _ := editTestGateway(t)
	ctx := context.Background()
	post := func(payload string) string {
		t.Helper()
		id, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-1", Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	root := post(`{"text":"Triaging","thread":"DEV-1"}`)
	if root != "CTEST/1700000000.000001" {
		t.Fatalf("opening a thread must return the root's id; got %q", root)
	}
	if reply := post(`{"text":"Update"}`); reply != "CTEST/1700000000.000002" {
		t.Errorf("a reply must return its own id, not the root's; got %q", reply)
	}
	if rewritten := post(`{"text":"Fixing","root":true}`); rewritten != root {
		t.Errorf("a root rewrite must return the root's id; got %q", rewritten)
	}
}

func TestEditUpdatesTheMessageTheIDNames(t *testing.T) {
	g, calls := editTestGateway(t)
	ctx := context.Background()
	if _, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-1", Payload: `{"text":"Triaging","thread":"DEV-1"}`}); err != nil {
		t.Fatal(err)
	}
	reply, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-1", Payload: `{"text":"Checking CO"}`})
	if err != nil {
		t.Fatal(err)
	}

	if err := g.editMessage(ctx, gatewaysdk.EditMessageRequest{MissionID: "run-1", MessageID: reply, Payload: `{"text":"Checked CO: rate differs"}`}); err != nil {
		t.Fatal(err)
	}
	got := calls()[2]
	if want := (recordedCall{method: "chat.update", channel: "CTEST", ts: "1700000000.000002", text: "Checked CO: rate differs"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestEditOfARequestRootKeepsTheGatewayLines(t *testing.T) {
	g, calls := editTestGateway(t)
	root := threadID("CTEST", "1700000000.000009")
	g.requests[root] = request{MissionLine: "*rate_triage* · run-1", Requester: "UTEST"}

	if err := g.editMessage(context.Background(), gatewaysdk.EditMessageRequest{MissionID: "run-1", MessageID: root, Payload: `{"text":"Status: done"}`}); err != nil {
		t.Fatal(err)
	}
	if got := calls()[0].text; got != "Status: done\n*rate_triage* · run-1\nRequester: <@UTEST>" {
		t.Errorf("an edit of a /squadron root must keep the mission and requester lines; got %q", got)
	}
}

func TestEditIsRefusedForQuestionsPlacementAndBadIDs(t *testing.T) {
	g, calls := editTestGateway(t)
	g.messages["call-1"] = postedMessage{channel: "CTEST", ts: "1700000000.000005"}
	ctx := context.Background()
	for name, req := range map[string]gatewaysdk.EditMessageRequest{
		"question": {MessageID: "CTEST/1700000000.000005", Payload: `{"text":"x"}`},
		"thread":   {MessageID: "CTEST/1700000000.000001", Payload: `{"text":"x","thread":"DEV-2"}`},
		"channel":  {MessageID: "CTEST/1700000000.000001", Payload: `{"text":"x","channel":"COTHER"}`},
		"root":     {MessageID: "CTEST/1700000000.000001", Payload: `{"text":"x","root":true}`},
		"no ts":    {MessageID: "CTEST", Payload: `{"text":"x"}`},
		"no id":    {Payload: `{"text":"x"}`},
		"bad JSON": {MessageID: "CTEST/1700000000.000001", Payload: `{`},
	} {
		if err := g.editMessage(ctx, req); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if n := len(calls()); n != 0 {
		t.Errorf("a refused edit must not reach Slack; got %d calls", n)
	}
}
