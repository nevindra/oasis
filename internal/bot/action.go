package bot

import (
	"context"
	"encoding/json"
	"log"
	"time"

	oasis "github.com/nevindra/oasis"
)

const maxToolIterations = 10

// ask_user tool definition injected alongside registry tools.
var askUserDefinition = oasis.ToolDefinition{
	Name:        "ask_user",
	Description: "Ask the user a clarifying question when you need more information to proceed.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"question":{"type":"string","description":"The question to ask the user"}},"required":["question"]}`),
}

// spawnActionAgent creates and launches an action agent, or enqueues if slots are full.
func (a *App) spawnActionAgent(ctx context.Context, chatID, text, threadID, originalMsgID string) {
	if !a.agents.SlotsAvailable() {
		log.Println(" [agent] slots full, enqueuing")
		a.agents.Enqueue(QueuedAction{
			ChatID:        chatID,
			Text:          text,
			ThreadID:      threadID,
			OriginalMsgID: originalMsgID,
		})
		_, _ = a.frontend.Send(ctx, chatID, "Queued — will run when a slot opens.")
		return
	}

	a.launchAgent(ctx, chatID, text, threadID, originalMsgID)
}

// launchAgent generates ack + launches the agent goroutine.
func (a *App) launchAgent(ctx context.Context, chatID, text, threadID, originalMsgID string) {
	// Generate ack and label via intent LLM
	ackText, description := a.generateAckAndLabel(ctx, text)

	ackMsgID, err := a.frontend.Send(ctx, chatID, ackText)
	if err != nil {
		log.Printf(" [agent] failed to send ack: %v", err)
		return
	}

	agentID := oasis.NewID()
	agent := &ActionAgent{
		ID:            agentID,
		ChatID:        chatID,
		Description:   description,
		Status:        AgentRunning,
		StartedAt:     time.Now(),
		InputCh:       make(chan string, 1),
		OriginalMsgID: originalMsgID,
		AckMsgID:      ackMsgID,
	}
	a.agents.Register(agent)

	go func() {
		log.Printf(" [agent:%s] started", agentID)

		response, err := a.runActionLoop(ctx, chatID, text, threadID, agentID, ackMsgID, originalMsgID, agent.InputCh)

		if err != nil {
			log.Printf(" [agent:%s] error: %v", agentID, err)
			_, _ = a.frontend.Send(ctx, chatID, "Sorry, something went wrong.")
		} else {
			thread, _ := a.getOrCreateThread(ctx, chatID)
			a.spawnStore(ctx, thread, text, response)
		}

		a.agents.Remove(agentID)
		log.Printf(" [agent:%s] done, removed", agentID)

		// Try to dequeue next action
		if queued, ok := a.agents.TryDequeue(); ok {
			log.Println(" [agent] dequeuing action from queue")
			a.launchAgent(ctx, queued.ChatID, queued.Text, queued.ThreadID, queued.OriginalMsgID)
		}
	}()
}

// ackLabelSchema is the JSON Schema for ack + label responses.
var ackLabelSchema = &oasis.ResponseSchema{
	Name:   "ack_and_label",
	Schema: json.RawMessage(`{"type":"object","properties":{"ack":{"type":"string"},"label":{"type":"string"}},"required":["ack","label"]}`),
}

