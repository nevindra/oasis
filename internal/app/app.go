package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/internal/config"
)

// Deps holds injected dependencies for the App.
type Deps struct {
	Frontend  oasis.Frontend
	ChatLLM   oasis.Provider
	IntentLLM oasis.Provider
	ActionLLM oasis.Provider
	Embedding oasis.EmbeddingProvider
	Store     oasis.VectorStore
	Memory    oasis.MemoryStore
}

// App is the Oasis application, building Oasis-specific orchestration
// on top of the generic Agent framework interfaces.
type App struct {
	frontend  oasis.Frontend
	chatLLM   oasis.Provider
	intentLLM oasis.Provider
	actionLLM oasis.Provider
	embedding oasis.EmbeddingProvider
	store     oasis.VectorStore
	memory    oasis.MemoryStore
	tools     *oasis.ToolRegistry
	agents    *AgentManager
	config    *config.Config

	// Optional concrete references for specialized operations.
	// ingestFile is a function that ingests file content into the knowledge base.
	ingestFile func(ctx context.Context, content, filename string) (string, error)
}

// New creates an Oasis App.
func New(cfg *config.Config, deps Deps) *App {
	return &App{
		frontend:  deps.Frontend,
		chatLLM:   deps.ChatLLM,
		intentLLM: deps.IntentLLM,
		actionLLM: deps.ActionLLM,
		embedding: deps.Embedding,
		store:     deps.Store,
		memory:    deps.Memory,
		tools:     oasis.NewToolRegistry(),
		agents:    NewAgentManager(3),
		config:    cfg,
	}
}

// AddTool registers a tool with the app.
func (a *App) AddTool(t oasis.Tool) {
	a.tools.Add(t)
}

// SetIngestFile sets the file ingestion function (provided by remember tool).
func (a *App) SetIngestFile(fn func(ctx context.Context, content, filename string) (string, error)) {
	a.ingestFile = fn
}

// Tools returns the tool registry (for scheduler use).
func (a *App) Tools() *oasis.ToolRegistry { return a.tools }

// Frontend returns the frontend (for scheduler use).
func (a *App) Frontend() oasis.Frontend { return a.frontend }

// Run starts the application: init stores, start scheduler, poll messages.
func (a *App) Run(ctx context.Context) error {
	// Init stores
	if err := a.store.Init(ctx); err != nil {
		return fmt.Errorf("store init: %w", err)
	}
	if a.memory != nil {
		if err := a.memory.Init(ctx); err != nil {
			return fmt.Errorf("memory init: %w", err)
		}
	}

	// Start polling
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
			go a.route(ctx, msg)
		}
	}
}

// RunWithSignal wraps Run with OS signal handling for graceful shutdown.
func (a *App) RunWithSignal() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx)
}
