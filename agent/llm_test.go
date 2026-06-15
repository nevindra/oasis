package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

type askUserTestHandler struct {
	resp InputResponse
	got  InputRequest
}

func (f *askUserTestHandler) RequestInput(_ context.Context, req InputRequest) (InputResponse, error) {
	f.got = req
	return f.resp, nil
}

func TestExecuteAskUserMultiSelect(t *testing.T) {
	h := &askUserTestHandler{resp: InputResponse{Values: []string{"red", "blue"}}}
	args, _ := json.Marshal(askUserArgs{Question: "pick colors", Options: []string{"red", "green", "blue"}, MultiSelect: true})
	tc := core.ToolCall{Name: core.ToolAskUser, Args: args}

	out, err := executeAskUser(context.Background(), h, "agentX", tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !h.got.MultiSelect {
		t.Error("expected MultiSelect to be forwarded to handler")
	}
	var got []string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result not a JSON array: %v (%q)", err, out)
	}
	if len(got) != 2 || got[0] != "red" || got[1] != "blue" {
		t.Errorf("got %v, want [red blue]", got)
	}
}

func TestExecuteAskUserSingleUnchanged(t *testing.T) {
	h := &askUserTestHandler{resp: InputResponse{Value: "yes"}}
	args, _ := json.Marshal(askUserArgs{Question: "ok?"})
	tc := core.ToolCall{Name: core.ToolAskUser, Args: args}

	out, err := executeAskUser(context.Background(), h, "agentX", tc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "yes" {
		t.Errorf("got %q, want \"yes\"", out)
	}
}
