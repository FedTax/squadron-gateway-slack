package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

// TestMockSlackPreview records real gateway HTTP requests against local Slack and
// mission-start substitutes. Scripted events make transport behavior reproducible
// without asking a model to choose the sequence or sending messages to a workspace.
func TestMockSlackPreview(t *testing.T) {
	output := os.Getenv("SQUADRON_SLACK_PREVIEW")
	if output == "" {
		t.Skip("set SQUADRON_SLACK_PREVIEW to a JSON output path to record the mock walkthrough")
	}
	type call struct {
		Scenario string         `json:"scenario"`
		Step     string         `json:"step"`
		Method   string         `json:"method"`
		Payload  map[string]any `json:"payload"`
		TS       string         `json:"ts"`
	}
	type check struct {
		Scenario    string `json:"scenario"`
		Description string `json:"description"`
		Passed      bool   `json:"passed"`
	}
	var mu sync.Mutex
	var calls []call
	var checks []check
	scenario, step := "", ""
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{}
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
		} else {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			for k, v := range r.PostForm {
				if len(v) > 0 {
					payload[k] = v[0]
				}
			}
		}
		delete(payload, "token")
		for _, k := range []string{"blocks", "view"} {
			if s, ok := payload[k].(string); ok {
				var v any
				if json.Unmarshal([]byte(s), &v) == nil {
					payload[k] = v
				}
			}
		}
		mu.Lock()
		ts := fmt.Sprintf("1700000000.%06d", len(calls)+1)
		if existing, ok := payload["ts"].(string); ok && existing != "" {
			ts = existing
		}
		calls = append(calls, call{scenario, step, strings.TrimPrefix(r.URL.Path, "/"), payload, ts})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "CTEST", "ts": ts, "text": payload["text"], "view": map[string]any{"id": "VTEST", "type": "modal"}})
	}))
	defer api.Close()

	missions := server.NewMCPServer("mock-squadron", "test", server.WithToolCapabilities(true))
	missions.AddTool(mcp.NewTool("list_missions"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"missions":[{"name":"rate_triage"}]}`), nil
	})
	missions.AddTool(mcp.NewTool("get_mission_config"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"inputs":[{"name":"issue","type":"string","required":true,"description":"Synthetic case key"},{"name":"repo_url","type":"string","required":false,"default":"https://example.invalid/repository"},{"name":"states","type":"list","required":false,"default":["CO","PA"]},{"name":"secret","type":"string","protected":true}]}`), nil
	})
	missions.AddTool(mcp.NewTool("run_mission"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"missionId":"run-triage"}`), nil
	})
	missionAPI := httptest.NewServer(server.NewStreamableHTTPServer(missions))
	defer missionAPI.Close()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, viaSlash := range []bool{false, true} {
		scenario = "External start"
		if viaSlash {
			scenario = "Slash-command start"
		}
		checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
		fresh := func() *slackGateway {
			g := newSlackGateway()
			g.client = slack.New("xoxb-local-stub", slack.OptionAPIURL(api.URL+"/"), slack.OptionHTTPClient(api.Client()))
			g.channelID = "CTEST"
			g.checkpointPath = checkpointPath
			g.mcp = &squadronMCP{url: missionAPI.URL + "/mcp"}
			return g
		}
		g := fresh()
		if viaSlash {
			step = "Open mission picker"
			g.onSlashCommand(ctx, slack.SlashCommand{ChannelID: "CTEST", UserID: "UTEST", TriggerID: "trigger"})
			step = "Choose mission"
			g.onMissionPicked(ctx, slack.InteractionCallback{View: slack.View{ID: "VTEST"}}, "rate_triage")
			step = "Submit mission"
			g.onStartSubmitted(ctx, nil, slack.InteractionCallback{User: slack.User{ID: "UTEST"}, View: slack.View{CallbackID: startCallbackID, PrivateMetadata: `{"mission":"rate_triage"}`, State: &slack.ViewState{Values: map[string]map[string]slack.BlockAction{startInputPrefix + "string::issue": {"value": {Value: "TEST-001"}}}}}})
		}
		post := func(run, text string, root bool) {
			body, _ := json.Marshal(map[string]any{"text": text, "thread": "TEST-001", "root": root})
			must(g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: run, TaskID: "task-test", Payload: string(body)}))
		}
		step = "Triage root update"
		post("run-triage", "*TEST-001*\nSynthetic rates differ in CO and PA.\nStatus: triaging", true)
		firstRoot := g.threadForMission("run-triage")
		checks = append(checks, check{scenario, "Case key points to the initial root", g.threadForKey("TEST-001") == firstRoot})
		step = "Progress reply"
		post("run-triage", "Investigating synthetic evidence; no external tools were called.", false)
		q := gatewaysdk.HumanInputRecord{MissionID: "run-triage", MissionName: "rate_triage", TaskName: "discover_sessions", ToolCallID: "choice", Question: "Which synthetic state should we inspect?", Choices: []string{"CO", "PA"}, RequestedAt: time.Now()}
		step = "Choice question"
		must(g.postQuestion(ctx, q))
		q.Response = "CO"
		q.ResponderUserID = "slack:UTEST"
		step = "Resolve choice"
		must(g.markResolved(ctx, q))
		q.ToolCallID = "free-text"
		q.Question = "Add a synthetic test note"
		q.Choices = nil
		q.Response = ""
		step = "Free-text question"
		must(g.postQuestion(ctx, q))
		q.Response = "Use the synthetic fixture only."
		step = "Resolve free text"
		must(g.markResolved(ctx, q))
		step = "Routed run root update"
		post("run-fix", "*TEST-001*\nCause: synthetic fixture mismatch.\nStatus: fixing", true)
		checks = append(checks, check{scenario, "Routed run shares the initial root", g.threadForMission("run-fix") == firstRoot})
		q.MissionID = "run-fix"
		q.MissionName = "rate_fix"
		q.ToolCallID = "routed-choice"
		q.Question = "Did the routed question stay in the original thread?"
		q.Choices = []string{"Yes", "No"}
		q.Response = ""
		step = "Routed question"
		must(g.postQuestion(ctx, q))
		step = "Failure notification"
		must(g.postNotification(ctx, gatewaysdk.NotificationRecord{MissionID: "run-fix", MissionName: "rate_fix", Event: "mission_failed", Error: "Intentional mock failure"}))
		g = fresh()
		g.loadCheckpoint()
		step = "Final summary after restart"
		post("run-finalize", "*TEST-001*\nResolves the synthetic fixture mismatch in CO and PA.\nStatus: complete\n<https://example.invalid/fix/pr/1|Synthetic fix PR> · <https://example.invalid/bruno/pr/2|Synthetic Bruno PR>\n<https://example.invalid/session/1|Synthetic development session>", true)
		checks = append(checks, check{scenario, "Restarted gateway rejoins the initial root", g.threadForMission("run-finalize") == firstRoot})
	}
	data, err := json.MarshalIndent(map[string]any{"calls": calls, "checks": checks}, "", "  ")
	must(err)
	must(os.WriteFile(output, append(data, '\n'), 0600))
	for _, c := range checks {
		t.Logf("%s: %s = %v", c.Scenario, c.Description, c.Passed)
	}
	t.Logf("Recorded %d local HTTP requests to %s; checks describe observed behavior, not test assertions", len(calls), output)
}
