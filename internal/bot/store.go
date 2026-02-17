package bot

import (
	"context"
	"log"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/memory"
)

// spawnStore persists messages and extracts facts in a background goroutine.
func (a *App) spawnStore(ctx context.Context, thread oasis.Thread, userText, assistantText string) {
	go func() {
		a.storeMessagePair(ctx, thread.ID, userText, assistantText)
		a.extractAndStoreFacts(ctx, userText, assistantText)
	}()
}

// storeMessagePair persists user + assistant messages with embedding.
func (a *App) storeMessagePair(ctx context.Context, threadID, userText, assistantText string) {
	now := oasis.NowUnix()

	// User message with embedding
	userMsg := oasis.Message{
		ID:        oasis.NewID(),
		ThreadID:  threadID,
		Role:      "user",
		Content:   userText,
		CreatedAt: now,
	}

	if a.embedding != nil {
		embs, err := a.embedding.Embed(ctx, []string{userText})
		if err == nil && len(embs) > 0 {
			userMsg.Embedding = embs[0]
		}
	}

	if err := a.store.StoreMessage(ctx, userMsg); err != nil {
		log.Printf(" [store] user message error: %v", err)
	}

	// Assistant message (no embedding)
	assistantMsg := oasis.Message{
		ID:        oasis.NewID(),
		ThreadID:  threadID,
		Role:      "assistant",
		Content:   assistantText,
		CreatedAt: now,
	}
	if err := a.store.StoreMessage(ctx, assistantMsg); err != nil {
		log.Printf(" [store] assistant message error: %v", err)
	}
}

// extractAndStoreFacts extracts user facts from the conversation turn.
func (a *App) extractAndStoreFacts(ctx context.Context, userText, assistantText string) {
	if a.memory == nil || a.intentLLM == nil || a.embedding == nil {
		return
	}

	if !memory.ShouldExtract(userText) {
		return
	}

	conversationTurn := "User: " + userText + "\nAssistant: " + assistantText

	req := oasis.ChatRequest{
		Messages: []oasis.ChatMessage{
			oasis.SystemMessage(memory.ExtractFactsPrompt),
			oasis.UserMessage(conversationTurn),
		},
		ResponseSchema: memory.ExtractFactsSchema,
	}

	resp, err := a.intentLLM.Chat(ctx, req)
	if err != nil {
		log.Printf(" [memory] fact extraction failed: %v", err)
		return
	}

	facts := memory.ParseExtractedFacts(resp.Content)
	if len(facts) == 0 {
		return
	}
	log.Printf(" [memory] extracted %d fact(s)", len(facts))

	// Embed all facts in one batch
	factTexts := make([]string, len(facts))
	for i, f := range facts {
		factTexts[i] = f.Fact
	}
	embeddings, err := a.embedding.Embed(ctx, factTexts)
	if err != nil {
		log.Printf(" [memory] fact embedding failed: %v", err)
		return
	}

	for i, fact := range facts {
		// Handle superseded facts
		if fact.Supersedes != nil {
			if err := a.memory.DeleteMatchingFacts(ctx, *fact.Supersedes); err != nil {
				log.Printf(" [memory] delete superseded failed: %v", err)
			}
		}

		var emb []float32
		if i < len(embeddings) {
			emb = embeddings[i]
		}
		if err := a.memory.UpsertFact(ctx, fact.Fact, fact.Category, emb); err != nil {
			log.Printf(" [memory] upsert fact failed: %v", err)
		}
	}
}
