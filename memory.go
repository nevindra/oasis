package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ExtractedFact is a user fact extracted from a conversation turn.
// Returned by the auto-extraction pipeline and persisted to MemoryStore.
type ExtractedFact struct {
	Fact       string  `json:"fact"`
	Category   string  `json:"category"`
	Supersedes *string `json:"supersedes,omitempty"`
}

// MemoryStore provides long-term user memory with semantic deduplication.
// Optional — pass to WithUserMemory() to enable.
type MemoryStore interface {
	UpsertFact(ctx context.Context, fact, category string, embedding []float32) error
	// SearchFacts returns facts semantically similar to the query embedding,
	// sorted by Score descending. Only facts with confidence >= 0.3 are returned.
	SearchFacts(ctx context.Context, embedding []float32, topK int) ([]ScoredFact, error)
	BuildContext(ctx context.Context, queryEmbedding []float32) (string, error)
	// DeleteFact removes a single fact by its ID.
	DeleteFact(ctx context.Context, factID string) error
	// DeleteMatchingFacts removes facts whose text contains the given substring.
	// Implementations must treat pattern as a plain substring match — never as
	// SQL LIKE, regex, or any other pattern language — to prevent injection.
	DeleteMatchingFacts(ctx context.Context, pattern string) error
	DecayOldFacts(ctx context.Context) error
	Init(ctx context.Context) error
}

// defaultSemanticRecallMinScore is the minimum cosine similarity required for
// a cross-thread message to be injected into LLM context during semantic recall.
// Applied when MinScore is not passed to CrossThreadSearch.
const defaultSemanticRecallMinScore float32 = 0.60

// defaultMaxHistory is the number of recent messages loaded from conversation
// history when MaxHistory is not passed to WithConversationMemory.
const defaultMaxHistory = 10

// maxPersistContentLen is the maximum rune length for persisted message content.
// Prevents unbounded DB growth from very large user or assistant messages.
const maxPersistContentLen = 50_000

// estimateTokens returns a rough token count for a chat message.
// Uses the ~4 characters per token heuristic, plus a small overhead
// for role markers and message framing.
func estimateTokens(msg ChatMessage) int {
	return utf8.RuneCountInString(msg.Content)/4 + 4
}

// maxPersistGoroutines is the maximum number of concurrent background
// persist goroutines. Provides backpressure when the store or embedding
// provider is slow, preventing unbounded goroutine growth.
const maxPersistGoroutines = 16

// agentMemory provides shared memory wiring for LLMAgent and Network.
// All fields are optional — nil means the feature is disabled.
type agentMemory struct {
	store             Store             // conversation history
	embedding         EmbeddingProvider // shared embedding provider
	memory            MemoryStore       // user facts
	crossThreadSearch bool              // enabled by CrossThreadSearch option
	semanticMinScore  float32           // 0 = use defaultSemanticRecallMinScore
	maxHistory        int               // 0 = use defaultMaxHistory
	maxTokens         int               // 0 = disabled (no token-based trimming)
	autoTitle         bool              // generate thread title from first message
	provider          Provider          // for auto-extraction when memory != nil
	tracer            Tracer            // nil = no tracing
	logger            *slog.Logger      // never nil (nopLogger fallback)
	semOnce           sync.Once        // guards sem initialization
	sem               chan struct{}     // bounded concurrency for background goroutines
	wg                sync.WaitGroup   // tracks in-flight persist goroutines
}

// initSem lazily initializes the semaphore. Safe for concurrent callers.
// If sem was pre-set (e.g. in tests), the existing channel is preserved.
func (m *agentMemory) initSem() {
	m.semOnce.Do(func() {
		if m.sem == nil {
			m.sem = make(chan struct{}, maxPersistGoroutines)
		}
	})
}

// drain waits for all in-flight persist goroutines to finish.
// Called during agent/network shutdown to prevent data loss.
func (m *agentMemory) drain() {
	m.wg.Wait()
}

