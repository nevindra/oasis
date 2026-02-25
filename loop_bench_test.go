package oasis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- runeCount benchmarks ---

func BenchmarkRuneCount_ASCII(b *testing.B) {
	msgs := make([]ChatMessage, 20)
	for i := range msgs {
		msgs[i] = ChatMessage{Content: strings.Repeat("hello world ", 100)}
	}
	b.ResetTimer()
	for range b.N {
		runeCount(msgs)
	}
}

func BenchmarkRuneCount_Multibyte(b *testing.B) {
	msgs := make([]ChatMessage, 20)
	for i := range msgs {
		msgs[i] = ChatMessage{Content: strings.Repeat("日本語テスト ", 100)}
	}
	b.ResetTimer()
	for range b.N {
		runeCount(msgs)
	}
}

// --- truncateStr benchmarks ---

func BenchmarkTruncateStr_Short(b *testing.B) {
	s := "hello world"
	for range b.N {
		truncateStr(s, 100)
	}
}

func BenchmarkTruncateStr_LongASCII(b *testing.B) {
	s := strings.Repeat("x", 200_000)
	for range b.N {
		truncateStr(s, 100_000)
	}
}

func BenchmarkTruncateStr_LongMultibyte(b *testing.B) {
	s := strings.Repeat("日本語", 50_000)
	for range b.N {
		truncateStr(s, 100_000)
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
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "ok"}
	}
	calls := []ToolCall{{ID: "1", Name: "tool", Args: json.RawMessage(`{}`)}}
	b.ResetTimer()
	for range b.N {
		dispatchParallel(context.Background(), calls, dispatch)
	}
}

func BenchmarkDispatchParallel_Five(b *testing.B) {
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "ok"}
	}
	calls := make([]ToolCall, 5)
	for i := range calls {
		calls[i] = ToolCall{ID: "1", Name: "tool", Args: json.RawMessage(`{}`)}
	}
	b.ResetTimer()
	for range b.N {
		dispatchParallel(context.Background(), calls, dispatch)
	}
}
