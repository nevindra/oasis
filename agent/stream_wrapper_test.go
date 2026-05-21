package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// emitterAgent is a test StreamingAgent that emits a fixed event sequence
// then returns a known AgentResult.
type emitterAgent struct {
	events []core.StreamEvent
	final  AgentResult
	delay  time.Duration // optional delay before emitting first event
}

func (e *emitterAgent) Name() string        { return "emitter" }
func (e *emitterAgent) Description() string { return "" }
func (e *emitterAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return e.final, nil
}
func (e *emitterAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent) (AgentResult, error) {
	defer close(ch)
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
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

func TestStream_OnTextDelta(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "x"},
			{Type: core.EventTextDelta, Content: "y"},
			{Type: core.EventToolCallStart, Name: "ignored"},
		},
		final:  AgentResult{Output: "xy"},
		delay:  10 * time.Millisecond, // Ensure callback is registered before events start
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var got []string
	s.OnTextDelta(func(chunk string) { got = append(got, chunk) })

	_, _ = s.Result()
	if !equalStrings(got, []string{"x", "y"}) {
		t.Errorf("OnTextDelta callback got %v, want [x y]", got)
	}
}

func TestStream_OnReasoningDelta(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventReasoningDelta, Content: "think1"},
			{Type: core.EventReasoningDelta, Content: "think2"},
			{Type: core.EventTextDelta, Content: "ignored"},
		},
		final:  AgentResult{Thinking: "think1think2"},
		delay:  10 * time.Millisecond,
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var got []string
	s.OnReasoningDelta(func(chunk string) { got = append(got, chunk) })

	_, _ = s.Result()
	if !equalStrings(got, []string{"think1", "think2"}) {
		t.Errorf("OnReasoningDelta callback got %v, want [think1 think2]", got)
	}
}

func TestStream_OnToolCall(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventToolCallStart, ID: "1", Name: "search"},
		},
		final:  AgentResult{},
		delay:  10 * time.Millisecond,
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var seen []string
	s.OnToolCall(func(tc core.ToolCall) { seen = append(seen, tc.Name) })

	_, _ = s.Result()
	if !equalStrings(seen, []string{"search"}) {
		t.Errorf("OnToolCall got %v, want [search]", seen)
	}
}

func TestStream_OnToolResult(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventToolCallResult, Content: "result1"},
			{Type: core.EventToolCallResult, Content: "result2"},
		},
		final:  AgentResult{},
		delay:  10 * time.Millisecond,
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var got []string
	s.OnToolResult(func(tr core.ToolResult) { got = append(got, string(tr.Content)) })

	_, _ = s.Result()
	if !equalStrings(got, []string{"result1", "result2"}) {
		t.Errorf("OnToolResult got %v, want [result1 result2]", got)
	}
}

func TestStream_OnEvent(t *testing.T) {
	ag := &emitterAgent{
		events: []core.StreamEvent{
			{Type: core.EventTextDelta, Content: "text1"},
			{Type: core.EventToolCallStart, Name: "tool1"},
			{Type: core.EventTextDelta, Content: "text2"},
		},
		final:  AgentResult{},
		delay:  10 * time.Millisecond,
	}
	s := StartStream(context.Background(), ag, AgentTask{})

	var got []core.StreamEventType
	s.OnEvent(func(ev core.StreamEvent) { got = append(got, ev.Type) })

	_, _ = s.Result()
	want := []core.StreamEventType{core.EventTextDelta, core.EventToolCallStart, core.EventTextDelta}
	if len(got) != len(want) {
		t.Errorf("OnEvent got %d events, want %d", len(got), len(want))
	} else {
		for i, g := range got {
			if g != want[i] {
				t.Errorf("OnEvent event %d: got %v, want %v", i, g, want[i])
			}
		}
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

func TestStreamBlockingAccessors(t *testing.T) {
	// Use emitterAgent to set AgentResult fields with FinishReason, Warnings, etc.
	ag := &emitterAgent{
		events: []core.StreamEvent{},
		final: AgentResult{
			Output:       "done",
			FinishReason: core.FinishStop,
			Warnings:     []string{"x"},
			ProviderMeta: json.RawMessage(`{"a":1}`),
			Sources:      nil,
			Files:        nil,
			SuspendPayload: nil,
			Iterations:   []core.IterationTrace{
				{Iter: 0, Model: "test", Usage: core.Usage{}},
			},
		},
	}
	s := StartStream(context.Background(), ag, AgentTask{Input: "x"})

	if s.FinishReason() != core.FinishStop {
		t.Errorf("FinishReason = %q", s.FinishReason())
	}
	if len(s.Warnings()) != 1 || s.Warnings()[0] != "x" {
		t.Errorf("Warnings = %v", s.Warnings())
	}
	if string(s.ProviderMeta()) != `{"a":1}` {
		t.Errorf("ProviderMeta = %s", s.ProviderMeta())
	}
	// Sources/Files default to nil for this trivial run.
	if s.Sources() != nil {
		t.Errorf("Sources should be nil, got %v", s.Sources())
	}
	if s.Files() != nil {
		t.Errorf("Files should be nil, got %v", s.Files())
	}
	if s.SuspendPayload() != nil {
		t.Errorf("SuspendPayload should be nil, got %s", s.SuspendPayload())
	}
	if len(s.Iterations()) != 1 {
		t.Errorf("Iterations len = %d", len(s.Iterations()))
	}
}
