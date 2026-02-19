package main

import (
	"context"
	"log"
	"sync"
	"time"

	oasis "github.com/nevindra/oasis"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const chatIDKey contextKey = "bot_chat_id"

// WithChatID adds a chat ID to the context for the InputHandler.
func WithChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, chatIDKey, chatID)
}

// chatIDFromContext extracts the chat ID set by WithChatID.
func chatIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(chatIDKey).(string); ok {
		return v
	}
	return ""
}

// TelegramInputHandler implements oasis.InputHandler by sending questions
// as Telegram messages and waiting for user replies.
type TelegramInputHandler struct {
	frontend Frontend
	mu       sync.Mutex
	pending  map[string]chan string // bot message ID -> reply channel
}

// NewTelegramInputHandler creates a new InputHandler for Telegram.
func NewTelegramInputHandler(frontend Frontend) *TelegramInputHandler {
	return &TelegramInputHandler{
		frontend: frontend,
		pending:  make(map[string]chan string),
	}
}

// RequestInput sends a question to the user via Telegram and waits for their reply.
func (h *TelegramInputHandler) RequestInput(ctx context.Context, req oasis.InputRequest) (oasis.InputResponse, error) {
	chatID := chatIDFromContext(ctx)
	if chatID == "" {
		return oasis.InputResponse{Value: "No chat context available."}, nil
	}

	question := req.Question
	if len(req.Options) > 0 {
		question += "\n\nOptions: " + joinOptions(req.Options)
	}

	msgID, err := h.frontend.Send(ctx, chatID, question)
	if err != nil {
		return oasis.InputResponse{}, err
	}

	// Register a channel for this message
	replyCh := make(chan string, 1)
	h.mu.Lock()
	h.pending[msgID] = replyCh
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pending, msgID)
		h.mu.Unlock()
	}()

	// Wait for reply with 5-minute timeout
	select {
	case reply := <-replyCh:
		log.Printf(" [input] got reply: %s", truncate(reply, 80))
		return oasis.InputResponse{Value: reply}, nil
	case <-time.After(5 * time.Minute):
		log.Printf(" [input] timed out waiting for reply")
		return oasis.InputResponse{Value: "User did not respond within 5 minutes. Proceed with your best judgment."}, nil
	case <-ctx.Done():
		return oasis.InputResponse{}, ctx.Err()
	}
}

// RouteReply routes a user reply to a waiting InputHandler request.
// Returns true if the reply was routed.
func (h *TelegramInputHandler) RouteReply(replyToMsgID, text string) bool {
	h.mu.Lock()
	ch, ok := h.pending[replyToMsgID]
	h.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case ch <- text:
		return true
	default:
		return false
	}
}

func joinOptions(opts []string) string {
	result := ""
	for i, opt := range opts {
		if i > 0 {
			result += ", "
		}
		result += opt
	}
	return result
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
