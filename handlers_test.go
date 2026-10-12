package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	gatewaysdk "github.com/mlund01/squadron-gateway-sdk"
	"github.com/slack-go/slack"
)

func TestSlackResponderID(t *testing.T) {
	cases := []struct {
		name string
		in   slack.InteractionCallback
		want string
	}{
		{
			name: "uses User.Name when present",
			in: slack.InteractionCallback{User: slack.User{
				ID:   "U123",
				Name: "alice",
			}},
			want: "slack:alice",
		},
		{
			name: "falls back to User.ID when name is empty",
			in:   slack.InteractionCallback{User: slack.User{ID: "U123"}},
			want: "slack:U123",
		},
		{
			name: "slack:unknown when neither is set",
			in:   slack.InteractionCallback{},
			want: "slack:unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slackResponderID(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecodeInteractionResponseRoutesByActionID(t *testing.T) {
	cases := []struct {
		name     string
		action   *slack.BlockAction
		wantTC   string
		wantResp string
		wantOK   bool
	}{
		{
			name: "button click → choice from action_id",
			action: &slack.BlockAction{
				ActionID: encodeCustomID("tc-1", "Option A"),
			},
			wantTC:   "tc-1",
			wantResp: "Option A",
			wantOK:   true,
		},
		{
			name: "multi-select submission → JSON-encoded option values",
			action: &slack.BlockAction{
				ActionID: encodeSelectMenuCustomID("tc-1"),
				SelectedOptions: []slack.OptionBlockObject{
					{Value: "A"},
					{Value: "C"},
				},
			},
			wantTC:   "tc-1",
			wantResp: `["A","C"]`,
			wantOK:   true,
		},
		{
			name: "foreign action_id → ok=false (component we didn't render)",
			action: &slack.BlockAction{
				ActionID: "not-our-prefix",
			},
			wantOK: false,
		},
		{
			name:   "nil action → ok=false",
			action: nil,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tcID, resp, ok := decodeInteractionResponse(tc.action)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if tcID != tc.wantTC {
				t.Errorf("toolCallID: got %q, want %q", tcID, tc.wantTC)
			}
			if resp != tc.wantResp {
				t.Errorf("response: got %q, want %q", resp, tc.wantResp)
			}
		})
	}
}

// fakeSquadronAPI exercises the resolve flow in-process; records
// each call so tests can inspect what was sent upstream.
type fakeSquadronAPI struct {
	mu            sync.Mutex
	resolveCalls  []resolveCall
	listCalls     atomic.Int32
	resolveResult gatewaysdk.ResolveResult
	resolveErr    error
	listResult    []gatewaysdk.HumanInputRecord
	listTotal     int
	listErr       error
}

type resolveCall struct {
	ToolCallID, Response, Responder string
}

func (f *fakeSquadronAPI) ListHumanInputs(_ context.Context, _ gatewaysdk.HumanInputFilter) ([]gatewaysdk.HumanInputRecord, int, error) {
	f.listCalls.Add(1)
	return f.listResult, f.listTotal, f.listErr
}

func (f *fakeSquadronAPI) ResolveHumanInput(_ context.Context, toolCallID, response, responder string) (gatewaysdk.ResolveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls = append(f.resolveCalls, resolveCall{toolCallID, response, responder})
	return f.resolveResult, f.resolveErr
}

func (f *fakeSquadronAPI) snapshotResolveCalls() []resolveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resolveCall(nil), f.resolveCalls...)
}

func TestFakeAPIWiring(t *testing.T) {
	api := &fakeSquadronAPI{
		resolveResult: gatewaysdk.ResolveResult{
			Record: gatewaysdk.HumanInputRecord{ToolCallID: "tc-1", Response: "yes"},
		},
	}
	g := newSlackGateway()
	g.api = api

	res, err := g.api.ResolveHumanInput(context.Background(), "tc-1", "yes", "slack:alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Response != "yes" {
		t.Errorf("response mismatch: got %q", res.Record.Response)
	}
	if calls := api.snapshotResolveCalls(); len(calls) != 1 || calls[0].Responder != "slack:alice" {
		t.Errorf("expected 1 resolve call with responder=slack:alice, got %+v", calls)
	}
}

func TestFakeAPIErrorPath(t *testing.T) {
	api := &fakeSquadronAPI{resolveErr: errors.New("rpc closed")}
	g := newSlackGateway()
	g.api = api

	if _, err := g.api.ResolveHumanInput(context.Background(), "tc", "x", "u"); err == nil {
		t.Error("expected error from fake API")
	}
}

func TestParseStartCommand(t *testing.T) {
	mission, inputs, err := parseStartCommand("<@U0BOT> start rate_triage issue=DEV-123 note=a&amp;b")
	if err != nil {
		t.Fatal(err)
	}
	if mission != "rate_triage" {
		t.Errorf("mission: got %q", mission)
	}
	if inputs["issue"] != "DEV-123" || inputs["note"] != "a&b" {
		t.Errorf("inputs: got %+v", inputs)
	}

	for _, text := range []string{
		"<@U0BOT> hello",
		"<@U0BOT> start",
		"<@U0BOT> start m issue",
		"<@U0BOT> start m slack_thread=1",
	} {
		if _, _, err := parseStartCommand(text); err == nil {
			t.Errorf("%q: expected an error", text)
		}
	}
}

func TestDecodeInteractionResponseIgnoresAnswerButton(t *testing.T) {
	if _, _, ok := decodeInteractionResponse(&slack.BlockAction{ActionID: encodeAnswerCustomID("tc-1")}); ok {
		t.Error("the Answer button opens a modal; it must not resolve the question")
	}
}

func TestAnswerFromSubmission(t *testing.T) {
	cb := slack.InteractionCallback{View: slack.View{
		CallbackID:      answerCallbackID,
		PrivateMetadata: "tc-1",
		State: &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
			answerBlockID: {answerActionID: {Value: "  use staging  "}},
		}},
	}}
	tc, answer, ok := answerFromSubmission(cb)
	if !ok || tc != "tc-1" || answer != "use staging" {
		t.Errorf("got tc=%q answer=%q ok=%v", tc, answer, ok)
	}

	cb.View.CallbackID = "someone-else"
	if _, _, ok := answerFromSubmission(cb); ok {
		t.Error("a submission from another modal must be ignored")
	}
}

func TestAnswerModalCarriesToolCallID(t *testing.T) {
	m := answerModal("tc-1")
	if m.PrivateMetadata != "tc-1" || m.CallbackID != answerCallbackID {
		t.Errorf("got metadata=%q callback=%q", m.PrivateMetadata, m.CallbackID)
	}
}
