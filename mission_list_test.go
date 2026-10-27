package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

func TestMissionListPreservesRootAcrossEditsAndRestart(t *testing.T) {
	g, calls := editTestGateway(t)
	g.checkpointPath = filepath.Join(t.TempDir(), "checkpoint.json")
	ctx := context.Background()
	root, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{ThreadID: "chain", MissionID: "a", RunURL: "https://example.test/runs/a", Payload: `{"text":"Original","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Original block"}}]}`})
	if err != nil {
		t.Fatal(err)
	}
	g.rememberRequest("chain", root, "U1")
	if err := g.editMessage(ctx, gatewaysdk.EditMessageRequest{ThreadID: "chain", MissionID: "b", RunURL: "https://example.test/runs/b", MessageID: root, Payload: `{"text":"Updated","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Updated block"}}]}`}); err != nil {
		t.Fatal(err)
	}
	restored := newSlackGateway()
	restored.client, restored.channelID, restored.checkpointPath = g.client, g.channelID, g.checkpointPath
	restored.loadCheckpoint()
	question := gatewaysdk.HumanInputRecord{ThreadID: "chain", MissionID: "c", RunURL: "https://example.test/runs/c", MissionName: "Review", ToolCallID: "q", Question: "Ready?"}
	if err := restored.postQuestion(ctx, question); err != nil {
		t.Fatal(err)
	}
	got := calls()
	update := got[len(got)-2]
	if update.method != "chat.update" || !strings.HasPrefix(update.text, "Updated\n") || !strings.Contains(update.text, "Requester: <@U1>") {
		t.Fatalf("root body/requester lost: %+v", update)
	}
	for _, id := range []string{"a", "b", "c"} {
		if strings.Count(update.text, "https://example.test/runs/"+id+"|") != 1 {
			t.Fatalf("missing or duplicate run %s: %s", id, update.text)
		}
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(update.blocks), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || !strings.Contains(string(blocks[0]), "Updated block") {
		t.Fatalf("root blocks lost or footer duplicated: %s", update.blocks)
	}
	before := len(calls())
	if err := restored.postQuestion(ctx, question); err != nil {
		t.Fatal(err)
	}
	if len(calls()) != before {
		t.Fatal("duplicate question changed root")
	}
	if err := restored.postNotification(ctx, gatewaysdk.NotificationRecord{ThreadID: "chain", MissionID: "c", RunURL: "https://example.test/runs/c", Event: "mission_completed"}); err != nil {
		t.Fatal(err)
	}
	if len(calls()) != before+1 {
		t.Fatal("known mission refreshed footer unnecessarily")
	}
	if len(restored.readCheckpoint().Conversations["chain"].Missions) != 3 {
		t.Fatal("mission list not persisted")
	}
	if _, err := restored.postMessage(ctx, gatewaysdk.PostMessageRequest{ThreadID: "chain", MissionID: "d", RunURL: "https://example.test/runs/d", Payload: `{"text":"Final","root":true}`}); err != nil {
		t.Fatal(err)
	}
	latest := calls()[len(calls())-1]
	if !strings.HasPrefix(latest.text, "Final\n") || !strings.Contains(latest.text, "runs/d|") || !strings.Contains(latest.text, "runs/a|") {
		t.Fatalf("root replacement lost missions: %s", latest.text)
	}
}

func TestMissionListRetriesFailedRefreshAfterRestart(t *testing.T) {
	g, _ := editTestGateway(t)
	ctx := context.Background()
	g.checkpointPath = filepath.Join(t.TempDir(), "checkpoint.json")
	if _, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{ThreadID: "chain", MissionID: "a", RunURL: "https://example.test/runs/a", Payload: `{"text":"Keep this body"}`}); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/chat.update" {
			if attempts.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"ok":false,"error":"internal_error"}`))
				return
			}
			text := r.Form.Get("text")
			if !strings.HasPrefix(text, "Keep this body\n") || strings.Count(text, "runs/b|") != 1 {
				t.Errorf("bad retried footer: %s", text)
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"channel":"CTEST","ts":"123.456"}`))
	}))
	defer api.Close()
	g.client = slack.New("x", slack.OptionAPIURL(api.URL+"/"))
	req := gatewaysdk.PostMessageRequest{ThreadID: "chain", MissionID: "b", RunURL: "https://example.test/runs/b", Payload: `{"text":"Reply"}`}
	if _, err := g.postMessage(ctx, req); err == nil {
		t.Fatal("expected refresh failure")
	}
	restored := newSlackGateway()
	restored.client, restored.checkpointPath = g.client, g.checkpointPath
	restored.loadCheckpoint()
	if _, err := restored.postMessage(ctx, req); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || restored.readCheckpoint().Conversations["chain"].FooterDirty {
		t.Fatal("pending footer not retried and cleared")
	}
}

func TestExistingMissionGetsSuppliedLinkWithoutDuplicate(t *testing.T) {
	g, calls := editTestGateway(t)
	ctx := context.Background()
	root, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{ThreadID: "chain", MissionID: "a", Payload: `{"text":"Body"}`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(calls()[0].text, "<http") {
		t.Fatal("invented a URL")
	}
	rec := gatewaysdk.NotificationRecord{ThreadID: "chain", MissionID: "a", MissionName: "Triage", RunURL: "https://cc.example/instances/1/runs/a", Event: "mission_completed"}
	if err := g.postNotification(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if len(g.conversations["chain"].Missions) != 1 {
		t.Fatal("duplicated existing run")
	}
	if lines := g.requestLines(root); lines != "*<https://cc.example/instances/1/runs/a|Triage>*" {
		t.Fatalf("supplied link/name not rendered: %s", lines)
	}
	if _, err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{ThreadID: "chain", MissionID: "a", Payload: `{"text":"Later","root":true}`}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls()[len(calls())-1].text, "runs/a|Triage>") {
		t.Fatal("missing metadata erased known link")
	}
}
