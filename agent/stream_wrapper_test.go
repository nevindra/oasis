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

func TestStream_Events_FanOut(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "a"},
			{Type: core.EventTextDelta, Content: "b"},
			{Type: core.EventTextDelta, Content: "c"},
		},
		final: AgentResult{Output: "abc"},
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	// Two parallel readers must both see all three events.
	collect := func(ch <-chan core.StreamEvent) []string {
		var out []string
		for ev := range ch {
			if ev.Type == core.EventTextDelta {
				out = append(out, ev.Content)
			}
		}
		return out
	}

	ch1 := s.Events()
	ch2 := s.Events()
	done1 := make(chan []string, 1)
	done2 := make(chan []string, 1)
	go func() { done1 <- collect(ch1) }()
	go func() { done2 <- collect(ch2) }()

	got1 := <-done1
	got2 := <-done2
	want := []string{"a", "b", "c"}

	if !equalStrings(got1, want) {
		t.Errorf("ch1 = %v, want %v", got1, want)
	}
	if !equalStrings(got2, want) {
		t.Errorf("ch2 = %v, want %v", got2, want)
	}
}

func TestStream_Events_LateReplay(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "early"},
		},
		final: AgentResult{Output: "early"},
	}
	s := StartStream(context.Background(), ag, AgentTask{})
	// Wait for the agent to finish before subscribing — late subscriber.
	<-s.Done()

	ch := s.Events()
	var got []string
	for ev := range ch {
		if ev.Type == core.EventTextDelta {
			got = append(got, ev.Content)
		}
	}
	if !equalStrings(got, []string{"early"}) {
		t.Errorf("late subscriber got %v, want [early]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
