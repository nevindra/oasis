package app

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

	conv, err := a.store.GetOrCreateConversation(ctx, chatID)
	if err != nil {
		log.Printf(" [conv] error: %v", err)
		return
	}

	// 3. Structural dispatch (no intent classification needed)

	// Document upload
	if msg.Document != nil {
		a.handleDocument(ctx, msg, conv)
		return
	}

	// Photo upload
	if len(msg.Photos) > 0 {
		a.handlePhoto(ctx, msg, conv)
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		return
	}

	// /new command
	if strings.TrimSpace(text) == "/new" {
		_, _ = a.store.GetOrCreateConversation(ctx, chatID)
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
		a.handleURL(ctx, msg, conv, text)
		return
	}

	// 4. Intent classification
	intent := ClassifyIntent(ctx, a.intentLLM, text)
	log.Printf(" [intent] %v", intent)

	switch intent {
	case oasis.IntentChat:
		log.Println(" [route] chat")
		response := a.handleChatStream(ctx, chatID, text, conv)
		a.spawnStore(ctx, conv, text, response)

	case oasis.IntentAction:
		log.Println(" [route] action (sub-agent)")
		a.spawnActionAgent(ctx, chatID, text, conv.ID, msg.ID)
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

// handleDocument handles file uploads — ingest + optionally chat with context.
func (a *App) handleDocument(ctx context.Context, msg oasis.IncomingMessage, conv oasis.Conversation) {
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
		response := a.handleChatStreamWithContext(ctx, msg.ChatID, caption, conv, contextStr)
		a.spawnStore(ctx, conv, caption, response)
	} else {
		_, _ = a.frontend.Send(ctx, msg.ChatID, result)
		a.spawnStore(ctx, conv, "[file upload]", result)
	}
}

// handlePhoto handles photo uploads — pass to chat LLM as images.
func (a *App) handlePhoto(ctx context.Context, msg oasis.IncomingMessage, conv oasis.Conversation) {
	text := msg.Caption
	if text == "" {
		text = "[photo]"
	}
	response := a.handleChatStream(ctx, msg.ChatID, text, conv)
	a.spawnStore(ctx, conv, text, response)
}

// handleURL ingests a URL into the knowledge base.
func (a *App) handleURL(ctx context.Context, msg oasis.IncomingMessage, conv oasis.Conversation, url string) {
	// Route URL ingestion through the action agent for full tool access
	a.spawnActionAgent(ctx, msg.ChatID, "Save this URL to the knowledge base: "+url, conv.ID, msg.ID)
}
