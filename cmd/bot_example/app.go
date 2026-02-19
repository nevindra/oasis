package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/ingest"
	ingestpdf "github.com/nevindra/oasis/ingest/pdf"
)

// App is a thin orchestration layer that connects a Frontend to a StreamingAgent.
// All routing, tool-calling, memory, and conversation history are handled by
// the framework's agent primitives (Network, LLMAgent).
type App struct {
	frontend Frontend
	agent    oasis.StreamingAgent
	store    oasis.Store
	memory   oasis.MemoryStore
	config   *Config
	input    *TelegramInputHandler
	ingestor *ingest.Ingestor
}

// New creates an App.
func New(cfg *Config, frontend Frontend, agent oasis.StreamingAgent, store oasis.Store, memory oasis.MemoryStore, input *TelegramInputHandler, ingestor *ingest.Ingestor) *App {
	return &App{
		frontend: frontend,
		agent:    agent,
		store:    store,
		memory:   memory,
		config:   cfg,
		input:    input,
		ingestor: ingestor,
	}
}

// Run starts the application: init stores, poll messages, dispatch to agent.
func (a *App) Run(ctx context.Context) error {
	if err := a.store.Init(ctx); err != nil {
		return fmt.Errorf("store init: %w", err)
	}
	if a.memory != nil {
		if err := a.memory.Init(ctx); err != nil {
			return fmt.Errorf("memory init: %w", err)
		}
	}

	msgs, err := a.frontend.Poll(ctx)
	if err != nil {
		return fmt.Errorf("frontend poll: %w", err)
	}

	log.Println("oasis: app running")

	for {
		select {
		case <-ctx.Done():
			log.Println("oasis: shutting down")
			return ctx.Err()
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			go a.handle(ctx, msg)
		}
	}
}

// RunWithSignal wraps Run with OS signal handling for graceful shutdown.
func (a *App) RunWithSignal() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx)
}

// handle processes a single incoming message.
func (a *App) handle(ctx context.Context, msg IncomingMessage) {
	log.Printf(" [recv] from=%s chat=%s", msg.UserID, msg.ChatID)

	// Auth check
	if !a.isOwner(ctx, msg.UserID) {
		log.Printf(" [auth] DENIED user=%s", msg.UserID)
		return
	}

	// Reply routing: check if this is a reply to an agent's ask_user question
	if msg.ReplyToMsgID != "" && msg.Text != "" {
		if a.input != nil && a.input.RouteReply(msg.ReplyToMsgID, msg.Text) {
			log.Printf(" [agent] routed reply (reply_to=%s)", msg.ReplyToMsgID)
			return
		}
	}

	_ = a.frontend.SendTyping(ctx, msg.ChatID)

	// Download file/photo attachments before resolving thread.
	images, docs, fileText, attachErr := downloadAttachments(ctx, a.frontend, msg)
	if attachErr != nil {
		log.Printf(" [attach] download error: %v", attachErr)
	}

	// Auto-ingest documents into the knowledge store in the background.
	if len(docs) > 0 {
		go a.autoIngest(ctx, docs)
	}

	// Resolve thread
	thread, err := a.getOrCreateThread(ctx, msg.ChatID)
	if err != nil {
		log.Printf(" [thread] error: %v", err)
		return
	}

	// /new command
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	// Prepend extracted file text (for non-image documents).
	if fileText != "" {
		if text != "" {
			text = text + "\n\n" + fileText
		} else {
			text = fileText
		}
	}
	// Allow messages with only attachments through (no text required).
	if text == "" && len(images) == 0 {
		return
	}
	// Images with no text: use a neutral prompt so the LLM knows what to do.
	if text == "" {
		text = "Please analyze this."
	}

	if strings.TrimSpace(text) == "/new" {
		now := oasis.NowUnix()
		_ = a.store.CreateThread(ctx, oasis.Thread{
			ID: oasis.NewID(), ChatID: msg.ChatID,
			CreatedAt: now, UpdatedAt: now,
		})
		log.Println(" [cmd] /new")
		return
	}

	// Build task with context
	task := oasis.AgentTask{
		Input:       text,
		Attachments: images,
		Context: map[string]any{
			oasis.ContextThreadID: thread.ID,
			oasis.ContextUserID:   msg.UserID,
			oasis.ContextChatID:   msg.ChatID,
		},
	}

	// Send placeholder
	placeholderID, err := a.frontend.Send(ctx, msg.ChatID, "Thinking...")
	if err != nil {
		log.Printf(" [send] placeholder error: %v", err)
		return
	}

	// Stream response
	a.streamResponse(ctx, msg.ChatID, placeholderID, task)
}

// autoIngest extracts text from uploaded documents and saves them to the knowledge store.
// PDFs are extracted via the PDF extractor; other files are treated as plain text.
// Runs in a goroutine — errors are logged and do not affect the main response flow.
func (a *App) autoIngest(ctx context.Context, docs []RawDoc) {
	pdfExtractor := ingestpdf.NewExtractor()
	for _, doc := range docs {
		var text string
		var err error
		if doc.MimeType == "application/pdf" {
			text, err = pdfExtractor.Extract(doc.Data)
			if err != nil {
				log.Printf(" [ingest] PDF extract error (%s): %v", doc.Filename, err)
				continue
			}
		} else {
			text = string(doc.Data)
		}
		if text == "" {
			continue
		}
		if _, err := a.ingestor.IngestText(ctx, text, doc.Filename, doc.Filename); err != nil {
			log.Printf(" [ingest] error (%s): %v", doc.Filename, err)
		} else {
			log.Printf(" [ingest] saved %q to knowledge store", doc.Filename)
		}
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

// streamResponse runs ExecuteStream and pipes events to a Telegram message with periodic edits.
func (a *App) streamResponse(ctx context.Context, chatID, placeholderID string, task oasis.AgentTask) {
	ch := make(chan oasis.StreamEvent, 64)

	type execResult struct {
		result oasis.AgentResult
		err    error
	}
	resultCh := make(chan execResult, 1)

	// Inject chat_id into context for InputHandler.
	agentCtx := WithChatID(ctx, chatID)

	go func() {
		r, err := a.agent.ExecuteStream(agentCtx, task, ch)
		resultCh <- execResult{r, err}
	}()

	// Consume stream, edit Telegram message periodically
	a.streamToTelegram(ctx, chatID, placeholderID, ch)

	// Check for errors
	res := <-resultCh
	if res.err != nil {
		log.Printf(" [agent] error: %v", res.err)
		_ = a.frontend.Edit(ctx, chatID, placeholderID, "Sorry, something went wrong. Please try again.")
		return
	}
	log.Printf(" [agent] done, %d input + %d output tokens", res.result.Usage.InputTokens, res.result.Usage.OutputTokens)
}
