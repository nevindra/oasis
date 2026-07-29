package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestToolResultToDispatch_CopiesUI(t *testing.T) {
	ui := &core.UIComponent{Name: "Card", Props: json.RawMessage(`{"a":1}`)}
	dr := toolResultToDispatch(core.ToolResult{Content: "x", UI: ui}, nil)
	if dr.UI != ui {
		t.Fatalf("DispatchResult.UI = %+v, want the same pointer", dr.UI)
	}
}

func TestToolResultToDispatch_NoUIOnError(t *testing.T) {
	dr := toolResultToDispatch(core.ToolResult{Error: "boom", UI: &core.UIComponent{Name: "Card"}}, nil)
	if dr.UI != nil {
		t.Fatalf("DispatchResult.UI = %+v, want nil on error result", dr.UI)
	}
}

func TestDispatchParallel_PropagatesUI(t *testing.T) {
	ui := &core.UIComponent{Name: "Card", Props: json.RawMessage(`{}`)}
	dispatch := func(_ context.Context, _ core.ToolCall) DispatchResult {
		return DispatchResult{Content: "ok", UI: ui}
	}

	// Single-call fast path.
	single := dispatchParallel(context.Background(), []core.ToolCall{{ID: "1", Name: "t"}}, dispatch, 4)
	if single[0].ui != ui {
		t.Fatalf("single: toolExecResult.ui = %+v, want set", single[0].ui)
	}

	// Multi-call worker path.
	multi := dispatchParallel(context.Background(),
		[]core.ToolCall{{ID: "1", Name: "t"}, {ID: "2", Name: "t"}}, dispatch, 4)
	for i, r := range multi {
		if r.ui != ui {
			t.Fatalf("multi[%d]: toolExecResult.ui = %+v, want set", i, r.ui)
		}
	}
}

func TestToolResultToDispatch_EmptyResultFloor(t *testing.T) {
	dr := toolResultToDispatch(core.ToolResult{}, nil)
	if dr.Content != "(tool completed with no output)" {
		t.Fatalf("content = %q, want the empty-output floor", dr.Content)
	}
	if dr.IsError {
		t.Fatal("floor must not be an error")
	}
	// A result carrying attachments or UI keeps its empty content — the
	// payload itself is the output.
	withAtt := toolResultToDispatch(core.ToolResult{Attachments: []core.Attachment{{}}}, nil)
	if withAtt.Content != "" {
		t.Fatalf("attachment result content = %q, want empty", withAtt.Content)
	}
	withUI := toolResultToDispatch(core.ToolResult{UI: &core.UIComponent{Name: "Card"}}, nil)
	if withUI.Content != "" {
		t.Fatalf("ui result content = %q, want empty", withUI.Content)
	}
}
