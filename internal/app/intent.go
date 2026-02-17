package app

import (
	"context"
	"encoding/json"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// IntentSystemPrompt is sent to the intent LLM for classification.
const IntentSystemPrompt = `You are an intent classifier for a personal assistant. Classify the user message into exactly one of two intents.

Return a JSON object with a single "intent" field:

1. **chat** — Conversation, questions, opinions, recommendations, explanations, or anything the assistant can answer from its own knowledge. This includes: "what is X?", "recommend me Y", "what do you think about Z?", "tell me about...", follow-up questions, casual talk, greetings.
   Return: ` + "`{\"intent\":\"chat\"}`" + `

2. **action** — The user wants to CREATE, UPDATE, DELETE, SEARCH, SCHEDULE, or MONITOR something using a tool. This includes:
   - Search: "cari di internet ...", "cari di knowledge base"
   - Reminders and scheduling: "ingatkan aku ...", "cek lagi nanti ...", "tolong pantau ...", "kabari kalau ...", "remind me ...", "check later ..."
   - Any request that implies a deferred or future action the assistant should perform
   Return: ` + "`{\"intent\":\"action\"}`" + `

## Rules
- If the user is asking a question or having a conversation, it's CHAT — even if the topic involves books, schedules, etc.
- Action is when the user wants to PERFORM an operation (create, update, delete, search, save, schedule, monitor).
- Requests to do something later or check on something in the future are ACTION, not CHAT.
- If in doubt, prefer CHAT.
- Respond with ONLY the JSON object, no extra text.
- The user may write in English or Indonesian.`

// ClassifyIntent uses the intent LLM to classify a message as Chat or Action.
func ClassifyIntent(ctx context.Context, intentLLM oasis.Provider, message string) oasis.Intent {
	req := oasis.ChatRequest{
		Messages: []oasis.ChatMessage{
			oasis.SystemMessage(IntentSystemPrompt),
			oasis.UserMessage(message),
		},
	}

	resp, err := intentLLM.Chat(ctx, req)
	if err != nil {
		// Fail-open: default to Action (safer to enter tool loop than miss an action)
		return oasis.IntentAction
	}

	return ParseIntent(resp.Content)
}

// ParseIntent parses an LLM response into an Intent. Defaults to Action on failure.
func ParseIntent(response string) oasis.Intent {
	jsonStr := extractJSON(response)

	var parsed struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return oasis.IntentAction
	}

	if parsed.Intent == "chat" {
		return oasis.IntentChat
	}
	return oasis.IntentAction
}

// extractJSON finds the first JSON object in a string (handles code fences).
func extractJSON(input string) string {
	trimmed := strings.TrimSpace(input)

	// Strip markdown code fences
	if strings.HasPrefix(trimmed, "```json") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	} else if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}

	return trimmed
}
