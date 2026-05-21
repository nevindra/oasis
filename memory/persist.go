package memory

import (
	"context"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/nevindra/oasis/core"
)

// maxPersistContentLen is the maximum rune length for persisted message content.
// Prevents unbounded DB growth from very large user or assistant messages.
const maxPersistContentLen = 50_000

// generateTitlePrompt is the system prompt for thread title generation.
const generateTitlePrompt = `Generate a short title (max 8 words) for this conversation based on the user's message. Return ONLY the title text, nothing else. No quotes, no prefix.`

// maxTitleInputLen is the maximum rune length of user text sent to the
// title-generation LLM. Only the first fragment of the message is needed
// to produce an 8-word title; sending the full text wastes tokens.
const maxTitleInputLen = 500

// PersistMessages stores user and assistant messages in the background.
// No-op if Store is not configured or thread_id is absent.
// If steps is non-empty, they are stored as metadata on the assistant message
// so that execution traces are persisted alongside the conversation.
func (m *AgentMemory) PersistMessages(ctx context.Context, agentName string, task core.AgentTask, userText, assistantText string, steps []core.StepTrace) {
	threadID := task.ThreadID
	if m.store == nil || threadID == "" {
		return
	}

	m.initSem()

	// Backpressure: if all slots are occupied, fall back to a lightweight
	// persist (no embedding, no fact extraction, no title generation) to
	// preserve conversation history without the expensive API calls that
	// cause the slowdown. This avoids silent data loss while keeping
	// goroutine count bounded.
	fullPersist := true
	select {
	case m.sem <- struct{}{}:
	default:
		m.logger.Warn("persist backpressure: falling back to lightweight persist (no embedding/extraction)", "agent", agentName, "thread_id", threadID)
		fullPersist = false
		// Block briefly for a slot — lightweight persist is fast (DB write only).
		// If still unavailable after 2 seconds, drop to prevent goroutine pile-up.
		t := time.NewTimer(2 * time.Second)
		select {
		case m.sem <- struct{}{}:
			t.Stop()
		case <-t.C:
			m.logger.Error("persist backpressure: dropping message persist (store unresponsive)", "agent", agentName, "thread_id", threadID)
			return
		}
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() { <-m.sem }()

		// Detach from parent cancellation so persist + extraction can finish
		// even after the handler returns. Inherits context values (trace IDs).
		// Timeout prevents goroutine leaks if store or embedding hangs.
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		if m.tracer != nil {
			var span core.Span
			bgCtx, span = m.tracer.Start(bgCtx, "agent.memory.persist",
				core.StringAttr("thread_id", threadID))
			defer span.End()
		}

		// Truncate to prevent unbounded DB growth.
		userText = truncateStr(userText, maxPersistContentLen)
		assistantText = truncateStr(assistantText, maxPersistContentLen)

		// Ensure thread row exists and updated_at is current.
		created := m.ensureThread(bgCtx, agentName, task)

		now := core.NowUnix()
		userMsg := core.Message{
			ID: core.NewID(), ThreadID: threadID,
			Role: "user", Content: userText, CreatedAt: now,
		}

		// Embed before storing so we only write once.
		// Skip embedding under backpressure — it's the expensive API call
		// that causes the slowdown. Messages are still persisted for history;
		// cross-thread search quality degrades gracefully.
		if fullPersist && m.embedding != nil {
			embs, err := m.embedding.Embed(bgCtx, []string{userText})
			if err == nil && len(embs) > 0 {
				userMsg.Embedding = embs[0]
			}
		}

		if err := m.store.StoreMessage(bgCtx, userMsg); err != nil {
			m.logger.Error("persist user message failed", "agent", agentName, "error", err)
		}

		asstMsg := core.Message{
			ID: core.NewID(), ThreadID: threadID,
			Role: "assistant", Content: assistantText, CreatedAt: now + 1,
		}
		if len(steps) > 0 {
			asstMsg.Metadata = map[string]any{"steps": steps}
		}
		if err := m.store.StoreMessage(bgCtx, asstMsg); err != nil {
			m.logger.Error("persist assistant message failed", "agent", agentName, "error", err)
		}

		// Skip expensive background work under backpressure.
		if !fullPersist {
			return
		}

		// Auto-generate thread title from the first user message.
		// Only attempt on newly created threads — existing threads already have
		// titles or had their chance. This avoids a redundant GetThread call.
		if m.autoTitle && m.provider != nil && created {
			m.generateTitleNewThread(bgCtx, agentName, userText, threadID)
		}

		// Auto-extract user facts from this conversation turn.
		if m.memory != nil && m.provider != nil && m.embedding != nil {
			m.extractAndPersistFacts(bgCtx, agentName, userText, assistantText)

			// Probabilistic decay: ~5% chance per turn.
			if rand.IntN(20) == 0 {
				if err := m.memory.DecayOldFacts(bgCtx); err != nil {
					m.logger.Error("decay facts failed", "agent", agentName, "error", err)
				}
			}
		}
	}()
}

