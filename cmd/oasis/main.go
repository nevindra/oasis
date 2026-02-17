package main

import (
	"context"
	"log"
	"os"

	"github.com/nevindra/oasis/frontend/telegram"
	"github.com/nevindra/oasis/internal/bot"
	"github.com/nevindra/oasis/internal/config"
	"github.com/nevindra/oasis/internal/scheduling"
	memsqlite "github.com/nevindra/oasis/memory/sqlite"
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
	chatLLM := gemini.New(cfg.LLM.APIKey, cfg.LLM.Model)
	intentLLM := gemini.New(cfg.Intent.APIKey, cfg.Intent.Model)
	actionLLM := gemini.New(cfg.Action.APIKey, cfg.Action.Model)
	embedding := gemini.NewEmbedding(cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimensions)

	// 3. Create store + memory
	store := sqlite.New(cfg.Database.Path)
	memStore := memsqlite.New(cfg.Database.Path)

	// 4. Create app
	oasisApp := bot.New(&cfg, bot.Deps{
		Frontend:  telegram.New(cfg.Telegram.Token),
		ChatLLM:   chatLLM,
		IntentLLM: intentLLM,
		ActionLLM: actionLLM,
		Embedding: embedding,
		Store:     store,
		Memory:    memStore,
	})

	// 5. Register tools
	knowledgeTool := knowledge.New(store, embedding)
	oasisApp.AddTool(knowledgeTool)

	scheduleTool := schedule.New(store, cfg.Brain.TimezoneOffset)
	oasisApp.AddTool(scheduleTool)

	rememberTool := remember.New(store, embedding)
	oasisApp.AddTool(rememberTool)
	oasisApp.SetIngestFile(rememberTool.IngestFile)

	shellTool := shell.New(cfg.Brain.WorkspacePath, 30)
	oasisApp.AddTool(shellTool)

	fileTool := file.New(cfg.Brain.WorkspacePath)
	oasisApp.AddTool(fileTool)

	httpTool := httptool.New()
	oasisApp.AddTool(httpTool)

	if cfg.Search.BraveAPIKey != "" {
		searchTool := search.New(embedding, cfg.Search.BraveAPIKey)
		oasisApp.AddTool(searchTool)
	}

	// 6. Start scheduler in background
	sched := scheduling.New(store, oasisApp.Tools(), oasisApp.Frontend(), intentLLM, cfg.Brain.TimezoneOffset)
	go sched.Run(context.Background())

	// 7. Run
	log.Fatal(oasisApp.RunWithSignal())
}
