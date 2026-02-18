package oasis

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// agentMemory provides shared memory wiring for LLMAgent and Network.
// All fields are optional — nil means the feature is disabled.
type agentMemory struct {
	store     Store             // conversation history
	embedding EmbeddingProvider // semantic search
	memory    MemoryStore       // user facts
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

		// Semantic recall: search relevant messages across all threads
		if m.embedding != nil {
			embs, err := m.embedding.Embed(ctx, []string{task.Input})
			if err == nil && len(embs) > 0 {
				related, err := m.store.SearchMessages(ctx, embs[0], 5)
				if err == nil && len(related) > 0 {
					var recall strings.Builder
					recall.WriteString("Relevant context from past conversations:\n")
					for _, msg := range related {
						fmt.Fprintf(&recall, "[%s]: %s\n", msg.Role, msg.Content)
					}
					messages = append(messages, SystemMessage(recall.String()))
				}
			}
		}
	}

	// Current user message
	messages = append(messages, UserMessage(task.Input))
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
		if err := m.store.StoreMessage(ctx, userMsg); err != nil {
			log.Printf("[agent:%s] persist user msg: %v", agentName, err)
		}

		// Embed user message for future semantic search
		if m.embedding != nil {
			embs, err := m.embedding.Embed(ctx, []string{userText})
			if err == nil && len(embs) > 0 {
				userMsg.Embedding = embs[0]
				_ = m.store.StoreMessage(ctx, userMsg)
			}
		}

		asstMsg := Message{
			ID: NewID(), ThreadID: threadID,
			Role: "assistant", Content: assistantText, CreatedAt: NowUnix(),
		}
		if err := m.store.StoreMessage(ctx, asstMsg); err != nil {
			log.Printf("[agent:%s] persist assistant msg: %v", agentName, err)
		}
	}()
}