// buildMessages constructs the message list: system prompt + user memory + conversation history + user input.
func (m *agentMemory) buildMessages(ctx context.Context, agentName, systemPrompt string, task AgentTask) []ChatMessage {
	if m.tracer != nil {
		var span Span
		ctx, span = m.tracer.Start(ctx, "agent.memory.load",
			StringAttr("thread_id", task.TaskThreadID()))
		defer span.End()
	}

	var messages []ChatMessage

	// Embed input once — reused by both user memory and cross-thread search
	// to avoid duplicate embedding API calls.
	var inputEmbedding []float32
	if m.embedding != nil && (m.memory != nil || m.crossThreadSearch) {
		embs, err := m.embedding.Embed(ctx, []string{task.Input})
		if err == nil && len(embs) > 0 {
			inputEmbedding = embs[0]
		}
	}

	// System prompt + user memory context
	prompt := m.buildSystemPrompt(ctx, systemPrompt, inputEmbedding)
	if prompt != "" {
		messages = append(messages, SystemMessage(prompt))
	}

	// Conversation history
	threadID := task.TaskThreadID()
	if m.store != nil && threadID != "" {
		limit := m.maxHistory
		if limit <= 0 {
			limit = defaultMaxHistory
		}
		history, err := m.store.GetMessages(ctx, threadID, limit)
		if err != nil {
			m.logger.Error("load history failed", "agent", agentName, "error", err)
		}
		for _, msg := range history {
			messages = append(messages, ChatMessage{Role: msg.Role, Content: msg.Content})
		}

		// Token-based trimming: drop oldest history messages until budget is met.
		if m.maxTokens > 0 && len(messages) > 0 {
			// Find the boundary between non-history and history messages.
			// History starts after the system prompt (index historyStart) and
			// ends before we append cross-thread recall and user input.
			historyStart := 0
			if messages[0].Role == "system" {
				historyStart = 1
			}
			historyEnd := len(messages) // history is everything from historyStart to end (so far)

			// Sum tokens in history portion.
			total := 0
			for i := historyStart; i < historyEnd; i++ {
				total += estimateTokens(messages[i])
			}

			// Drop oldest (lowest index in history) until we fit.
			for total > m.maxTokens && historyStart < historyEnd {
				total -= estimateTokens(messages[historyStart])
				historyStart++
			}

			// Rebuild: keep pre-history messages + trimmed history.
			if historyStart > 0 {
				trimmed := make([]ChatMessage, 0, len(messages))
				if messages[0].Role == "system" {
					trimmed = append(trimmed, messages[0])
				}
				trimmed = append(trimmed, messages[historyStart:historyEnd]...)
				messages = trimmed
			}
		}

		// Cross-thread recall: search relevant messages across all threads,
		// excluding the current thread (already in history) and low-score results.
		if m.crossThreadSearch && len(inputEmbedding) > 0 {
			minScore := m.semanticMinScore
			if minScore == 0 {
				minScore = defaultSemanticRecallMinScore
			}
			related, err := m.store.SearchMessages(ctx, inputEmbedding, 5)
			if err == nil {
				var recall strings.Builder
				recall.WriteString("Relevant context from past conversations:\n")
				n := 0
				for _, r := range related {
					// Skip messages from the current thread (already in history).
					if r.ThreadID == threadID {
						continue
					}
					// Skip low-relevance results.
					if r.Score < minScore {
						continue
					}
					fmt.Fprintf(&recall, "[%s]: %s\n", r.Role, r.Content)
					n++
				}
				if n > 0 {
					messages = append(messages, SystemMessage(recall.String()))
				}
			}
		}
	}

	// Current user message, with optional multimodal attachments.
	userMsg := ChatMessage{Role: "user", Content: task.Input, Attachments: task.Attachments}
	messages = append(messages, userMsg)
	return messages
}

// buildSystemPrompt assembles the system prompt with optional user memory context.
// inputEmbedding is the pre-computed embedding of the user input (may be nil).
func (m *agentMemory) buildSystemPrompt(ctx context.Context, basePrompt string, inputEmbedding []float32) string {
	var parts []string
	if basePrompt != "" {
		parts = append(parts, basePrompt)
	}

	// User memory: inject known facts
	if m.memory != nil && len(inputEmbedding) > 0 {
		memCtx, err := m.memory.BuildContext(ctx, inputEmbedding)
		if err == nil && memCtx != "" {
			parts = append(parts, memCtx)
		}
	}

	return strings.Join(parts, "\n\n")
}

