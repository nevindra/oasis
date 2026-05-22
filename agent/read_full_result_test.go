package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func TestReadFullResultTool(t *testing.T) {
	store := core.NewInMemoryToolResultStore()
	// Store plain text using TextContent so rawMessageToString can unquote it.
	id, _ := store.Put(context.Background(), core.TextContent("the quick brown fox jumps over the lazy dog"))

	tool := agent.NewReadFullResultTool(store)
	if tool.Name() != "read_full_result" {
		t.Errorf("unexpected name: %s", tool.Name())
	}

	argsJSON, _ := json.Marshal(map[string]any{
		"id":     id,
		"offset": 4,
		"length": 5,
	})

	result, err := tool.ExecuteRaw(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if !strings.Contains(string(result.Content), "quick") {
		t.Errorf("expected 'quick' in content, got %q", result.Content)
	}
}

func TestReadFullResultUnknownID(t *testing.T) {
	store := core.NewInMemoryToolResultStore()
	tool := agent.NewReadFullResultTool(store)
	argsJSON, _ := json.Marshal(map[string]any{"id": "no-such-id", "offset": 0, "length": 10})

	result, err := tool.ExecuteRaw(context.Background(), argsJSON)
	// Change C: Erase now propagates the typed Go error from Execute so that
	// dispatch policy wrappers can inspect it (Retryable, timeout, etc.).
	// ToolResult.Error is also populated for the LLM-visible string.
	if err == nil {
		t.Fatalf("expected Go error to be propagated for unknown id, got nil")
	}
	if result.Error == "" {
		t.Error("expected non-empty ToolResult.Error for unknown id")
	}
	if !strings.Contains(result.Error, "not found or expired") {
		t.Errorf("unexpected ToolResult.Error: %q", result.Error)
	}
}
