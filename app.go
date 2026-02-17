package oasis

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// App is the core orchestrator that connects a Frontend, Provider, Store, and Tools.
type App struct {
	frontend     Frontend
	provider     Provider
	embedding    EmbeddingProvider
	store        Store
	memory       MemoryStore // nil if not configured
	tools        *ToolRegistry
	systemPrompt string
	maxIter      int
}

// Option configures an App.
type Option func(*App)

func WithFrontend(f Frontend) Option         { return func(a *App) { a.frontend = f } }
func WithProvider(p Provider) Option         { return func(a *App) { a.provider = p } }
func WithEmbedding(e EmbeddingProvider) Option { return func(a *App) { a.embedding = e } }
func WithStore(s Store) Option               { return func(a *App) { a.store = s } }
func WithMemory(m MemoryStore) Option        { return func(a *App) { a.memory = m } }
func WithSystemPrompt(s string) Option       { return func(a *App) { a.systemPrompt = s } }
func WithMaxToolIterations(n int) Option     { return func(a *App) { a.maxIter = n } }

// New creates an App with the given options.
func New(opts ...Option) *App {
	a := &App{
		tools:   NewToolRegistry(),
		maxIter: 10,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// AddTool registers a tool with the app.
func (a *App) AddTool(t Tool) {
	a.tools.Add(t)
}

// Store returns the app's Store (for tools that need it).
func (a *App) Store() Store {
	return a.store
}

// Embedding returns the app's EmbeddingProvider (for tools that need it).
func (a *App) Embedding() EmbeddingProvider {
	return a.embedding
}

// Run starts the app's main loop: poll frontend, handle messages.
func (a *App) Run(ctx context.Context) error {
	if a.frontend == nil || a.provider == nil || a.store == nil {
		return fmt.Errorf("app requires Frontend, Provider, and Store")
	}

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
			return ctx.Err()
		case msg, ok := <-msgs:
			if !ok {
				return nil
			}
			go a.handleMessage(ctx, msg)
		}
	}
}

// handleMessage processes a single incoming message.
func (a *App) handleMessage(ctx context.Context, msg IncomingMessage) {
	if msg.Text == "" && msg.Document == nil && len(msg.Photos) == 0 {
		return
	}

	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	if text == "" {
		return
	}

	thread, err := a.getOrCreateThread(ctx, msg.ChatID)
	if err != nil {
		log.Printf("oasis: get thread: %v", err)
		return
	}

	a.handleAction(ctx, msg, thread, text)
}

// getOrCreateThread returns the most recent thread for a chatID, or creates one.
func (a *App) getOrCreateThread(ctx context.Context, chatID string) (Thread, error) {
	threads, err := a.store.ListThreads(ctx, chatID, 1)
	if err != nil {
		return Thread{}, err
	}
	if len(threads) > 0 {
		return threads[0], nil
	}
	thread := Thread{
		ID:        NewID(),
		ChatID:    chatID,
		CreatedAt: NowUnix(),
		UpdatedAt: NowUnix(),
	}
	if err := a.store.CreateThread(ctx, thread); err != nil {
		return Thread{}, err
	}
	return thread, nil
}

// handleAction runs the tool-calling loop.
func (a *App) handleAction(ctx context.Context, msg IncomingMessage, thread Thread, userText string) {
	// Build messages: system + history + user
	messages := a.buildMessages(ctx, thread, userText)

	// Send placeholder
	placeholderID, err := a.frontend.Send(ctx, msg.ChatID, "...")
	if err != nil {
		log.Printf("oasis: send placeholder: %v", err)
		return
	}

	toolDefs := a.tools.AllDefinitions()

	for i := 0; i < a.maxIter; i++ {
		var resp ChatResponse
		var callErr error

		if len(toolDefs) > 0 {
			resp, callErr = a.provider.ChatWithTools(ctx, ChatRequest{Messages: messages}, toolDefs)
		} else {
			resp, callErr = a.provider.Chat(ctx, ChatRequest{Messages: messages})
		}
		if callErr != nil {
			log.Printf("oasis: llm call: %v", callErr)
			_ = a.frontend.Edit(ctx, msg.ChatID, placeholderID, "Error: "+callErr.Error())
			return
		}

		// No tool calls — final text response
		if len(resp.ToolCalls) == 0 {
			_ = a.frontend.EditFormatted(ctx, msg.ChatID, placeholderID, resp.Content)
			a.spawnStore(ctx, thread, userText, resp.Content)
			return
		}

		// Execute tool calls
		messages = append(messages, ChatMessage{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
			Content:   resp.Content,
		})

		for _, tc := range resp.ToolCalls {
			_ = a.frontend.Edit(ctx, msg.ChatID, placeholderID, fmt.Sprintf("Using %s...", tc.Name))
			result, execErr := a.tools.Execute(ctx, tc.Name, tc.Args)
			content := result.Content
			if execErr != nil {
				content = "error: " + execErr.Error()
			} else if result.Error != "" {
				content = "error: " + result.Error
			}
			messages = append(messages, ToolResultMessage(tc.ID, content))
		}
	}

	// Max iterations — send what we have
	_ = a.frontend.Edit(ctx, msg.ChatID, placeholderID, "Reached maximum tool iterations.")
}

// buildMessages constructs the message list: system prompt + memory + history + user message.
func (a *App) buildMessages(ctx context.Context, thread Thread, userText string) []ChatMessage {
	var messages []ChatMessage

	// System prompt
	var systemParts []string
	if a.systemPrompt != "" {
		systemParts = append(systemParts, a.systemPrompt)
	}
	systemParts = append(systemParts, fmt.Sprintf("Current time: %s", time.Now().Format(time.RFC3339)))

	// Memory context
	if a.memory != nil && a.embedding != nil {
		embs, err := a.embedding.Embed(ctx, []string{userText})
		if err == nil && len(embs) > 0 {
			memCtx, err := a.memory.BuildContext(ctx, embs[0])
			if err == nil && memCtx != "" {
				systemParts = append(systemParts, memCtx)
			}
		}
	}

	messages = append(messages, SystemMessage(strings.Join(systemParts, "\n\n")))

	// Thread history
	history, _ := a.store.GetMessages(ctx, thread.ID, 20)
	for _, m := range history {
		messages = append(messages, ChatMessage{Role: m.Role, Content: m.Content})
	}

	// Current user message
	messages = append(messages, UserMessage(userText))
	return messages
}

// spawnStore persists messages and extracts facts in the background.
func (a *App) spawnStore(ctx context.Context, thread Thread, userText, assistantText string) {
	go func() {
		// Store user message
		userMsg := Message{
			ID: NewID(), ThreadID: thread.ID,
			Role: "user", Content: userText, CreatedAt: NowUnix(),
		}
		_ = a.store.StoreMessage(ctx, userMsg)

		// Embed user message
		if a.embedding != nil {
			embs, err := a.embedding.Embed(ctx, []string{userText})
			if err == nil && len(embs) > 0 {
				userMsg.Embedding = embs[0]
				_ = a.store.StoreMessage(ctx, userMsg)
			}
		}

		// Store assistant message
		asstMsg := Message{
			ID: NewID(), ThreadID: thread.ID,
			Role: "assistant", Content: assistantText, CreatedAt: NowUnix(),
		}
		_ = a.store.StoreMessage(ctx, asstMsg)
	}()
}
