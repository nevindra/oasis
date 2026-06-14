// memory/retrieve_test.go
package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestBuildMessages_Minimal(t *testing.T) {
	store := newConformanceStore(t)
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})
	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "hello"}
	msgs := m.BuildMessages(context.Background(), "agent", "you are helpful", task)
	if len(msgs) < 2 {
		t.Fatalf("expected system + user, got %d", len(msgs))
	}
	if msgs[len(msgs)-1].Role != core.RoleUser {
		t.Fatal("user msg should be last")
	}
}

func TestBuildMessages_BatchedRecallIncludesFacts(t *testing.T) {
	store := newConformanceStore(t)
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "f1", Kind: KindFact, Content: "User likes dark mode",
		Scope: Scoped(ScopeResource, "c1"), Embedding: []float32{1, 0, 0},
	}))
	emb := &fakeEmbedder{out: [][]float32{{1, 0, 0}}}
	var m AgentMemory
	m.Init(AgentMemoryConfig{
		Store: store, Embedding: emb,
		RecallKinds: []core.MemoryKind{KindFact}, RecallTopK: 5,
		Logger: discardLogger(),
	})
	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "what color"}
	msgs := m.BuildMessages(context.Background(), "agent", "", task)

	// RAG content must NOT be in the system message.
	for _, msg := range msgs {
		if msg.Role == core.RoleSystem && strings.Contains(msg.Content, "dark mode") {
			t.Fatal("RAG content must not appear in system message")
		}
	}

	// RAG content MUST appear somewhere in the assembled messages.
	combined := ""
	for _, mm := range msgs {
		combined += "\n" + mm.Content
	}
	if !strings.Contains(combined, "dark mode") {
		t.Fatalf("recall not injected:\n%s", combined)
	}
}

// TestBuildMessages_RAGInContextBlock asserts that when processors return
// PromptParts, they land in a separate user message wrapped in <context> tags,
// positioned after history and before the current user input.
func TestBuildMessages_RAGInContextBlock(t *testing.T) {
	store := newConformanceStore(t)
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "f1", Kind: KindFact, Content: "User likes dark mode",
		Scope: Scoped(ScopeResource, "c1"), Embedding: []float32{1, 0, 0},
	}))
	emb := &fakeEmbedder{out: [][]float32{{1, 0, 0}}}
	var m AgentMemory
	m.Init(AgentMemoryConfig{
		Store: store, Embedding: emb,
		RecallKinds: []core.MemoryKind{KindFact}, RecallTopK: 5,
		Logger: discardLogger(),
	})
	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "what color"}
	msgs := m.BuildMessages(context.Background(), "agent", "you are helpful", task)

	// Find the context block message.
	var ctxMsg *core.ChatMessage
	for i := range msgs {
		if strings.HasPrefix(msgs[i].Content, "<context>") {
			ctxMsg = &msgs[i]
		}
	}
	if ctxMsg == nil {
		t.Fatalf("no <context> block found in messages; got: %v", msgs)
	}

	// Must be a user-role message.
	if ctxMsg.Role != core.RoleUser {
		t.Errorf("context block role = %q, want %q", ctxMsg.Role, core.RoleUser)
	}

	// Must contain the recalled fact.
	if !strings.Contains(ctxMsg.Content, "dark mode") {
		t.Errorf("context block does not contain recalled fact: %s", ctxMsg.Content)
	}

	// Must be properly closed.
	if !strings.HasSuffix(strings.TrimRight(ctxMsg.Content, "\n"), "</context>") {
		t.Errorf("context block not closed: %s", ctxMsg.Content)
	}

	// Context block must come before the final user input.
	lastIdx := len(msgs) - 1
	ctxIdx := -1
	for i := range msgs {
		if &msgs[i] == ctxMsg {
			ctxIdx = i
		}
	}
	if ctxIdx >= lastIdx {
		t.Errorf("context block (index %d) is not before user input (index %d)", ctxIdx, lastIdx)
	}

	// Final message must be the user's actual input, not the context block.
	if msgs[lastIdx].Content != "what color" {
		t.Errorf("last message content = %q, want %q", msgs[lastIdx].Content, "what color")
	}
}

