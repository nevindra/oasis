package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// route handles an incoming message through the routing pipeline.
func (a *App) route(ctx context.Context, msg oasis.IncomingMessage) {
	log.Printf(" [recv] from=%s chat=%s", msg.UserID, msg.ChatID)

	// 1. Auth check
	if !a.isOwner(ctx, msg.UserID) {
		log.Printf(" [auth] DENIED user=%s", msg.UserID)
		return
	}

	chatID := msg.ChatID

	// 2. Reply routing: check if this is a reply to an agent's ask_user question
	if msg.ReplyToMsgID != "" {
		if msg.Text != "" && a.agents.RouteReply(msg.ReplyToMsgID, msg.Text) {
			log.Printf(" [agent] routed reply to agent (reply_to=%s)", msg.ReplyToMsgID)
			return
		}
	}

	_ = a.frontend.SendTyping(ctx, chatID)

	thread, err := a.getOrCreateThread(ctx, chatID)
	if err != nil {
		log.Printf(" [thread] error: %v", err)
		return
	}

	// 3. Structural dispatch (no intent classification needed)

	// Document upload
	if msg.Document != nil {
		a.handleDocument(ctx, msg, thread)
		return
	}

	// Photo upload
	if len(msg.Photos) > 0 {
		a.handlePhoto(ctx, msg, thread)
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		return
	}

	// /new command — create a fresh thread
	if strings.TrimSpace(text) == "/new" {
		now := oasis.NowUnix()
		_ = a.store.CreateThread(ctx, oasis.Thread{
			ID: oasis.NewID(), ChatID: chatID,
			CreatedAt: now, UpdatedAt: now,
		})
		log.Println(" [cmd] /new")
		return
	}

	// /status command
	if strings.TrimSpace(text) == "/status" {
		status := a.agents.FormatStatus()
		_, _ = a.frontend.Send(ctx, chatID, status)
		log.Println(" [cmd] /status")
		return
	}

	// URL messages (structural)
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		a.handleURL(ctx, msg, thread, text)
		return
	}

	// 4. Intent classification
	intent := ClassifyIntent(ctx, a.intentLLM, text)
	log.Printf(" [intent] %v", intent)

	switch intent {
	case oasis.IntentChat:
		log.Println(" [route] chat")
		response := a.handleChatStream(ctx, chatID, text, thread)
		a.spawnStore(ctx, thread, text, response)

	case oasis.IntentAction:
		log.Println(" [route] action (sub-agent)")
		a.spawnActionAgent(ctx, chatID, text, thread.ID, msg.ID)
	}
}

// isOwner checks if the user is the authorized owner.
// Auto-registers the first user as owner.
func (a *App) isOwner(ctx context.Context, userID string) bool {
	ownerStr, err := a.store.GetConfig(ctx, "owner_user_id")
	if err == nil && ownerStr != "" {
		return ownerStr == userID
	}

	if a.config.Telegram.AllowedUserID != "" {
		return a.config.Telegram.AllowedUserID == userID
	}

	// Auto-register first user as owner
	_ = a.store.SetConfig(ctx, "owner_user_id", userID)
	log.Printf(" [auth] registered owner user_id=%s", userID)
	return true
}

// getOrCreateThread returns the most recent thread for a chatID, or creates one.
func (a *App) getOrCreateThread(ctx context.Context, chatID string) (oasis.Thread, error) {
	threads, err := a.store.ListThreads(ctx, chatID, 1)
	if err != nil {
		return oasis.Thread{}, err
	}
	if len(threads) > 0 {
		return threads[0], nil
	}
	now := oasis.NowUnix()
	thread := oasis.Thread{
		ID:        oasis.NewID(),
		ChatID:    chatID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := a.store.CreateThread(ctx, thread); err != nil {
		return oasis.Thread{}, err
	}
	return thread, nil
}

// handleDocument handles file uploads — ingest + optionally chat with context.
func (a *App) handleDocument(ctx context.Context, msg oasis.IncomingMessage, thread oasis.Thread) {
	if a.ingestFile == nil || msg.Document == nil {
		return
	}

	data, filename, err := a.frontend.DownloadFile(ctx, msg.Document.FileID)
	if err != nil {
		log.Printf(" [file] download error: %v", err)
		_, _ = a.frontend.Send(ctx, msg.ChatID, "Failed to download file.")
		return
	}

	content := string(data)
	result, err := a.ingestFile(ctx, content, filename)
	if err != nil {
		log.Printf(" [ingest] error: %v", err)
		_, _ = a.frontend.Send(ctx, msg.ChatID, "Failed to process file.")
		return
	}

	caption := msg.Caption
	if caption != "" {
		// Chat with file content as context
		maxContext := 30000
		fileContext := content
		if len(fileContext) > maxContext {
			fileContext = fileContext[:maxContext]
		}
		contextStr := fmt.Sprintf("## File: %s\n\n%s", filename, fileContext)
		response := a.handleChatStreamWithContext(ctx, msg.ChatID, caption, thread, contextStr)
		a.spawnStore(ctx, thread, caption, response)
	} else {
		_, _ = a.frontend.Send(ctx, msg.ChatID, result)
		a.spawnStore(ctx, thread, "[file upload]", result)
	}
}

// handlePhoto handles photo uploads — pass to chat LLM as images.
func (a *App) handlePhoto(ctx context.Context, msg oasis.IncomingMessage, thread oasis.Thread) {
	text := msg.Caption
	if text == "" {
		text = "[photo]"
	}
	response := a.handleChatStream(ctx, msg.ChatID, text, thread)
	a.spawnStore(ctx, thread, text, response)
}

// handleURL ingests a URL into the knowledge base.
func (a *App) handleURL(ctx context.Context, msg oasis.IncomingMessage, thread oasis.Thread, url string) {
	// Route URL ingestion through the action agent for full tool access
	a.spawnActionAgent(ctx, msg.ChatID, "Save this URL to the knowledge base: "+url, thread.ID, msg.ID)
}
