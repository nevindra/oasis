package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// selfCloneProvider drives a parent that fans out N spawn_subagent calls in
// one message, and clones that answer their subtask directly. Shared by the
// parent and its clones, so it must be safe for concurrent calls.
type selfCloneProvider struct {
	mu sync.Mutex
	// cloneSawSpawnTool records whether any clone's request advertised the
	// spawn_subagent tool (it must not — no recursive cloning).
	cloneSawSpawnTool bool
	fanout            int
	parentCalls       int
}

func (p *selfCloneProvider) Name() string { return "self-clone-mock" }

func (p *selfCloneProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	if ch != nil {
		defer close(ch)
	}
	last := req.Messages[len(req.Messages)-1]
	if strings.HasPrefix(last.Content, "PART:") {
		// A clone's run: check its advertised tools, answer immediately.
		p.mu.Lock()
		for _, t := range req.Tools {
			if t.Name == core.ToolSelfClone || t.Name == core.ToolTask {
				p.cloneSawSpawnTool = true
			}
		}
		p.mu.Unlock()
		return core.ChatResponse{Content: "done " + strings.TrimPrefix(last.Content, "PART:")}, nil
	}

	p.mu.Lock()
	p.parentCalls++
	call := p.parentCalls
	p.mu.Unlock()
	if call == 1 {
		var tcs []core.ToolCall
		for i := 0; i < p.fanout; i++ {
			args, _ := json.Marshal(map[string]string{"task": "PART:" + string(rune('A'+i))})
			tcs = append(tcs, core.ToolCall{ID: string(rune('1' + i)), Name: core.ToolSelfClone, Args: args})
		}
		return core.ChatResponse{ToolCalls: tcs}, nil
	}
	return core.ChatResponse{Content: "merged"}, nil
}

// TestSelfCloneParallelFanout: two spawn_subagent calls in one message run
// two clones; their reports come back as tool results and the clones never
// see the spawn tool themselves (no recursion).
func TestSelfCloneParallelFanout(t *testing.T) {
	p := &selfCloneProvider{fanout: 2}
	a := New("worker", "self-cloning worker", p, WithSelfClone(4, time.Minute))

	ch := make(chan core.StreamEvent, 256)
	var starts, finishes []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			switch ev.Type {
			case core.EventAgentStart:
				starts = append(starts, ev.Name)
			case core.EventAgentFinish:
				finishes = append(finishes, ev.Name)
				if ev.IsError {
					t.Errorf("clone %s finished with error: %s", ev.Name, ev.Content)
				}
			}
		}
	}()

	result, err := a.Execute(context.Background(), AgentTask{Input: "split this work"}, core.WithStream(ch))
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if result.Output != "merged" {
		t.Errorf("output = %q, want merged", result.Output)
	}
	if len(starts) != 2 || len(finishes) != 2 {
		t.Fatalf("starts=%v finishes=%v, want 2 each", starts, finishes)
	}
	for _, name := range starts {
		if !strings.HasPrefix(name, "worker-") {
			t.Errorf("clone name %q, want worker-N", name)
		}
	}
	if p.cloneSawSpawnTool {
		t.Error("a clone was offered spawn_subagent — recursion must be disabled")
	}
}

// TestSelfCloneBudget: with max 1, the second spawn in the same wave gets a
// budget-exhausted error result while the run still completes.
func TestSelfCloneBudget(t *testing.T) {
	p := &selfCloneProvider{fanout: 2}
	a := New("worker", "self-cloning worker", p, WithSelfClone(1, time.Minute))

	ch := make(chan core.StreamEvent, 256)
	var errResults []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			if ev.Type == core.EventToolCallResult && strings.Contains(ev.Content, "budget exhausted") {
				errResults = append(errResults, ev.Content)
			}
		}
	}()

	result, err := a.Execute(context.Background(), AgentTask{Input: "split"}, core.WithStream(ch))
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if result.Output != "merged" {
		t.Errorf("output = %q, want merged", result.Output)
	}
	if len(errResults) != 1 {
		t.Errorf("budget error results = %d, want exactly 1", len(errResults))
	}
}

// TestSelfCloneDisabledByDefault: without WithSelfClone the tool is neither
// advertised nor dispatchable.
func TestSelfCloneDisabledByDefault(t *testing.T) {
	sawTool := false
	p := &callbackProvider{
		name:     "plain",
		response: core.ChatResponse{Content: "ok"},
		onChat: func(req core.ChatRequest) {
			for _, tl := range req.Tools {
				if tl.Name == core.ToolSelfClone {
					sawTool = true
				}
			}
		},
	}
	a := New("plain", "no cloning", p)
	if _, err := a.Execute(context.Background(), AgentTask{Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	if sawTool {
		t.Error("spawn_subagent advertised without WithSelfClone")
	}
}
