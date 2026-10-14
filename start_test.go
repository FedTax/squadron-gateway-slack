package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

func TestStartModalMarksRequiredAndSkipsProtected(t *testing.T) {
	m := startModal([]missionSummary{{Name: "rate_triage"}}, startForm{Mission: "rate_triage"}, []missionInput{
		{Name: "issue", Type: "string", Required: true},
		{Name: "repo_url", Type: "string", Default: json.RawMessage(`"https://github.com/FedTax/x"`)},
		{Name: "token", Type: "string", Protected: true},
	})
	if m.Submit == nil || readStartForm(m.PrivateMetadata).Mission != "rate_triage" {
		t.Fatalf("a picked mission must be submittable and carried in metadata: %+v", m)
	}
	var inputs []*slack.InputBlock
	for _, b := range m.Blocks.BlockSet {
		if ib, ok := b.(*slack.InputBlock); ok {
			inputs = append(inputs, ib)
		}
	}
	if len(inputs) != 2 {
		t.Fatalf("protected inputs must not get a field; got %d fields", len(inputs))
	}
	if inputs[0].Optional || !inputs[1].Optional {
		t.Errorf("required flags: issue optional=%v repo_url optional=%v", inputs[0].Optional, inputs[1].Optional)
	}
	if el := inputs[1].Element.(*slack.PlainTextInputBlockElement); el.InitialValue != "https://github.com/FedTax/x" {
		t.Errorf("string default must be shown as text, got %q", el.InitialValue)
	}
}

func TestStartModalWithoutMissionCannotSubmit(t *testing.T) {
	if m := startModal([]missionSummary{{Name: "a"}}, startForm{}, nil); m.Submit != nil {
		t.Error("the form must not submit before a mission is picked")
	}
}

func TestStartInputsFromSubmissionDecodesTypedValues(t *testing.T) {
	cb := slack.InteractionCallback{View: slack.View{
		CallbackID:      startCallbackID,
		PrivateMetadata: `{"mission":"rate_triage"}`,
		State: &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
			startInputPrefix + "string::issue":    {"value": {Value: "DEV-123"}},
			startInputPrefix + "list::states":     {"value": {Value: `["CO","PA"]`}},
			startInputPrefix + "number::count":    {"value": {Value: "3"}},
			startInputPrefix + "string::optional": {"value": {Value: ""}},
			startMissionBlockID:                   {startMissionAction: {}},
		}},
	}}
	form, inputs, ok := startInputsFromSubmission(cb)
	if !ok || form.Mission != "rate_triage" {
		t.Fatalf("mission=%q ok=%v", form.Mission, ok)
	}
	if inputs["issue"] != "DEV-123" || inputs["count"] != float64(3) {
		t.Errorf("got %+v", inputs)
	}
	if states, ok := inputs["states"].([]any); !ok || len(states) != 2 {
		t.Errorf("list input must arrive as a list, got %#v", inputs["states"])
	}
	if _, set := inputs["optional"]; set {
		t.Error("a blank optional field must be left for the mission's default")
	}
}

func TestStartFieldErrorsPlacesErrorOnRejectedInput(t *testing.T) {
	cb := slack.InteractionCallback{View: slack.View{
		State: &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
			startInputPrefix + "string::issue": {"value": {Value: "DEV-1"}},
			startInputPrefix + "list::states":  {"value": {Value: "CO"}},
		}},
	}}
	errs := startFieldErrors(cb, errors.New("failed to start mission: input 'states': a list is required"))
	if _, ok := errs[startInputPrefix+"list::states"]; !ok || len(errs) != 1 {
		t.Errorf("the error must land on the states field only; got %v", errs)
	}
	if got := startFieldErrors(cb, errors.New("mission \"x\" is at max parallel capacity (1)")); got != nil {
		t.Errorf("an error about no input must not land on a field; got %v", got)
	}
}

