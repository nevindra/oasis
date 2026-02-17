package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/frontend/telegram"
	"github.com/nevindra/oasis/provider/gemini"
	"github.com/nevindra/oasis/store/sqlite"
	"github.com/nevindra/oasis/tools/knowledge"
)

func main() {
	apiKey := os.Getenv("OASIS_LLM_API_KEY")
	tgToken := os.Getenv("OASIS_TELEGRAM_TOKEN")
	dbPath := os.Getenv("OASIS_DB_PATH")
	if dbPath == "" {
		dbPath = "oasis.db"
	}

	if apiKey == "" || tgToken == "" {
		log.Fatal("OASIS_LLM_API_KEY and OASIS_TELEGRAM_TOKEN are required")
	}

	emb := gemini.NewEmbedding(apiKey, "gemini-embedding-001", 1536)

	agent := oasis.New(
		oasis.WithProvider(gemini.New(apiKey, "gemini-2.5-flash-preview-05-20")),
		oasis.WithEmbedding(emb),
		oasis.WithFrontend(telegram.New(tgToken)),
		oasis.WithStore(sqlite.New(dbPath)),
		oasis.WithSystemPrompt("You are Oasis, a helpful personal AI assistant. Respond concisely."),
	)

	agent.AddTool(knowledge.New(agent.Store(), emb))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := agent.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