// ensureThread creates the thread row if it doesn't exist yet, and updates
// its updated_at timestamp. Called before persisting messages so that
// ListThreads / GetThread work correctly for threads created via
// WithConversationMemory. Returns true if the thread was newly created.
func (m *agentMemory) ensureThread(ctx context.Context, agentName string, task AgentTask) bool {
	threadID := task.TaskThreadID()
	now := NowUnix()

	_, err := m.store.GetThread(ctx, threadID)
	if err != nil {
		// Thread doesn't exist yet — create it.
		chatID := task.TaskChatID()
		if chatID == "" {
			chatID = threadID
		}
		if createErr := m.store.CreateThread(ctx, Thread{
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
	if updateErr := m.store.UpdateThread(ctx, Thread{
		ID:        threadID,
		UpdatedAt: now,
	}); updateErr != nil {
		m.logger.Error("update thread timestamp failed", "agent", agentName, "thread_id", threadID, "error", updateErr)
	}
	return false
}

// persistMessages stores user and assistant messages in the background.
// No-op if Store is not configured or thread_id is absent.
// If steps is non-empty, they are stored as metadata on the assistant message
// so that execution traces are persisted alongside the conversation.
func (m *agentMemory) persistMessages(ctx context.Context, agentName string, task AgentTask, userText, assistantText string, steps []StepTrace) {
	threadID := task.TaskThreadID()
	if m.store == nil || threadID == "" {
		return
	}

	m.initSem()

	// Backpressure: if all slots are occupied, drop this persist to avoid
	// unbounded goroutine growth when the store is slow.
	select {
	case m.sem <- struct{}{}:
	default:
		m.logger.Warn("persist backpressure: dropping message persist", "agent", agentName, "thread_id", threadID)
		return
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
			var span Span
			bgCtx, span = m.tracer.Start(bgCtx, "agent.memory.persist",
				StringAttr("thread_id", threadID))
			defer span.End()
		}

		// Truncate to prevent unbounded DB growth.
		userText = truncateStr(userText, maxPersistContentLen)
		assistantText = truncateStr(assistantText, maxPersistContentLen)

		// Ensure thread row exists and updated_at is current.
		created := m.ensureThread(bgCtx, agentName, task)

		now := NowUnix()
		userMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "user", Content: userText, CreatedAt: now,
		}

		// Embed before storing so we only write once.
		if m.embedding != nil {
			embs, err := m.embedding.Embed(bgCtx, []string{userText})
			if err == nil && len(embs) > 0 {
				userMsg.Embedding = embs[0]
			}
		}

		if err := m.store.StoreMessage(bgCtx, userMsg); err != nil {
			m.logger.Error("persist user message failed", "agent", agentName, "error", err)
		}

		asstMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "assistant", Content: assistantText, CreatedAt: now + 1,
		}
		if len(steps) > 0 {
			asstMsg.Metadata = map[string]any{"steps": steps}
		}
		if err := m.store.StoreMessage(bgCtx, asstMsg); err != nil {
			m.logger.Error("persist assistant message failed", "agent", agentName, "error", err)
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

// generateTitlePrompt is the system prompt for thread title generation.
const generateTitlePrompt = `Generate a short title (max 8 words) for this conversation based on the user's message. Return ONLY the title text, nothing else. No quotes, no prefix.`

// generateTitleNewThread generates a thread title from the user message using
// the LLM and updates the thread. Called only for newly created threads, so it
// skips the GetThread check — a new thread always has an empty title.
func (m *agentMemory) generateTitleNewThread(ctx context.Context, agentName, userText, threadID string) {
	resp, err := m.provider.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{
			SystemMessage(generateTitlePrompt),
			UserMessage(userText),
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
	if r := []rune(title); len(r) > 100 {
		title = string(r[:100])
	}

	if err := m.store.UpdateThread(ctx, Thread{
		ID:        threadID,
		Title:     title,
		UpdatedAt: NowUnix(),
	}); err != nil {
		m.logger.Error("update thread title failed", "agent", agentName, "thread_id", threadID, "error", err)
	}
}

// extractFactsPrompt is the system prompt for fact extraction with supersedes support.
const extractFactsPrompt = `You are a memory extraction system. Given a conversation between a user and an assistant, extract factual information ABOUT THE USER.

Extract facts like:
- Personal info (name, job, location, timezone)
- Preferences (communication style, tools, languages)
- Habits and routines
- Current projects or goals
- Relationships and people they mention

Rules:
- Only extract facts clearly stated or strongly implied by the USER (not the assistant)
- Each fact should be a single, concise statement
- Categorize each fact as: personal, preference, work, habit, or relationship
- If a new fact CONTRADICTS or UPDATES a previously known fact, include a "supersedes" field with the old fact text
- If no new user facts are present, return an empty array
- Do NOT extract facts about the assistant or general knowledge

Return a JSON array:
[{"fact": "User moved to Bali", "category": "personal", "supersedes": "Lives in Jakarta"}]

If the fact does not supersede anything, omit the "supersedes" field:
[{"fact": "User's name is Nev", "category": "personal"}]

Return ONLY the JSON array, no extra text. Return [] if no facts found.`

// shouldExtractFacts returns true if the user message is worth running
// fact extraction on. Skips trivial messages to avoid wasted LLM calls.
func shouldExtractFacts(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 10 {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, s := range trivialMessages {
		if lower == s {
			return false
		}
	}
	return true
}

var trivialMessages = []string{
	"ok", "oke", "okay", "okey",
	"thanks", "thank you", "makasih", "thx", "ty",
	"yes", "no", "ya", "ga", "gak", "nggak", "engga",
	"nice", "sip", "siap", "oke sip",
	"lol", "haha", "wkwk", "wkwkwk",
	"hmm", "hm", "oh", "ah",
	"good", "great", "cool", "yep", "nope",
}

// extractAndPersistFacts runs fact extraction on the conversation turn and
// persists results to MemoryStore, including semantic supersedes handling.
func (m *agentMemory) extractAndPersistFacts(ctx context.Context, agentName, userText, assistantText string) {
	if !shouldExtractFacts(userText) {
		return
	}

	resp, err := m.provider.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{
			SystemMessage(extractFactsPrompt),
			UserMessage(fmt.Sprintf("User: %s\nAssistant: %s", userText, assistantText)),
		},
	})
	if err != nil {
		return
	}

	facts := sanitizeFacts(parseExtractedFacts(resp.Content))
	if len(facts) == 0 {
		return
	}

	// Handle supersedes: batch-embed all superseded texts in a single call,
	// then search+delete with the pre-computed embeddings.
	var supersededTexts []string
	for _, f := range facts {
		if f.Supersedes != nil {
			supersededTexts = append(supersededTexts, *f.Supersedes)
		}
	}
	if len(supersededTexts) > 0 {
		supersededEmbs, embErr := m.embedding.Embed(ctx, supersededTexts)
		if embErr == nil && len(supersededEmbs) == len(supersededTexts) {
			for _, emb := range supersededEmbs {
				m.deleteSupersededFactByEmbedding(ctx, agentName, emb)
			}
		}
	}

	// Batch embed all facts in a single call.
	texts := make([]string, len(facts))
	for i, f := range facts {
		texts[i] = f.Fact
	}
	embs, err := m.embedding.Embed(ctx, texts)
	if err != nil || len(embs) != len(facts) {
		return
	}
	for i, f := range facts {
		if err := m.memory.UpsertFact(ctx, f.Fact, f.Category, embs[i]); err != nil {
			m.logger.Error("upsert fact failed", "agent", agentName, "error", err)
		}
	}
}

// maxFactLength is the maximum rune length for an extracted fact.
// Prevents attacker-controlled content from bloating the memory store
// and being injected into future system prompts.
const maxFactLength = 200

// validFactCategories is the set of allowed category values for extracted facts.
// Facts with categories outside this set are dropped to prevent injection of
// arbitrary content through the extraction pipeline.
var validFactCategories = map[string]bool{
	"personal":     true,
	"preference":   true,
	"work":         true,
	"habit":        true,
	"relationship": true,
}

// sanitizeFacts filters and cleans extracted facts. It drops facts with invalid
// categories or empty text, and truncates facts exceeding maxFactLength.
func sanitizeFacts(facts []ExtractedFact) []ExtractedFact {
	valid := make([]ExtractedFact, 0, len(facts))
	for _, f := range facts {
		if f.Fact == "" || !validFactCategories[f.Category] {
			continue
		}
		f.Fact = truncateStr(f.Fact, maxFactLength)
		valid = append(valid, f)
	}
	return valid
}

// supersedesMinScore is the cosine similarity threshold for matching
// a superseded fact. Lower than the dedup threshold (0.85) because
// supersedes targets contradictions that are semantically similar but different.
const supersedesMinScore float32 = 0.80

// deleteSupersededFactByEmbedding searches for semantically similar facts
// using a pre-computed embedding and deletes matches above the threshold.
func (m *agentMemory) deleteSupersededFactByEmbedding(ctx context.Context, agentName string, embedding []float32) {
	results, err := m.memory.SearchFacts(ctx, embedding, 5)
	if err != nil {
		return
	}
	for _, r := range results {
		if r.Score >= supersedesMinScore {
			if err := m.memory.DeleteFact(ctx, r.Fact.ID); err != nil {
				m.logger.Error("delete superseded fact failed", "agent", agentName, "fact_id", r.Fact.ID, "error", err)
			}
		}
	}
}

// parseExtractedFacts parses the LLM's fact extraction response.
// Handles both raw JSON arrays and markdown-fenced responses.
func parseExtractedFacts(response string) []ExtractedFact {
	content := strings.TrimSpace(response)
	var facts []ExtractedFact
	if err := json.Unmarshal([]byte(content), &facts); err != nil {
		// LLM sometimes wraps JSON in markdown fences — find the array.
		start := strings.Index(content, "[")
		end := strings.LastIndex(content, "]")
		if start >= 0 && end > start {
			_ = json.Unmarshal([]byte(content[start:end+1]), &facts)
		}
	}
	return facts
}
