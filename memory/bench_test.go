// memory/bench_test.go
package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/nevindra/oasis/core"
)

func buildConversationHistory(store *testStore, threadID string, n int) {
	ctx := context.Background()
	_ = store.CreateThread(ctx, core.Thread{ID: threadID})
	for i := range n {
		_ = store.StoreMessage(ctx, core.Message{
			ID:       fmt.Sprintf("msg-%d", i),
			ThreadID: threadID,
			Role:     "user",
			Content:  fmt.Sprintf("message number %d: what is the meaning of life?", i),
		})
		_ = store.StoreMessage(ctx, core.Message{
			ID:       fmt.Sprintf("rsp-%d", i),
			ThreadID: threadID,
			Role:     "assistant",
			Content:  fmt.Sprintf("response number %d: the answer is 42", i),
		})
	}
}

func BenchmarkBuildMessages(b *testing.B) {
	for _, n := range []int{10, 50, 100, 500} {
		b.Run(fmt.Sprintf("messages=%d", n*2), func(b *testing.B) {
			store := newConformanceStore(b)
			buildConversationHistory(store, "t1", n)

			var m AgentMemory
			m.Init(AgentMemoryConfig{
				Store:      store,
				MaxHistory: n * 2,
				Logger:     discardLogger(),
			})
			task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "what should I do next?"}

			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				_ = m.BuildMessages(context.Background(), "agent", "you are a helpful assistant", task)
			}
		})
	}
}

func BenchmarkRemember(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("facts=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				store := newConformanceStore(b)
				var m AgentMemory
				m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})
				b.StartTimer()

				for i := range n {
					_ = m.Remember(context.Background(), core.MemoryItem{
						ID:      fmt.Sprintf("fact-%d", i),
						Kind:    KindFact,
						Content: fmt.Sprintf("user prefers option %d over alternatives", i),
						Scope:   Scoped(ScopeResource, "user1"),
					})
				}
			}
		})
	}
}

func BenchmarkRecall(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("items=%d", n), func(b *testing.B) {
			store := newConformanceStore(b)
			emb := &fakeEmbedder{out: make([][]float32, n+1)}
			for i := range emb.out {
				emb.out[i] = []float32{1, 0, 0}
			}

			var m AgentMemory
			m.Init(AgentMemoryConfig{
				Store:       store,
				Embedding:   emb,
				RecallTopK:  5,
				RecallKinds: []core.MemoryKind{KindFact},
				Logger:      discardLogger(),
			})

			ctx := context.Background()
			for i := range n {
				_ = store.Upsert(ctx, core.MemoryItem{
					ID:        fmt.Sprintf("fact-%d", i),
					Kind:      KindFact,
					Content:   fmt.Sprintf("fact number %d about the user", i),
					Scope:     Scoped(ScopeResource, "user1"),
					Embedding: []float32{1, 0, 0},
				})
			}

			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				_, _ = m.Recall(ctx, "tell me about the user", RecallKind(KindFact), RecallLimit(5))
			}
		})
	}
}
