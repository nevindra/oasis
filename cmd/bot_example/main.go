package main

import (
	"context"
	"log"
	"os"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/ingest"
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
	cfg := LoadConfig(os.Getenv("OASIS_CONFIG"))

	// 2. Create providers
	var routerLLM oasis.Provider = gemini.New(cfg.Intent.APIKey, cfg.Intent.Model)
	var chatLLM oasis.Provider = gemini.New(cfg.LLM.APIKey, cfg.LLM.Model)
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

		routerLLM = observer.WrapProvider(routerLLM, cfg.Intent.Model, inst)
		chatLLM = observer.WrapProvider(chatLLM, cfg.LLM.Model, inst)
		actionLLM = observer.WrapProvider(actionLLM, cfg.Action.Model, inst)
		embedding = observer.WrapEmbedding(embedding, cfg.Embedding.Model, inst)

		log.Println(" [observer] OTEL observability enabled")
	}

	// 4. Create store + memory
	store := sqlite.New(cfg.Database.Path)
	memStore := sqlite.NewMemoryStore(store.DB())

	// 5. Create frontend + input handler
	frontend := NewBot(cfg.Telegram.Token)
	inputHandler := NewTelegramInputHandler(frontend)

	// 6. Create tools
	tools := collectTools(cfg, store, embedding, inst)

	// 7. Build agents
	clock := newClockPreProcessor(cfg.Brain.TimezoneOffset)

	chatAgent := oasis.NewLLMAgent("chat", "Handle casual conversation, questions, and general chat", chatLLM,
		oasis.WithPrompt(chatPrompt(cfg)),
		oasis.WithTools(wrapTool(knowledge.New(store, embedding), inst)),
		oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding)),
		oasis.WithUserMemory(memStore, embedding),
		oasis.WithProcessors(clock),
	)

	actionAgent := oasis.NewLLMAgent("action", "Execute tasks using tools: search the web, manage schedules, save knowledge, read/write files, run commands", actionLLM,
		oasis.WithPrompt(actionPrompt()),
		oasis.WithTools(tools...),
		oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding)),
		oasis.WithUserMemory(memStore, embedding),
		oasis.WithProcessors(clock),
	)

	network := oasis.NewNetwork("oasis", "AI personal assistant", routerLLM,
		oasis.WithAgents(chatAgent, actionAgent),
		oasis.WithPrompt(routerPrompt()),
		oasis.WithInputHandler(inputHandler),
		oasis.WithMaxIter(15),
	)

	// 8. Run
	// Note: observer wrapping is on individual providers/tools.
	// ObservedAgent doesn't support StreamingAgent yet.
	ingestor := ingest.NewIngestor(store, embedding,
		ingest.WithExtractor(ingest.TypePDF, ingest.NewPDFExtractor()),
	)
	app := New(&cfg, frontend, network, store, memStore, inputHandler, ingestor)
	log.Fatal(app.RunWithSignal())
}

// collectTools creates all tools with optional observer wrapping.
func collectTools(cfg Config, store oasis.Store, embedding oasis.EmbeddingProvider, inst *observer.Instruments) []oasis.Tool {
	var tools []oasis.Tool

	tools = append(tools, wrapTool(knowledge.New(store, embedding), inst))
	tools = append(tools, wrapTool(schedule.New(store, cfg.Brain.TimezoneOffset), inst))
	tools = append(tools, wrapTool(remember.New(store, embedding), inst))
	tools = append(tools, wrapTool(shell.New(cfg.Brain.WorkspacePath, 30), inst))
	tools = append(tools, wrapTool(file.New(cfg.Brain.WorkspacePath), inst))
	tools = append(tools, wrapTool(httptool.New(), inst))

	if cfg.Search.BraveAPIKey != "" {
		tools = append(tools, wrapTool(search.New(embedding, cfg.Search.BraveAPIKey), inst))
	}

	return tools
}

// wrapTool wraps a tool with observer instrumentation if inst is non-nil.
func wrapTool(t oasis.Tool, inst *observer.Instruments) oasis.Tool {
	if inst == nil {
		return t
	}
	return observer.WrapTool(t, inst)
}

// --- System Prompts ---

func chatPrompt(cfg Config) string {
	return `You are Oasis, a personal AI assistant.

## Personality
Be warm, clear, and direct. You're here to genuinely help — not to impress. Skip filler phrases like "Great question!", "Certainly!", "Of course!", or "Ada lagi yang bisa dibantu?". Just respond naturally. If something is unclear, ask once and briefly.

## Memory & context
- Conversation history and known facts about the user are injected automatically before each message.
- Use that context to give relevant, personalized responses.
- If the user asks about personal details (name, job, skills, preferences), use knowledge_search to look it up first — don't guess.
- If you don't have enough information, say so directly.
- Never invent context. If the user says "thanks", just respond to that — don't assume why they're thanking you or add details that weren't mentioned.

## Tools
- knowledge_search: Search the user's personal knowledge base and past conversations.

## Attachments
- If the user sends a file or photo, its content is included in the message — read and analyze it directly.

## Language
Always respond in the same language the user used. Indonesian → Indonesian, English → English. Never switch unless the user does first.`
}

func actionPrompt() string {
	return `You are Oasis, a personal AI assistant. Get things done for the user using your tools.

## Tools
- web_search — look up information, find URLs, get current news
- knowledge_search — search saved knowledge and past conversations
- remember — save information to the knowledge base
- schedule_create / schedule_list / schedule_update / schedule_delete — manage reminders and scheduled tasks
- shell_exec — run commands in the workspace
- file_read / file_write — read or write files in the workspace
- http_fetch — fetch content from a URL

## Guidelines
- Pick the right tool and use it — don't ask for permission unless truly necessary.
- Report what was done, not every step. Keep the response short.

Always respond in the same language the user used.`
}

func routerPrompt() string {
	return `You are the routing layer for Oasis. Delegate each message to the right agent and return their response as-is.

## Agents
- agent_chat — conversation, questions, opinions, recommendations, analysis of files or photos, anything answerable from knowledge
- agent_action — tasks requiring tools: web search, schedules, file operations, running commands, fetching URLs

## Rules
- When in doubt, route to agent_chat.
- Pass the user's original message unchanged — do not paraphrase, translate, or summarize.
- Return the agent's response verbatim — no commentary, no mention of which agent handled it.`
}
