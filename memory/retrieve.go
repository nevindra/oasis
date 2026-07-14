// memory/retrieve.go
package memory

import (
	"context"
	"log/slog"
	"strings"

	"github.com/nevindra/oasis/core"
)

const (
	defaultMaxHistory             = 10
	defaultKeepRecent             = 3
	defaultSemanticRecallMinScore = float32(0.60)
	maxRecallContentLen           = 500
	defaultRecallTopK             = 8
)

// RetrieveProcessor transforms a RetrieveContext on the hot path.
// Errors from processors are logged and the offending processor is skipped;
// the pipeline continues. The agent always gets some prompt.
type RetrieveProcessor interface {
	Process(ctx context.Context, in *RetrieveContext) error
}

// RetrieveContext carries everything a retrieve processor needs.
type RetrieveContext struct {
	AgentName string
	Task      core.AgentTask
	Embedding []float32

	History     []core.Message
	Selected    map[core.MemoryKind][]core.MemoryItem // by Kind, set by BatchedRecall
	Pinned      []core.MemoryItem
	CrossThread []core.ScoredMessage

	SystemPrompt string
	PromptParts  []string

	Store        core.MemoryItemStore // memory items (pinned, recall); may be nil
	HistoryStore core.Store           // conversation history (threads, messages)
	Embedder     core.EmbeddingProvider
	Logger       *slog.Logger
}

func runRetrievePipeline(ctx context.Context, in *RetrieveContext, procs []RetrieveProcessor) {
	for _, p := range procs {
		if err := p.Process(ctx, in); err != nil {
			in.Logger.Warn("retrieve processor error", "error", err)
		}
	}
}

// BuildMessages runs the retrieve pipeline and returns the LLM-ready message list.
func (m *AgentMemory) BuildMessages(ctx context.Context, agentName, systemPrompt string, task core.AgentTask) []core.ChatMessage {
	if m.tracer != nil {
		var span core.Span
		ctx, span = m.tracer.Start(ctx, "agent.memory.load",
			core.StringAttr("thread_id", task.ThreadID))
		defer span.End()
	}

	// Fast path: skip the full retrieve pipeline when no memory backend is configured.
	if m.store == nil && m.itemStore == nil && m.embedding == nil &&
		len(m.retrieveProcs) == 0 && !m.semanticRecall && m.maxTokens == 0 {
		var out []core.ChatMessage
		if strings.TrimSpace(systemPrompt) != "" {
			out = append(out, core.SystemMessage(systemPrompt))
		}
		out = append(out, core.ChatMessage{
			Role: core.RoleUser, Content: task.Input, Attachments: task.Attachments,
		})
		return out
	}

	in := &RetrieveContext{
		AgentName:    agentName,
		Task:         task,
		Selected:     nil,
		SystemPrompt: systemPrompt,
		Store:        m.itemStore,
		HistoryStore: m.store,
		Embedder:     m.embedding,
		Logger:       m.logger,
	}

	runRetrievePipeline(ctx, in, m.cachedRetrieveChain)

	// Assemble final []core.ChatMessage.
	//
	// Message order:
	//   [0]   system  — stable: systemPrompt only (no RAG content)
	//   [1..N] history — stable: loaded from store
	//   [N+1] user    — RAG context block (only if PromptParts non-empty)
	//                   varies per turn; kept out of system to preserve cache hits
	//   [N+2] user    — current user input
	//
	// Note: pinned items (LoadPinned) and semantic recall (BatchedRecall,
	// RecallCrossThread) all land in the retrieved-context message rather than
	// the system message. They are still authoritative content — the
	// <context>...</context> wrapper signals to the LLM that this is retrieved
	// context rather than user instruction.
	out := make([]core.ChatMessage, 0, len(in.History)+3)
	if strings.TrimSpace(systemPrompt) != "" {
		out = append(out, core.SystemMessage(systemPrompt))
	}
	if m.replayToolCalls {
		// Expand persisted step traces back into tool_call/tool_result pairs
		// (see replay.go). Expansion happens AFTER trimming, per whole stored
		// message, so a trim can never split an assistant call from its result.
		out = append(out, expandHistory(in.History, m.replayVerbatimTurns, m.protectedTools)...)
	} else {
		for _, msg := range in.History {
			out = append(out, core.ChatMessage{Role: core.Role(msg.Role), Content: msg.Content})
		}
	}
	if len(in.PromptParts) > 0 {
		out = append(out, core.ChatMessage{
			Role:    core.RoleUser,
			Content: "<context>\n" + strings.Join(in.PromptParts, "\n\n") + "\n</context>",
		})
	}
	out = append(out, core.ChatMessage{
		Role: core.RoleUser, Content: task.Input, Attachments: task.Attachments,
	})
	return mergeAdjacentSystemMessages(out)
}

func (m *AgentMemory) defaultRetrieveChain() []RetrieveProcessor {
	chain := []RetrieveProcessor{
		EmbedInput{},
		LoadHistory{Limit: m.maxHistory},
	}
	if m.itemStore != nil {
		chain = append(chain, LoadPinned{})
		chain = append(chain, BatchedRecall{
			Kinds: m.recallKinds,
			TopK:  m.recallTopK,
		})
	}
	if m.semanticRecall {
		chain = append(chain, RecallCrossThread{MinScore: m.semanticMinScore})
	}
	if m.maxTokens > 0 {
		trimProc := TrimToBudget{
			Budget:     m.maxTokens,
			Semantic:   m.semanticTrimming,
			KeepRecent: m.keepRecent,
		}
		if m.semanticTrimming {
			// Use the dedicated trimming embedder if set, else fall back to the main one.
			trimProc.Embedder = m.trimmingEmbedding
			if trimProc.Embedder == nil {
				trimProc.Embedder = m.embedding
			}
			m.initTrimCache()
			trimProc.TrimCache = m.trimCache
		}
		chain = append(chain, trimProc)
	}
	chain = append(chain, m.retrieveProcs...)
	return chain
}

func mergeAdjacentSystemMessages(messages []core.ChatMessage) []core.ChatMessage {
	if len(messages) < 2 {
		return messages
	}
	out := make([]core.ChatMessage, 0, len(messages))
	for _, m := range messages {
		if len(out) > 0 && out[len(out)-1].Role == "system" && m.Role == "system" {
			prev := out[len(out)-1]
			if prev.Content == "" {
				prev.Content = m.Content
			} else if m.Content != "" {
				prev.Content = prev.Content + "\n\n" + m.Content
			}
			out[len(out)-1] = prev
			continue
		}
		out = append(out, m)
	}
	return out
}
