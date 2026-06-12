package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

// --- stream forwarder benchmarks ---

// BenchmarkIterChStreaming exercises newForwarder with a realistic
// per-iteration event burst (64 events ≈ one LLM call worth of text deltas).
// Serves as a regression guard for future changes to defaultIterChBufSize
// (Phase 4 finding 4.1.a): a meaningful drop in ns/op or B/op alongside a
// buffer-size change confirms the new size is workable; a regression alongside
// other refactors flags an unintended slowdown in the streaming path.
func BenchmarkIterChStreaming(b *testing.B) {
	ev := core.StreamEvent{Type: core.EventTextDelta, Content: "delta chunk"}
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		dest := make(chan core.StreamEvent, 256)
		iterCh, wait := newForwarder(ctx, dest, defaultIterChBufSize, forwarderConfig{})
		done := make(chan struct{})
		go func() {
			for range dest {
			}
			close(done)
		}()
		for range 64 {
			iterCh <- ev
		}
		close(iterCh)
		wait()
		close(dest)
		<-done
	}
}

// --- runeCount benchmarks ---

func BenchmarkRuneCount_ASCII(b *testing.B) {
	msgs := make([]core.ChatMessage, 20)
	for i := range msgs {
		msgs[i] = core.ChatMessage{Content: strings.Repeat("hello world ", 100)}
	}
	b.ResetTimer()
	for range b.N {
		runeCount(msgs)
	}
}

func BenchmarkRuneCount_Multibyte(b *testing.B) {
	msgs := make([]core.ChatMessage, 20)
	for i := range msgs {
		msgs[i] = core.ChatMessage{Content: strings.Repeat("日本語テスト ", 100)}
	}
	b.ResetTimer()
	for range b.N {
		runeCount(msgs)
	}
}

// --- TruncateStr benchmarks ---

func BenchmarkTruncateStr_Short(b *testing.B) {
	s := "hello world"
	for range b.N {
		TruncateStr(s, 100)
	}
}

func BenchmarkTruncateStr_LongASCII(b *testing.B) {
	s := strings.Repeat("x", 200_000)
	for range b.N {
		TruncateStr(s, 100_000)
	}
}

func BenchmarkTruncateStr_LongMultibyte(b *testing.B) {
	s := strings.Repeat("日本語", 50_000)
	for range b.N {
		TruncateStr(s, 100_000)
	}
}

// --- buildRoutingSummary benchmarks ---

func BenchmarkBuildRoutingSummary(b *testing.B) {
	agents := []string{"researcher", "writer", "reviewer"}
	tools := []string{"web_search", "file_read"}
	for range b.N {
		buildRoutingSummary(agents, tools)
	}
}

// --- dispatchParallel benchmarks ---

func BenchmarkDispatchParallel_Single(b *testing.B) {
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "ok"}
	}
	calls := []core.ToolCall{{ID: "1", Name: "tool", Args: json.RawMessage(`{}`)}}
	b.ResetTimer()
	for range b.N {
		dispatchParallel(context.Background(), calls, dispatch, 10)
	}
}

func BenchmarkDispatchParallel_Five(b *testing.B) {
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "ok"}
	}
	calls := make([]core.ToolCall, 5)
	for i := range calls {
		calls[i] = core.ToolCall{ID: "1", Name: "tool", Args: json.RawMessage(`{}`)}
	}
	b.ResetTimer()
	for range b.N {
		dispatchParallel(context.Background(), calls, dispatch, 10)
	}
}
