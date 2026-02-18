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
	memStore := memsqlite.New(cfg.Database.Path)

	// 5. Create frontend + input handler
	frontend := telegram.New(cfg.Telegram.Token)
	inputHandler := bot.NewTelegramInputHandler(frontend)

	// 6. Create tools
	tools := collectTools(cfg, store, embedding, inst)

	// 7. Build agents
	chatAgent := oasis.NewLLMAgent("chat", "Handle casual conversation, questions, and general chat", chatLLM,
		oasis.WithPrompt(chatPrompt(cfg)),
		oasis.WithConversationMemory(store),
		oasis.WithUserMemory(memStore),
		oasis.WithSemanticSearch(embedding),
	)

	actionAgent := oasis.NewLLMAgent("action", "Execute tasks using tools: search the web, manage schedules, save knowledge, read/write files, run commands", actionLLM,
		oasis.WithPrompt(actionPrompt()),
		oasis.WithTools(tools...),
		oasis.WithConversationMemory(store),
		oasis.WithUserMemory(memStore),
		oasis.WithSemanticSearch(embedding),
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
	app := bot.New(&cfg, frontend, network, store, memStore, inputHandler)
	log.Fatal(app.RunWithSignal())
}

// collectTools creates all tools with optional observer wrapping.
func collectTools(cfg config.Config, store oasis.Store, embedding oasis.EmbeddingProvider, inst *observer.Instruments) []oasis.Tool {
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

func chatPrompt(cfg config.Config) string {
	return `You are Oasis, a personal AI assistant. You are helpful, concise, and friendly.
Respond naturally in the same language the user uses.
If the user writes in Indonesian, respond in Indonesian. If in English, respond in English.`
}

func actionPrompt() string {
	return `You are Oasis, a personal AI assistant with tools. Use your tools to complete the user's request.

## Tool usage guidelines
- web_search: Use for general information lookup, quick answers, and finding URLs.
- knowledge_search: Search saved knowledge and past conversations.
- remember: Save information to the knowledge base for future reference.
- schedule_create/schedule_list/schedule_update/schedule_delete: Manage scheduled actions.
- shell_exec: Execute commands in the workspace directory.
- file_read/file_write: Read/write files in the workspace.
- http_fetch: Fetch and extract content from URLs.

Be concise in your final response. Respond in the same language as the user.`
}

func routerPrompt() string {
	return `You are a router for Oasis, a personal AI assistant. Your job is to decide which agent should handle the user's message.

You have two agents:
- agent_chat: For casual conversation, questions, opinions, recommendations, explanations, or anything you can answer from knowledge. Use this for: "what is X?", "recommend me Y", greetings, follow-up questions, casual talk.
- agent_action: For tasks that require using tools: searching the web, managing schedules, saving/searching knowledge, reading/writing files, running commands. Use this when the user wants to CREATE, UPDATE, DELETE, SEARCH, SCHEDULE, or MONITOR something.

Rules:
- If the user is asking a question or having conversation, route to agent_chat.
- If the user wants to perform an operation (search, save, schedule, file ops, etc.), route to agent_action.
- When in doubt, prefer agent_chat.
- Always delegate to exactly one agent per message. Pass the user's full message as the task.`
}
