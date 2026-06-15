package agent

import (
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestApplyResultTransform_NilIsPassthrough(t *testing.T) {
	in := core.ToolResult{Content: "raw"}
	if got := applyResultTransform(nil, "t", in, true, nil); got.Content != "raw" {
		t.Errorf("nil sink: Content = %q, want raw", got.Content)
	}
	st := &core.SinkTransform{} // Result nil
	if got := applyResultTransform(st, "t", in, true, nil); got.Content != "raw" {
		t.Errorf("nil Result fn: Content = %q, want raw", got.Content)
	}
}

func TestApplyResultTransform_Applies(t *testing.T) {
	st := &core.SinkTransform{Result: func(name string, r core.ToolResult) core.ToolResult {
		return core.ToolResult{Content: "redacted"}
	}}
	got := applyResultTransform(st, "t", core.ToolResult{Content: "secret"}, true, nil)
	if got.Content != "redacted" {
		t.Errorf("Content = %q, want redacted", got.Content)
	}
}

func TestApplyResultTransform_PanicFailClosed(t *testing.T) {
	st := &core.SinkTransform{Result: func(name string, r core.ToolResult) core.ToolResult {
		panic("boom")
	}}
	in := core.ToolResult{Content: "secret", Attachments: []core.Attachment{{Data: []byte("x")}}}
	got := applyResultTransform(st, "t", in, true /*failClosed*/, nil)
	if got.Content != redactionFailed {
		t.Errorf("Content = %q, want placeholder %q", got.Content, redactionFailed)
	}
	if len(got.Attachments) != 0 {
		t.Error("fail-closed must drop attachments (no raw leak)")
	}
}

func TestApplyResultTransform_PanicFailOpen(t *testing.T) {
	st := &core.SinkTransform{Result: func(name string, r core.ToolResult) core.ToolResult {
		panic("boom")
	}}
	in := core.ToolResult{Content: "secret"}
	got := applyResultTransform(st, "t", in, false /*failOpen=model*/, nil)
	if got.Content != "secret" {
		t.Errorf("fail-open: Content = %q, want raw passthrough", got.Content)
	}
}

func TestApplyArgsTransform_PanicFailClosed(t *testing.T) {
	st := &core.SinkTransform{Args: func(name string, a json.RawMessage) json.RawMessage {
		panic("boom")
	}}
	got := applyArgsTransform(st, "t", json.RawMessage(`{"secret":"v"}`), nil)
	if string(got) != `"`+redactionFailed+`"` {
		t.Errorf("args fail-closed = %s, want quoted placeholder", got)
	}
}