// TestBuildMessages_NoRAG asserts that when no processors produce PromptParts,
// no extra message is injected between history and user input.
func TestBuildMessages_NoRAG(t *testing.T) {
	// Use a store with no memory items and no embedder — no PromptParts produced.
	store := newConformanceStore(t)
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})

	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "hello"}
	msgs := m.BuildMessages(context.Background(), "agent", "you are helpful", task)

	// Expected: [system, user-input]. No context block.
	for _, msg := range msgs {
		if strings.HasPrefix(msg.Content, "<context>") {
			t.Errorf("unexpected <context> block when PromptParts empty: %s", msg.Content)
		}
	}

	// Exactly system + user (no history loaded for empty store).
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages (system + user), got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != core.RoleSystem {
		t.Errorf("msgs[0].Role = %q, want system", msgs[0].Role)
	}
	if msgs[1].Role != core.RoleUser {
		t.Errorf("msgs[1].Role = %q, want user", msgs[1].Role)
	}
}

// TestBuildMessages_SystemStableAcrossCalls verifies the cache-hit property:
// when processors return different PromptParts on consecutive calls,
// messages[0] (the system message) is byte-identical between the two calls.
func TestBuildMessages_SystemStableAcrossCalls(t *testing.T) {
	store := newConformanceStore(t)

	// Plant two facts with different embeddings so different inputs recall
	// different content — simulating variation per turn.
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "f1", Kind: KindFact, Content: "User likes dark mode",
		Scope: Scoped(ScopeResource, "c1"), Embedding: []float32{1, 0, 0},
	}))
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "f2", Kind: KindFact, Content: "User prefers Go over Python",
		Scope: Scoped(ScopeResource, "c1"), Embedding: []float32{0, 1, 0},
	}))

	// Call 1: embedding points toward fact 1.
	emb1 := &fakeEmbedder{out: [][]float32{{1, 0, 0}}}
	var m1 AgentMemory
	m1.Init(AgentMemoryConfig{
		Store: store, Embedding: emb1,
		RecallKinds: []core.MemoryKind{KindFact}, RecallTopK: 1,
		Logger: discardLogger(),
	})
	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "colors?"}
	msgs1 := m1.BuildMessages(context.Background(), "agent", "you are helpful", task)

	// Call 2: embedding points toward fact 2 (different recall).
	emb2 := &fakeEmbedder{out: [][]float32{{0, 1, 0}}}
	var m2 AgentMemory
	m2.Init(AgentMemoryConfig{
		Store: store, Embedding: emb2,
		RecallKinds: []core.MemoryKind{KindFact}, RecallTopK: 1,
		Logger: discardLogger(),
	})
	msgs2 := m2.BuildMessages(context.Background(), "agent", "you are helpful", task)

	if len(msgs1) == 0 || len(msgs2) == 0 {
		t.Fatal("both calls must produce messages")
	}

	// Verify the two calls produced different RAG content (otherwise the test is trivial).
	combined1, combined2 := "", ""
	for _, mm := range msgs1 {
		combined1 += mm.Content
	}
	for _, mm := range msgs2 {
		combined2 += mm.Content
	}
	if combined1 == combined2 {
		t.Skip("store did not return different recall results; cannot verify stability test")
	}

	// The critical assertion: system messages are byte-identical.
	sys1 := msgs1[0]
	sys2 := msgs2[0]
	if sys1.Role != core.RoleSystem || sys2.Role != core.RoleSystem {
		t.Fatalf("msgs[0] not system: call1=%q call2=%q", sys1.Role, sys2.Role)
	}
	if sys1.Content != sys2.Content {
		t.Errorf("system message differs between calls (cache miss):\ncall1: %q\ncall2: %q", sys1.Content, sys2.Content)
	}
}