// generateAckAndLabel creates a brief ack + short label from the user's request.
func (a *App) generateAckAndLabel(ctx context.Context, userMessage string) (string, string) {
	system := `You are a casual personal assistant. The user just asked you to do something (search, create a task, etc).

Return a JSON object with two fields:
- "ack": A brief, casual acknowledgment (1 sentence, max 20 words) in the SAME language as the user. Do NOT do the task — just acknowledge you'll work on it. No emojis.
- "label": A short task label (3-6 words, in English) summarizing what the agent will do. Examples: "Search CS:GO tournaments", "Create grocery task", "Find flight prices".

Respond with ONLY the JSON object, no extra text.`

	req := oasis.ChatRequest{
		Messages: []oasis.ChatMessage{
			oasis.SystemMessage(system),
			oasis.UserMessage(userMessage),
		},
		ResponseSchema: ackLabelSchema,
	}

	fallbackLabel := userMessage
	if len(fallbackLabel) > 40 {
		fallbackLabel = fallbackLabel[:40]
	}

	resp, err := a.intentLLM.Chat(ctx, req)
	if err != nil {
		return "On it...", fallbackLabel
	}

	content := extractJSON(resp.Content)
	var parsed struct {
		Ack   string `json:"ack"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return resp.Content, fallbackLabel
	}

	ack := parsed.Ack
	if ack == "" {
		ack = "On it..."
	}
	label := parsed.Label
	if label == "" {
		label = fallbackLabel
	}
	return ack, label
}

// runActionLoop runs the tool-calling loop for an action agent.
func (a *App) runActionLoop(
	ctx context.Context,
	chatID, text, threadID, agentID, ackMsgID, originalMsgID string,
	inputCh <-chan string,
) (string, error) {
	// Build memory context
	memoryContext := ""
	if a.memory != nil && a.embedding != nil {
		embs, err := a.embedding.Embed(ctx, []string{text})
		if err == nil && len(embs) > 0 {
			mc, _ := a.memory.BuildContext(ctx, embs[0])
			memoryContext = mc
		}
	}

	thread, _ := a.getOrCreateThread(ctx, chatID)
	messages := a.buildSystemPrompt(ctx, memoryContext, thread)

	// Add tool usage guidelines
	if len(messages) > 0 {
		messages[0].Content += `
## Tool usage guidelines
- **web_search**: Use for general information lookup, quick answers, and finding URLs.
- **ask_user**: Use when you need clarification from the user before proceeding.
- **shell_exec**: Execute commands in the workspace directory.
- **file_read/file_write**: Read/write files in the workspace.
- **schedule_***: Create, list, update, or delete scheduled actions.
- **remember**: Save information to the knowledge base.
- **knowledge_search**: Search saved knowledge and past conversations.
`
	}

	messages = append(messages, oasis.UserMessage(text))

	// Tool definitions: registry tools + ask_user
	toolDefs := a.tools.AllDefinitions()
	toolDefs = append(toolDefs, askUserDefinition)

	var finalText string

	for iteration := 0; iteration < maxToolIterations; iteration++ {
		log.Printf(" [agent:%s] iteration %d/%d", agentID, iteration+1, maxToolIterations)

		req := oasis.ChatRequest{Messages: messages}
		resp, err := a.actionLLM.ChatWithTools(ctx, req, toolDefs)
		if err != nil {
			log.Printf(" [agent:%s] LLM error: %v", agentID, err)
			finalText = "Sorry, something went wrong. Please try again."
			break
		}

		// No tool calls — final text
		if len(resp.ToolCalls) == 0 {
			finalText = resp.Content
			break
		}

		// Add assistant message with tool calls
		assistantMsg := oasis.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// Execute each tool call
		var lastOutput string
		for _, tc := range resp.ToolCalls {
			if tc.Name == "ask_user" {
				// Handle ask_user
				lastOutput = a.handleAskUser(ctx, chatID, agentID, originalMsgID, tc, inputCh)
				messages = append(messages, oasis.ToolResultMessage(tc.ID, lastOutput))
			} else {
				// Regular tool execution
				log.Printf(" [tool] %s(%s)", tc.Name, string(tc.Args))
				result, execErr := a.tools.Execute(ctx, tc.Name, tc.Args)
				content := result.Content
				if execErr != nil {
					content = "error: " + execErr.Error()
				} else if result.Error != "" {
					content = "error: " + result.Error
				}
				log.Printf(" [tool] %s -> %d chars", tc.Name, len(content))
				lastOutput = content
				messages = append(messages, oasis.ToolResultMessage(tc.ID, content))
			}
		}

		// Short-circuit for single simple tools
		if len(resp.ToolCalls) == 1 && resp.ToolCalls[0].Name != "ask_user" && !isErrorOutput(lastOutput) {
			if isSimpleTool(resp.ToolCalls[0].Name) {
				log.Printf(" [agent:%s] short-circuit: simple tool", agentID)
				finalText = lastOutput
				break
			}
		}
	}

	// Force synthesis if empty
	if finalText == "" {
		log.Printf(" [agent:%s] forcing final response (max iterations)", agentID)
		messages = append(messages, oasis.UserMessage(
			"You have used all available tool calls. Now summarize what you found and respond to the user. "+
				"If you found useful information, present it clearly. If you could not complete the task, explain what happened."))
		req := oasis.ChatRequest{Messages: messages}
		resp, err := a.actionLLM.ChatWithTools(ctx, req, nil)
		if err == nil {
			finalText = resp.Content
		} else {
			finalText = "Sorry, something went wrong."
		}
	}

	if finalText == "" {
		finalText = "Done."
	}

	// Send result
	_, _ = a.frontend.Send(ctx, chatID, finalText)
	// Update ack message to "Done"
	_ = a.frontend.Edit(ctx, chatID, ackMsgID, "Done.")

	log.Printf(" [agent:%s] sent %d chars (action)", agentID, len(finalText))
	return finalText, nil
}

// handleAskUser sends a question to the user and waits for a reply.
func (a *App) handleAskUser(ctx context.Context, chatID, agentID, originalMsgID string, tc oasis.ToolCall, inputCh <-chan string) string {
	var params struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal(tc.Args, &params)
	if params.Question == "" {
		params.Question = "Could you clarify?"
	}

	log.Printf(" [agent:%s] ask_user: %s", agentID, params.Question)

	botMsgID, err := a.frontend.Send(ctx, chatID, params.Question)
	if err != nil {
		return "Error: failed to send question to user."
	}

	// Register for reply routing
	a.agents.RegisterMessage(botMsgID, agentID)
	a.agents.SetStatus(agentID, AgentWaitingForInput)

	// Wait for reply with 5-minute timeout
	select {
	case reply := <-inputCh:
		a.agents.SetStatus(agentID, AgentRunning)
		log.Printf(" [agent:%s] got user reply: %s", agentID, truncate(reply, 80))
		return "User replied: " + reply
	case <-time.After(5 * time.Minute):
		a.agents.SetStatus(agentID, AgentRunning)
		log.Printf(" [agent:%s] ask_user timed out", agentID)
		return "User did not respond within 5 minutes. Proceed with your best judgment."
	case <-ctx.Done():
		return "Operation cancelled."
	}
}

func isSimpleTool(name string) bool {
	switch name {
	case "remember", "schedule_create", "schedule_list", "schedule_update", "schedule_delete":
		return true
	}
	return false
}

func isErrorOutput(output string) bool {
	return len(output) > 6 && (output[:6] == "Error:" || output[:6] == "error:")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
