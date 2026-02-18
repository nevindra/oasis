package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
)

// defaultSemanticRecallMinScore is the minimum cosine similarity required for
// a cross-thread message to be injected into LLM context during semantic recall.
// Applied when MinScore is not passed to CrossThreadSearch.
const defaultSemanticRecallMinScore float32 = 0.60

// agentMemory provides shared memory wiring for LLMAgent and Network.
// All fields are optional — nil means the feature is disabled.
type agentMemory struct {
	store             Store             // conversation history
	embedding         EmbeddingProvider // shared embedding provider
	memory            MemoryStore       // user facts
	crossThreadSearch bool              // enabled by CrossThreadSearch option
	semanticMinScore  float32           // 0 = use defaultSemanticRecallMinScore
	provider          Provider          // for auto-extraction when memory != nil
}

// buildMessages constructs the message list: system prompt + user memory + conversation history + user input.
func (m *agentMemory) buildMessages(ctx context.Context, agentName, systemPrompt string, task AgentTask) []ChatMessage {
	var messages []ChatMessage

	// System prompt + user memory context
	prompt := m.buildSystemPrompt(ctx, systemPrompt, task.Input)
	if prompt != "" {
		messages = append(messages, SystemMessage(prompt))
	}

	// Conversation history
	threadID := task.TaskThreadID()
	if m.store != nil && threadID != "" {
		history, err := m.store.GetMessages(ctx, threadID, 20)
		if err != nil {
			log.Printf("[agent:%s] load history: %v", agentName, err)
		}
		for _, msg := range history {
			messages = append(messages, ChatMessage{Role: msg.Role, Content: msg.Content})
		}

		// Cross-thread recall: search relevant messages across all threads,
		// excluding the current thread (already in history) and low-score results.
		if m.crossThreadSearch && m.embedding != nil {
			embs, err := m.embedding.Embed(ctx, []string{task.Input})
			if err == nil && len(embs) > 0 {
				minScore := m.semanticMinScore
				if minScore == 0 {
					minScore = defaultSemanticRecallMinScore
				}
				related, err := m.store.SearchMessages(ctx, embs[0], 5)
				if err == nil {
					var recall strings.Builder
					recall.WriteString("Relevant context from past conversations:\n")
					n := 0
					for _, r := range related {
						// Skip messages from the current thread (already in history).
						if r.ThreadID == threadID {
							continue
						}
						// Skip low-relevance results (Score==0 means store didn't compute it).
						if r.Score > 0 && r.Score < minScore {
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
	}

	// Current user message, with optional multimodal attachments.
	userMsg := ChatMessage{Role: "user", Content: task.Input, Attachments: task.Attachments}
	messages = append(messages, userMsg)
	return messages
}

// buildSystemPrompt assembles the system prompt with optional user memory context.
func (m *agentMemory) buildSystemPrompt(ctx context.Context, basePrompt, input string) string {
	var parts []string
	if basePrompt != "" {
		parts = append(parts, basePrompt)
	}

	// User memory: inject known facts
	if m.memory != nil && m.embedding != nil {
		embs, err := m.embedding.Embed(ctx, []string{input})
		if err == nil && len(embs) > 0 {
			memCtx, err := m.memory.BuildContext(ctx, embs[0])
			if err == nil && memCtx != "" {
				parts = append(parts, memCtx)
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

// persistMessages stores user and assistant messages in the background.
// No-op if Store is not configured or thread_id is absent.
func (m *agentMemory) persistMessages(ctx context.Context, agentName string, task AgentTask, userText, assistantText string) {
	threadID := task.TaskThreadID()
	if m.store == nil || threadID == "" {
		return
	}

	go func() {
		// Detach from parent cancellation so persist + extraction can finish
		// even after the handler returns. Inherits context values (trace IDs).
		bgCtx := context.WithoutCancel(ctx)

		userMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "user", Content: userText, CreatedAt: NowUnix(),
		}

		// Embed before storing so we only write once.
		if m.embedding != nil {
			embs, err := m.embedding.Embed(bgCtx, []string{userText})
			if err == nil && len(embs) > 0 {
				userMsg.Embedding = embs[0]
			}
		}

		if err := m.store.StoreMessage(bgCtx, userMsg); err != nil {
			log.Printf("[agent:%s] persist user msg: %v", agentName, err)
		}

		asstMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "assistant", Content: assistantText, CreatedAt: NowUnix(),
		}
		if err := m.store.StoreMessage(bgCtx, asstMsg); err != nil {
			log.Printf("[agent:%s] persist assistant msg: %v", agentName, err)
		}

		// Auto-extract user facts from this conversation turn.
		if m.memory != nil && m.provider != nil && m.embedding != nil {
			m.extractAndPersistFacts(bgCtx, agentName, userText, assistantText)

			// Probabilistic decay: ~5% chance per turn.
			if rand.IntN(20) == 0 {
				if err := m.memory.DecayOldFacts(bgCtx); err != nil {
					log.Printf("[agent:%s] decay facts: %v", agentName, err)
				}
			}
		}
	}()
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

	facts := parseExtractedFacts(resp.Content)
	for _, f := range facts {
		// Handle supersedes: semantically find and delete the old fact.
		if f.Supersedes != nil {
			m.deleteSupersededFact(ctx, agentName, *f.Supersedes)
		}

		embs, err := m.embedding.Embed(ctx, []string{f.Fact})
		if err == nil && len(embs) > 0 {
			if err := m.memory.UpsertFact(ctx, f.Fact, f.Category, embs[0]); err != nil {
				log.Printf("[agent:%s] upsert fact: %v", agentName, err)
			}
		}
	}
}

// supersedesMinScore is the cosine similarity threshold for matching
// a superseded fact. Lower than the dedup threshold (0.85) because
// supersedes targets contradictions that are semantically similar but different.
const supersedesMinScore float32 = 0.80

// deleteSupersededFact embeds the superseded text, searches for semantically
// similar facts, and deletes matches above the threshold.
func (m *agentMemory) deleteSupersededFact(ctx context.Context, agentName, supersededText string) {
	embs, err := m.embedding.Embed(ctx, []string{supersededText})
	if err != nil || len(embs) == 0 {
		return
	}
	results, err := m.memory.SearchFacts(ctx, embs[0], 5)
	if err != nil {
		return
	}
	for _, r := range results {
		if r.Score >= supersedesMinScore {
			if err := m.memory.DeleteFact(ctx, r.Fact.ID); err != nil {
				log.Printf("[agent:%s] delete superseded fact %s: %v", agentName, r.Fact.ID, err)
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
