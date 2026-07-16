// agent/persist_interrupted_test.go
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/store/sqlite"
)

// newPersistTestMem builds an AgentMemory over an in-memory sqlite store so
// tests can observe what terminateIteration persists.
func newPersistTestMem(t *testing.T) (*memory.AgentMemory, *sqlite.Store) {
	t.Helper()
	s := sqlite.New(":memory:")
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("sqlite init: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	m := &memory.AgentMemory{}
	m.Init(memory.AgentMemoryConfig{Store: s})
	return m, s
}

// TestTerminateIteration_PersistsErrorTurnWithSteps: a turn that ran tools
// and then died on an error must still reach the thread store — with the
// executed steps and a synthetic interruption marker — so the next turn on
// this thread knows about the side effects.
func TestTerminateIteration_PersistsErrorTurnWithSteps(t *testing.T) {
	m, s := newPersistTestMem(t)
	cfg := LoopConfig{Name: "test", Config: Config{Logger: nopLogger}, Mem: m}
	state := &loopState{steps: []core.StepTrace{{Name: "edit_file", Type: core.StepTypeTool, Output: "saved"}}}
	task := AgentTask{ThreadID: "th-err", Input: "build it"}

	terminateIteration(context.Background(), &cfg, task, nil, state, core.FinishError, AgentResult{}, errors.New("boom"))

	msgs, err := s.GetMessages(context.Background(), "th-err", 10)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages after error turn, want 2 (user+assistant)", len(msgs))
	}
	var asst *core.Message
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			asst = &msgs[i]
		}
	}
	if asst == nil {
		t.Fatal("no assistant row persisted for error turn")
	}
	if !strings.Contains(asst.Content, "[Turn interrupted by error: boom]") {
		t.Fatalf("assistant content = %q, want interruption marker", asst.Content)
	}
	if !strings.Contains(string(asst.Metadata), "edit_file") {
		t.Fatalf("assistant metadata missing executed steps: %s", asst.Metadata)
	}
}

// TestTerminateIteration_SkipsGhostTurn: an error before any output or tool
// execution (e.g. the LLM call itself failed) persists nothing, so callers
// that retry the same task don't accumulate ghost turns.
func TestTerminateIteration_SkipsGhostTurn(t *testing.T) {
	m, s := newPersistTestMem(t)
	cfg := LoopConfig{Name: "test", Config: Config{Logger: nopLogger}, Mem: m}
	state := &loopState{}
	task := AgentTask{ThreadID: "th-ghost", Input: "hi"}

	terminateIteration(context.Background(), &cfg, task, nil, state, core.FinishError, AgentResult{}, errors.New("llm 400"))

	msgs, err := s.GetMessages(context.Background(), "th-ghost", 10)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages for a no-work error turn, want 0", len(msgs))
	}
}

// TestTerminateIteration_SuspendedNotPersisted: suspended turns resume later
// and persist on their terminal exit; persisting at suspension would double
// up the turn.
func TestTerminateIteration_SuspendedNotPersisted(t *testing.T) {
	m, s := newPersistTestMem(t)
	cfg := LoopConfig{Name: "test", Config: Config{Logger: nopLogger}, Mem: m}
	state := &loopState{steps: []core.StepTrace{{Name: "ask_user", Type: core.StepTypeTool}}}
	task := AgentTask{ThreadID: "th-susp", Input: "hi"}

	terminateIteration(context.Background(), &cfg, task, nil, state, core.FinishSuspended, AgentResult{}, nil)

	msgs, err := s.GetMessages(context.Background(), "th-susp", 10)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("got %d messages for a suspended turn, want 0", len(msgs))
	}
}

// TestTerminateIteration_HaltedTurnPersistsOutput: a processor halt that
// carries a user-facing output is a legitimate end of turn and must persist
// like a natural stop.
func TestTerminateIteration_HaltedTurnPersistsOutput(t *testing.T) {
	m, s := newPersistTestMem(t)
	cfg := LoopConfig{Name: "test", Config: Config{Logger: nopLogger}, Mem: m}
	state := &loopState{}
	task := AgentTask{ThreadID: "th-halt", Input: "hi"}

	terminateIteration(context.Background(), &cfg, task, nil, state, core.FinishHalted, AgentResult{Output: "guard says no"}, nil)

	msgs, err := s.GetMessages(context.Background(), "th-halt", 10)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages after halted turn, want 2", len(msgs))
	}
}
