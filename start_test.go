package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/slack-go/slack"
)

func TestStartModalMarksRequiredAndSkipsProtected(t *testing.T) {
	m := startModal([]missionSummary{{Name: "rate_triage"}}, "rate_triage", []missionInput{
		{Name: "issue", Type: "string", Required: true},
		{Name: "repo_url", Type: "string", Default: json.RawMessage(`"https://github.com/FedTax/x"`)},
		{Name: "token", Type: "string", Protected: true},
	})
	if m.Submit == nil || m.PrivateMetadata != "rate_triage" {
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
	if m := startModal([]missionSummary{{Name: "a"}}, "", nil); m.Submit != nil {
		t.Error("the form must not submit before a mission is picked")
	}
}

func TestStartInputsFromSubmissionDecodesTypedValues(t *testing.T) {
	cb := slack.InteractionCallback{View: slack.View{
		CallbackID:      startCallbackID,
		PrivateMetadata: "rate_triage",
		State: &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
			startInputPrefix + "string::issue":    {"value": {Value: "DEV-123"}},
			startInputPrefix + "list::states":     {"value": {Value: `["CO","PA"]`}},
			startInputPrefix + "number::count":    {"value": {Value: "3"}},
			startInputPrefix + "string::optional": {"value": {Value: ""}},
			startMissionBlockID:                   {startMissionAction: {}},
		}},
	}}
	mission, inputs, ok := startInputsFromSubmission(cb)
	if !ok || mission != "rate_triage" {
		t.Fatalf("mission=%q ok=%v", mission, ok)
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
