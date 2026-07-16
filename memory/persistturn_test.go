// memory/persistturn_test.go
package memory

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// TestPersistTurn_MessagesDurableOnReturn pins the durability contract: when
// PersistTurn returns, the user and assistant rows are already visible to a
// reader — no Close(), no sleep. A fast follow-up Execute on the same thread
// must never race past the previous turn.
func TestPersistTurn_MessagesDurableOnReturn(t *testing.T) {
	store := newConformanceStore(t)
	m := &AgentMemory{}
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})

	task := core.AgentTask{ThreadID: "t1", Input: "hello"}
	steps := []core.StepTrace{{Name: "tool_x", Type: core.StepTypeTool, Output: "ok"}}
	m.PersistTurn(context.Background(), "agent", task, "hello", "world", steps)

	store.mu.Lock()
	msgs := append([]core.Message(nil), store.messages["t1"]...)
	store.mu.Unlock()

	if len(msgs) != 2 {
		t.Fatalf("got %d messages immediately after PersistTurn, want 2 (user+assistant)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("first row = %+v, want user/hello", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "world" {
		t.Fatalf("second row = %+v, want assistant/world", msgs[1])
	}
	if !strings.Contains(string(msgs[1].Metadata), `"steps"`) {
		t.Fatalf("assistant metadata missing steps blob: %s", msgs[1].Metadata)
	}
}

// TestPersistTurn_BackpressureKeepsMessages saturates the enrichment
// semaphore and verifies PersistTurn still (a) returns promptly — it must
// never block the agent loop waiting for a slot — and (b) persists the
// message rows. Only enrichment may be skipped under backpressure.
func TestPersistTurn_BackpressureKeepsMessages(t *testing.T) {
	store := newConformanceStore(t)
	m := &AgentMemory{}
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})

	m.initSem()
	for i := 0; i < maxIngestGoroutines; i++ {
		m.sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < maxIngestGoroutines; i++ {
			<-m.sem
		}
	}()

	task := core.AgentTask{ThreadID: "t2", Input: "hi"}
	done := make(chan struct{})
	go func() {
		m.PersistTurn(context.Background(), "agent", task, "hi", "yo", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PersistTurn blocked under backpressure; must skip enrichment, not wait")
	}

	store.mu.Lock()
	n := len(store.messages["t2"])
	store.mu.Unlock()
	if n != 2 {
		t.Fatalf("got %d messages under backpressure, want 2 — messages must never be dropped", n)
	}
}

// TestPersistTurn_SameSecondTurnsStayOrdered pins the fix for the `now + 1`
// timestamp hack: two turns persisted within the same wall second must come
// back from the store in persist order (user A, asst A, user B, asst B).
// Ordering rides on the UUIDv7 ID tiebreak; a fabricated asst=now+1 timestamp
// used to sort turn B's user row BEFORE turn A's assistant row.
func TestPersistTurn_SameSecondTurnsStayOrdered(t *testing.T) {
	store := newConformanceStore(t)
	m := &AgentMemory{}
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})

	task := core.AgentTask{ThreadID: "t-order"}
	// Two full turns back to back — with sync persist these land in the
	// same second virtually every run.
	m.PersistTurn(context.Background(), "agent", task, "q1", "a1", nil)
	m.PersistTurn(context.Background(), "agent", task, "q2", "a2", nil)

	store.mu.Lock()
	rows := append([]core.Message(nil), store.messages["t-order"]...)
	store.mu.Unlock()
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	// Re-sort exactly like the pg/sqlite readers do: created_at, then id.
	sorted := append([]core.Message(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt != sorted[j].CreatedAt {
			return sorted[i].CreatedAt < sorted[j].CreatedAt
		}
		return sorted[i].ID < sorted[j].ID
	})
	want := []string{"q1", "a1", "q2", "a2"}
	for i, w := range want {
		if sorted[i].Content != w {
			got := make([]string, len(sorted))
			for j, r := range sorted {
				got[j] = r.Content
			}
			t.Fatalf("reader order = %v, want %v", got, want)
		}
	}
}

// TestPersistTurn_CanceledContextStillPersists pins that a turn ending on an
// already-canceled context (user abort, upstream error) still lands in the
// thread store: PersistTurn detaches from the caller's cancellation.
func TestPersistTurn_CanceledContextStillPersists(t *testing.T) {
	store := newConformanceStore(t)
	m := &AgentMemory{}
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	task := core.AgentTask{ThreadID: "t3", Input: "in"}
	m.PersistTurn(ctx, "agent", task, "in", "out", nil)

	store.mu.Lock()
	n := len(store.messages["t3"])
	store.mu.Unlock()
	if n != 2 {
		t.Fatalf("got %d messages after canceled-context persist, want 2", n)
	}
}