// ensureThread creates the thread row if it doesn't exist yet, and updates
// its updated_at timestamp. Called before persisting messages so that
// ListThreads / GetThread work correctly for threads created via
// WithConversationMemory. Returns true if the thread was newly created.
func (m *AgentMemory) ensureThread(ctx context.Context, agentName string, task core.AgentTask) bool {
	threadID := task.ThreadID
	now := core.NowUnix()

	existing, err := m.store.GetThread(ctx, threadID)
	if err != nil {
		// Thread doesn't exist yet — create it.
		chatID := task.ChatID
		if chatID == "" {
			chatID = threadID
		}
		if createErr := m.store.CreateThread(ctx, core.Thread{
			ID:        threadID,
			ChatID:    chatID,
			CreatedAt: now,
			UpdatedAt: now,
		}); createErr != nil {
			// May fail if another goroutine just created it (race) — log and continue.
			m.logger.Debug("create thread failed (may already exist)", "agent", agentName, "thread_id", threadID, "error", createErr)
		}
		return true
	}

	// Thread exists — bump updated_at so ListThreads ordering stays current.
	// Preserve the existing thread fields (title, metadata) to avoid clobbering
	// values set by background goroutines (e.g. AutoTitle).
	existing.UpdatedAt = now
	if updateErr := m.store.UpdateThread(ctx, existing); updateErr != nil {
		m.logger.Error("update thread timestamp failed", "agent", agentName, "thread_id", threadID, "error", updateErr)
	}
	return false
}

// generateTitleNewThread generates a thread title from the user message using
// the LLM and updates the thread. Reads the existing thread first to avoid
// overwriting ChatID, Metadata, or other fields with zero values.
func (m *AgentMemory) generateTitleNewThread(ctx context.Context, agentName, userText, threadID string) {
	resp, err := core.Chat(ctx, m.provider, core.ChatRequest{
		Messages: []core.ChatMessage{
			core.SystemMessage(generateTitlePrompt),
			core.UserMessage(truncateStr(userText, maxTitleInputLen)),
		},
	})
	if err != nil {
		m.logger.Error("generate title failed", "agent", agentName, "error", err)
		return
	}

	title := strings.TrimSpace(resp.Content)
	// Strip surrounding quotes if LLM wraps the title.
	if len(title) >= 2 && title[0] == '"' && title[len(title)-1] == '"' {
		title = title[1 : len(title)-1]
	}
	if title == "" {
		return
	}
	title = truncateStr(title, 100)

	// Read-then-update to preserve ChatID, Metadata, and other fields.
	// ensureThread may have raced and already set fields on this thread.
	thread, getErr := m.store.GetThread(ctx, threadID)
	if getErr != nil {
		m.logger.Error("get thread for title update failed", "agent", agentName, "thread_id", threadID, "error", getErr)
		return
	}
	thread.Title = title
	thread.UpdatedAt = core.NowUnix()
	if err := m.store.UpdateThread(ctx, thread); err != nil {
		m.logger.Error("update thread title failed", "agent", agentName, "thread_id", threadID, "error", err)
	}
}