func TestDMStartKeepsRequestInDM(t *testing.T) {
	type call struct{ method, channel, threadTS, metadata string }
	var mu sync.Mutex
	var calls []call
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{}
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&payload)
		} else {
			_ = r.ParseForm()
			for k, v := range r.PostForm {
				payload[k] = v[0]
			}
		}
		c := call{method: strings.TrimPrefix(r.URL.Path, "/")}
		c.channel, _ = payload["channel"].(string)
		c.threadTS, _ = payload["thread_ts"].(string)
		if view, ok := payload["view"].(map[string]any); ok {
			c.metadata, _ = view["private_metadata"].(string)
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		channel := c.channel
		if channel == "UTEST" {
			channel = "DTEST"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": channel, "ts": "1700000000.000001"})
	}))
	defer api.Close()

	missions := server.NewMCPServer("mock-squadron", "test", server.WithToolCapabilities(true))
	missions.AddTool(mcp.NewTool("list_missions"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"missions":[{"name":"rate_triage"}]}`), nil
	})
	missions.AddTool(mcp.NewTool("run_mission"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"missionId":"run-triage"}`), nil
	})
	missionAPI := httptest.NewServer(server.NewStreamableHTTPServer(missions))
	defer missionAPI.Close()

	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	fresh := func() *slackGateway {
		g := newSlackGateway()
		g.client = slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/"), slack.OptionHTTPClient(api.Client()))
		g.channelID = "CTEST"
		g.checkpointPath = checkpointPath
		g.mcp = &squadronMCP{url: missionAPI.URL + "/mcp"}
		return g
	}
	ctx := context.Background()
	g := fresh()

	g.onSlashCommand(ctx, slack.SlashCommand{ChannelID: "D0APPDM", UserID: "UTEST", TriggerID: "trigger"})
	if len(calls) != 1 || readStartForm(calls[0].metadata).Channel != "D0APPDM" {
		t.Fatalf("the form must carry the DM it was opened in; got %+v", calls)
	}
	g.onStartSubmitted(ctx, nil, slack.InteractionCallback{User: slack.User{ID: "UTEST"}, View: slack.View{
		CallbackID:      startCallbackID,
		PrivateMetadata: `{"mission":"rate_triage","channel":"D0APPDM"}`,
		State:           &slack.ViewState{Values: map[string]map[string]slack.BlockAction{}},
	}})
	if got := calls[1]; got.method != "chat.postMessage" || got.channel != "UTEST" {
		t.Fatalf("the request message must go to the requester's DM with the app; got %+v", got)
	}

	if err := g.postQuestion(ctx, gatewaysdk.HumanInputRecord{MissionID: "run-triage", ToolCallID: "q1", Question: "Which state?"}); err != nil {
		t.Fatal(err)
	}
	if got := calls[2]; got.channel != "DTEST" || got.threadTS != "1700000000.000001" {
		t.Errorf("a question must reply in the DM thread; got %+v", got)
	}
	if err := g.markResolved(ctx, gatewaysdk.HumanInputRecord{ToolCallID: "q1", Question: "Which state?", Response: "CO"}); err != nil {
		t.Fatal(err)
	}
	if got := calls[3]; got.method != "chat.update" || got.channel != "DTEST" {
		t.Errorf("an answered question must be edited in the DM; got %+v", got)
	}

	g = fresh()
	g.loadCheckpoint()
	if err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-triage", Payload: `{"text":"Status: done","root":true}`}); err != nil {
		t.Fatal(err)
	}
	if got := calls[4]; got.method != "chat.update" || got.channel != "DTEST" {
		t.Errorf("after a restart the top-level message must still be updated in the DM; got %+v", got)
	}
}

func TestChannelStartStaysInItsChannelAndLinksTheRun(t *testing.T) {
	type call struct{ method, channel, text string }
	var mu sync.Mutex
	var calls []call
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c := call{method: strings.TrimPrefix(r.URL.Path, "/"), channel: r.PostForm.Get("channel"), text: r.PostForm.Get("text")}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": c.channel, "ts": "1700000000.000002"})
	}))
	defer api.Close()

	missions := server.NewMCPServer("mock-squadron", "test", server.WithToolCapabilities(true))
	missions.AddTool(mcp.NewTool("run_mission"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`{"missionId":"run-triage"}`), nil
	})
	missionAPI := httptest.NewServer(server.NewStreamableHTTPServer(missions))
	defer missionAPI.Close()

	g := newSlackGateway()
	g.client = slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/"), slack.OptionHTTPClient(api.Client()))
	g.channelID = "CTEST"
	g.runURL = "https://cc.example/runs/{mission_id}"
	g.mcp = &squadronMCP{url: missionAPI.URL + "/mcp"}
	ctx := context.Background()

	g.onStartSubmitted(ctx, nil, slack.InteractionCallback{User: slack.User{ID: "UTEST"}, View: slack.View{
		CallbackID:      startCallbackID,
		PrivateMetadata: `{"mission":"rate_triage","channel":"CSECOND"}`,
		State:           &slack.ViewState{Values: map[string]map[string]slack.BlockAction{}},
	}})
	run := "*<https://cc.example/runs/run-triage|rate_triage>*"
	if got := calls[0]; got.channel != "CSECOND" || got.text != run+"\nRequester: <@UTEST>" {
		t.Fatalf("the request must post in the channel /squadron was run in, linked to the run; got %+v", got)
	}

	if err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: "run-triage", Payload: `{"text":"Fixing CO","root":true}`}); err != nil {
		t.Fatal(err)
	}
	if got := calls[1]; got.method != "chat.update" || got.channel != "CSECOND" || got.text != "Fixing CO\n"+run+"\nRequester: <@UTEST>" {
		t.Errorf("a root rewrite must stay in that channel and keep the run and requester lines; got %+v", got)
	}
}

func TestDesiredChannelOpensTheThreadOnlyWhenNoneExists(t *testing.T) {
	type call struct{ channel, threadTS string }
	var mu sync.Mutex
	var calls []call
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c := call{channel: r.PostForm.Get("channel"), threadTS: r.PostForm.Get("thread_ts")}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": c.channel, "ts": "1700000000.000003"})
	}))
	defer api.Close()

	g := newSlackGateway()
	g.client = slack.New("xoxb-test", slack.OptionAPIURL(api.URL+"/"), slack.OptionHTTPClient(api.Client()))
	g.channelID = "CTEST"
	ctx := context.Background()
	post := func(missionID, payload string) {
		t.Helper()
		if err := g.postMessage(ctx, gatewaysdk.PostMessageRequest{MissionID: missionID, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}

	post("run-1", `{"text":"Triaging","thread":"DEV-1","desired_channel":"CSECOND"}`)
	if got := calls[0]; got != (call{channel: "CSECOND"}) {
		t.Fatalf("a run with no thread must open it in desired_channel; got %+v", got)
	}
	post("run-1", `{"text":"Update"}`)
	if got := calls[1]; got != (call{channel: "CSECOND", threadTS: "1700000000.000003"}) {
		t.Errorf("later posts must follow the thread to its channel; got %+v", got)
	}
	post("run-2", `{"text":"Fixing","thread":"DEV-1","desired_channel":"COTHER"}`)
	if got := calls[2]; got != (call{channel: "CSECOND", threadTS: "1700000000.000003"}) {
		t.Errorf("an existing thread must win over desired_channel; got %+v", got)
	}
}
