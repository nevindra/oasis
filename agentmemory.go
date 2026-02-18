package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		userMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "user", Content: userText, CreatedAt: NowUnix(),
		}

		// Embed before storing so we only write once.
		if m.embedding != nil {
			embs, err := m.embedding.Embed(ctx, []string{userText})
			if err == nil && len(embs) > 0 {
				userMsg.Embedding = embs[0]
			}
		}

		if err := m.store.StoreMessage(ctx, userMsg); err != nil {
			log.Printf("[agent:%s] persist user msg: %v", agentName, err)
		}

		asstMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "assistant", Content: assistantText, CreatedAt: NowUnix(),
		}
		if err := m.store.StoreMessage(ctx, asstMsg); err != nil {
			log.Printf("[agent:%s] persist assistant msg: %v", agentName, err)
		}

		// Auto-extract user facts from this conversation turn.
		if m.memory != nil && m.provider != nil && m.embedding != nil {
			facts := extractUserFacts(ctx, m.provider, userText, assistantText)
			for _, f := range facts {
				embs, err := m.embedding.Embed(ctx, []string{f.Fact})
				if err == nil && len(embs) > 0 {
					if err := m.memory.UpsertFact(ctx, f.Fact, f.Category, embs[0]); err != nil {
						log.Printf("[agent:%s] upsert fact: %v", agentName, err)
					}
				}
			}
		}
	}()
}

// extractUserFacts calls the provider with a lightweight prompt to extract
// durable user facts from a single conversation exchange.
// Returns nil if nothing significant was found or the call fails.
func extractUserFacts(ctx context.Context, p Provider, userMsg, assistantMsg string) []ExtractedFact {
	const systemPrompt = `Extract concrete, durable facts about the user from this conversation exchange.
Return a JSON array: [{"fact":"...","category":"..."}]
Categories: interests, preferences, background, goals, personal, skills
Rules:
- Only extract specific facts stated or strongly implied by the user
- Skip trivial or ephemeral things (greetings, thanks, small talk, one-off actions)
- Return [] if nothing significant was revealed
- Return ONLY the JSON array, no other text`

	resp, err := p.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{
			SystemMessage(systemPrompt),
			UserMessage(fmt.Sprintf("User: %s\nAssistant: %s", userMsg, assistantMsg)),
		},
	})
	if err != nil {
		return nil
	}

	content := strings.TrimSpace(resp.Content)
	var facts []ExtractedFact
	if err := json.Unmarshal([]byte(content), &facts); err != nil {
		// LLM sometimes wraps JSON in markdown — try to find the array
		start := strings.Index(content, "[")
		end := strings.LastIndex(content, "]")
		if start >= 0 && end > start {
			_ = json.Unmarshal([]byte(content[start:end+1]), &facts)
		}
	}
	return facts
}
