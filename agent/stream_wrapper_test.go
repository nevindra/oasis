package agent

import (
	"context"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// emitterAgent is a test StreamingAgent that emits a fixed event sequence
// then returns a known AgentResult.
type emitterAgent struct {
	events []core.StreamEvent
	final  AgentResult
}

func (e *emitterAgent) Name() string        { return "emitter" }
func (e *emitterAgent) Description() string { return "" }
func (e *emitterAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return e.final, nil
}
func (e *emitterAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent) (AgentResult, error) {
	defer close(ch)
	for _, ev := range e.events {
		select {
		case ch <- ev:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	return e.final, nil
}

func TestStartStream_BlockingResult(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "hello "},
			{Type: core.EventTextDelta, Content: "world"},
		},
		final: AgentResult{Output: "hello world"},
	}
	s := StartStream(context.Background(), ag, AgentTask{Input: "hi"})
	res, err := s.Result()
	if err != nil {
		t.Fatalf("Result() err = %v", err)
	}
	if res.Output != "hello world" {
		t.Errorf("Result().Output = %q, want %q", res.Output, "hello world")
	}
	if got, want := s.Text(), "hello world"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestStartStream_DoneChannel(t *testing.T) {
	ag := &emitterAgent{final: AgentResult{Output: "ok"}}
	s := StartStream(context.Background(), ag, AgentTask{})
	select {
	case <-s.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("Done() never closed within 1s")
	}
}
