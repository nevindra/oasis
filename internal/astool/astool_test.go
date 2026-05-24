package astool_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/astool"
)

type fakeAgent struct{ called string }

func (f *fakeAgent) Name() string        { return "summarize" }
func (f *fakeAgent) Description() string { return "Summarizes text" }
func (f *fakeAgent) Execute(_ context.Context, task core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	f.called = task.Input
	return core.AgentResult{Output: "summary of: " + task.Input}, nil
}

func TestWrap_NameAndDescription(t *testing.T) {
	f := &fakeAgent{}
	tool := astool.Wrap(f)
	if got, want := tool.Name(), "agent_summarize"; got != want {
		t.Fatalf("Name(): got %q want %q", got, want)
	}
	def := tool.Definition()
	if got, want := def.Description, "Summarizes text"; got != want {
		t.Fatalf("Description(): got %q want %q", got, want)
	}
}

func TestWrap_ExecuteForwardsTaskToChild(t *testing.T) {
	f := &fakeAgent{}
	tool := astool.Wrap(f)
	args, _ := json.Marshal(map[string]string{"task": "hello world"})
	res, err := tool.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if f.called != "hello world" {
		t.Fatalf("child not called with task: got %q", f.called)
	}
	want := []byte(`"summary of: hello world"`)
	if string(res.Content) != string(want) {
		t.Fatalf("ExecuteRaw content: got %q want %q", res.Content, want)
	}
}

func TestUnwrap_RoundTrips(t *testing.T) {
	f := &fakeAgent{}
	tool := astool.Wrap(f)
	got, ok := astool.Unwrap(tool)
	if !ok || got != core.Agent(f) {
		t.Fatalf("Unwrap should return the original agent: got %v ok=%v", got, ok)
	}
}
