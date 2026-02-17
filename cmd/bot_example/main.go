package main

import (
	"context"
	"log"
	"os"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/frontend/telegram"
	"github.com/nevindra/oasis/internal/bot"
	"github.com/nevindra/oasis/internal/config"
	memsqlite "github.com/nevindra/oasis/memory/sqlite"
	"github.com/nevindra/oasis/observer"
	"github.com/nevindra/oasis/provider/gemini"
	"github.com/nevindra/oasis/store/sqlite"
	"github.com/nevindra/oasis/tools/file"
	httptool "github.com/nevindra/oasis/tools/http"
	"github.com/nevindra/oasis/tools/knowledge"
	"github.com/nevindra/oasis/tools/remember"
	"github.com/nevindra/oasis/tools/schedule"
	"github.com/nevindra/oasis/tools/search"
	"github.com/nevindra/oasis/tools/shell"
)

func main() {
	// 1. Load config
	cfg := config.Load(os.Getenv("OASIS_CONFIG"))

	// 2. Create providers
	var chatLLM oasis.Provider = gemini.New(cfg.LLM.APIKey, cfg.LLM.Model)
	var intentLLM oasis.Provider = gemini.New(cfg.Intent.APIKey, cfg.Intent.Model)
	var actionLLM oasis.Provider = gemini.New(cfg.Action.APIKey, cfg.Action.Model)
	var embedding oasis.EmbeddingProvider = gemini.NewEmbedding(cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimensions)

	// 3. Observer (opt-in via config)
	var inst *observer.Instruments
	if cfg.Observer.Enabled {
		pricing := make(map[string]observer.ModelPricing, len(cfg.Observer.Pricing))
		for model, p := range cfg.Observer.Pricing {
			pricing[model] = observer.ModelPricing{InputPerMillion: p.Input, OutputPerMillion: p.Output}
		}

		var shutdown func(context.Context) error
		var err error
		inst, shutdown, err = observer.Init(context.Background(), pricing)
		if err != nil {
			log.Fatalf(" [observer] init failed: %v", err)
		}
		defer shutdown(context.Background())

		chatLLM = observer.WrapProvider(chatLLM, cfg.LLM.Model, inst)
		intentLLM = observer.WrapProvider(intentLLM, cfg.Intent.Model, inst)
		actionLLM = observer.WrapProvider(actionLLM, cfg.Action.Model, inst)
		embedding = observer.WrapEmbedding(embedding, cfg.Embedding.Model, inst)

		log.Println(" [observer] OTEL observability enabled")
	}

	// 4. Create store + memory
	store := sqlite.New(cfg.Database.Path)
	memStore := memsqlite.New(cfg.Database.Path)

	// 5. Create app
	oasisApp := bot.New(&cfg, bot.Deps{
		Frontend:  telegram.New(cfg.Telegram.Token),
		ChatLLM:   chatLLM,
		IntentLLM: intentLLM,
		ActionLLM: actionLLM,
		Embedding: embedding,
		Store:     store,
		Memory:    memStore,
	})

	// 6. Register tools
	knowledgeTool := knowledge.New(store, embedding)
	oasisApp.AddTool(wrapTool(knowledgeTool, inst))

	scheduleTool := schedule.New(store, cfg.Brain.TimezoneOffset)
	oasisApp.AddTool(wrapTool(scheduleTool, inst))

	rememberTool := remember.New(store, embedding)
	oasisApp.AddTool(wrapTool(rememberTool, inst))
	oasisApp.SetIngestFile(rememberTool.IngestFile)

	shellTool := shell.New(cfg.Brain.WorkspacePath, 30)
	oasisApp.AddTool(wrapTool(shellTool, inst))

	fileTool := file.New(cfg.Brain.WorkspacePath)
	oasisApp.AddTool(wrapTool(fileTool, inst))

	httpTool := httptool.New()
	oasisApp.AddTool(wrapTool(httpTool, inst))

	if cfg.Search.BraveAPIKey != "" {
		searchTool := search.New(embedding, cfg.Search.BraveAPIKey)
		oasisApp.AddTool(wrapTool(searchTool, inst))
	}

	// 7. Run
	log.Fatal(oasisApp.RunWithSignal())
}

// wrapTool wraps a tool with observer instrumentation if inst is non-nil.
func wrapTool(t oasis.Tool, inst *observer.Instruments) oasis.Tool {
	if inst == nil {
		return t
	}
	return observer.WrapTool(t, inst)
}
