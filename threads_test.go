package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/slack-go/slack"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
)

func TestFallbackThreadTitleNamesMissionAndQuestion(t *testing.T) {
	got := fallbackThreadTitle(gatewaysdk.HumanInputRecord{MissionName: "rate_triage", ShortSummary: "Need the CO rate source"})
	if !strings.Contains(got, "rate_triage") || !strings.Contains(got, "Need the CO rate source") {
		t.Errorf("got %q", got)
	}
}

func TestRelatedRunsShareQuestionFirstConversation(t *testing.T) {
	g, calls := editTestGateway(t)
	ctx := context.Background()
	if err := g.postQuestion(ctx, gatewaysdk.HumanInputRecord{MissionID: "run-a", ThreadID: "chain", ToolCallID: "q1", Question: "Proceed?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-b", ThreadID: "chain", Payload: `{"text":"follow-up"}`}); err != nil {
		t.Fatal(err)
	}
	if err := g.postNotification(ctx, gatewaysdk.NotificationRecord{MissionID: "run-c", ThreadID: "chain", Event: "mission_completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-d", ThreadID: "other-chain", Payload: `{"text":"separate"}`}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 5 {
		t.Fatalf("calls: %+v", got)
	}
	for _, c := range got[1:4] {
		if c.threadTS != got[0].ts {
			t.Errorf("related run escaped root: %+v", c)
		}
	}
	if got[4].threadTS != "" {
		t.Errorf("unrelated run joined root: %+v", got[4])
	}
}

func TestConcurrentFirstMessagesCreateOneRoot(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var calls []recordedCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c := recordedCall{channel: r.Form.Get("channel"), threadTS: r.Form.Get("thread_ts")}
		mu.Lock()
		c.ts = fmt.Sprintf("1700000000.%06d", len(calls)+1)
		calls = append(calls, c)
		first := len(calls) == 1
		mu.Unlock()
		if first {
			close(entered)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": c.channel, "ts": c.ts})
	}))
	defer api.Close()
	g := newSlackGateway()
	g.client = slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/"))
	g.channelID = "CTEST"
	errs := make(chan error, 3)
	go func() {
		_, err := g.postMessage(context.Background(), gatewaysdk.PostMessageRequest{MissionID: "a", ThreadID: "shared", Payload: `{"text":"first"}`})
		errs <- err
	}()
	<-entered
	go func() {
		errs <- g.postQuestion(context.Background(), gatewaysdk.HumanInputRecord{MissionID: "b", ThreadID: "shared", ToolCallID: "q", Question: "next?"})
	}()
	// A different mission thread must progress while the first waits for Slack.
	go func() {
		_, err := g.postMessage(context.Background(), gatewaysdk.PostMessageRequest{MissionID: "c", ThreadID: "other", Payload: `{"text":"independent"}`})
		errs <- err
	}()
	select {
	case err := <-errs:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("unrelated thread blocked")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("calls: %+v", calls)
	}
	if calls[2].threadTS != calls[0].ts {
		t.Fatalf("concurrent question created another root: %+v", calls)
	}
}
