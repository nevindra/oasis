package agent

import (
	"testing"

	"github.com/nevindra/oasis/core"
)

// TestApplyPromptCacheMarkers_DefaultMarksSystemAndTail verifies the default
// (non-disabled) path marks messages[0] and the last message as cache
// breakpoints.
func TestApplyPromptCacheMarkers_DefaultMarksSystemAndTail(t *testing.T) {
	msgs := []core.ChatMessage{
		{Role: core.RoleSystem, Content: "you are helpful"},
		{Role: core.RoleUser, Content: "hello"},
		{Role: core.RoleAssistant, Content: "hi"},
		{Role: core.RoleUser, Content: "what's the weather?"},
	}

	applyPromptCacheMarkers(msgs, false)

	if !msgs[0].CacheCheckpoint {
		t.Error("msgs[0] (system) should be marked")
	}
	if msgs[1].CacheCheckpoint || msgs[2].CacheCheckpoint {
		t.Error("intermediate messages should not be marked")
	}
	if !msgs[3].CacheCheckpoint {
		t.Error("msgs[last] should be marked")
	}
}

// TestApplyPromptCacheMarkers_DisabledClearsAll verifies the disabled path
// clears every CacheCheckpoint, including markers set by a previous iteration
// or by user code.
func TestApplyPromptCacheMarkers_DisabledClearsAll(t *testing.T) {
	msgs := []core.ChatMessage{
		{Role: core.RoleSystem, Content: "sys", CacheCheckpoint: true},
		{Role: core.RoleUser, Content: "u", CacheCheckpoint: true},
	}

	applyPromptCacheMarkers(msgs, true)

	for i := range msgs {
		if msgs[i].CacheCheckpoint {
			t.Errorf("msgs[%d].CacheCheckpoint should be false when disabled", i)
		}
	}
}

// TestApplyPromptCacheMarkers_AdvancesAcrossIterations verifies that calling
// the helper repeatedly (simulating iterations) resets stale markers and
// re-marks the new tail. Without the reset, every previous tail would stay
// marked and we'd accumulate past Anthropic's 4-breakpoint limit.
func TestApplyPromptCacheMarkers_AdvancesAcrossIterations(t *testing.T) {
	// Iteration 1.
	msgs := []core.ChatMessage{
		{Role: core.RoleSystem, Content: "sys"},
		{Role: core.RoleUser, Content: "u1"},
	}
	applyPromptCacheMarkers(msgs, false)
	if !msgs[0].CacheCheckpoint || !msgs[1].CacheCheckpoint {
		t.Fatal("iter 1: system and tail should be marked")
	}

	// Iteration 2: append assistant + tool result.
	msgs = append(msgs,
		core.ChatMessage{Role: core.RoleAssistant, Content: "asst"},
		core.ChatMessage{Role: "tool", Content: "tool_result"},
	)
	applyPromptCacheMarkers(msgs, false)

	if !msgs[0].CacheCheckpoint {
		t.Error("iter 2: system should still be marked")
	}
	if msgs[1].CacheCheckpoint {
		t.Error("iter 2: previous tail msgs[1] should NOT be marked (was tail in iter 1)")
	}
	if msgs[2].CacheCheckpoint {
		t.Error("iter 2: msgs[2] is mid-list, should NOT be marked")
	}
	if !msgs[3].CacheCheckpoint {
		t.Error("iter 2: new tail msgs[3] should be marked")
	}
}

// TestApplyPromptCacheMarkers_SingleMessage verifies the helper handles a
// degenerate slice of length 1 — system-only — by marking only [0] (not also
// trying to mark "tail" which would be the same index).
func TestApplyPromptCacheMarkers_SingleMessage(t *testing.T) {
	msgs := []core.ChatMessage{
		{Role: core.RoleSystem, Content: "sys"},
	}
	applyPromptCacheMarkers(msgs, false)
	if !msgs[0].CacheCheckpoint {
		t.Error("single-message slice: msgs[0] should be marked")
	}
}

// TestApplyPromptCacheMarkers_EmptySlice verifies no panic on len==0.
func TestApplyPromptCacheMarkers_EmptySlice(t *testing.T) {
	var msgs []core.ChatMessage
	applyPromptCacheMarkers(msgs, false)
	applyPromptCacheMarkers(msgs, true)
	// Reaching here without panic is the assertion.
}
