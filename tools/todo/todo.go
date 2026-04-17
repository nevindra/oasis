package todo

import (
	"context"
	"encoding/json"
	"fmt"

	oasis "github.com/nevindra/oasis"
)

const (
	maxItems         = 50
	maxContentLen    = 1000
	maxActiveFormLen = 200
	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
)

// nudgeMessage is returned to the model after a successful Set call.
// Mirrors Claude Code's TodoWriteTool result text and keeps the model
// in the habit of re-using the tool throughout a turn.
const nudgeMessage = "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable."

// Tool implements oasis.Tool for managing a Claude-Code-style task list.
type Tool struct {
	backend Backend
	keyFn   func(ctx context.Context) string
}

// New returns a Tool that delegates storage to backend. keyFn extracts the
// scoping identifier (conversation_id, session_id, etc.) from the agent's
// execution context.
func New(backend Backend, keyFn func(context.Context) string) *Tool {
	return &Tool{backend: backend, keyFn: keyFn}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{{
		Name:        "todo_write",
		Description: ToolDescription,
		Parameters:  json.RawMessage(paramsSchema),
	}}
}

func (t *Tool) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	var p struct {
		Todos []Item `json:"todos"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return oasis.ToolResult{Error: "invalid todo_write args: " + err.Error()}, nil
	}
	if err := validate(p.Todos); err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}

	// All-done: replace stored list with empty so the UI auto-hides.
	// Mirrors Claude Code's TodoWriteTool.ts:69 behavior.
	toStore := p.Todos
	if allCompleted(p.Todos) {
		toStore = nil
	}

	key := t.keyFn(ctx)
	if err := t.backend.Set(ctx, key, toStore); err != nil {
		return oasis.ToolResult{Error: "todo_write backend error: " + err.Error()}, nil
	}
	return oasis.ToolResult{Content: nudgeMessage}, nil
}

func validate(items []Item) error {
	if len(items) > maxItems {
		return fmt.Errorf("todo_write: too many items (%d > %d)", len(items), maxItems)
	}
	for i, it := range items {
		if it.Content == "" {
			return fmt.Errorf("todo_write: item %d has empty content", i)
		}
		if it.ActiveForm == "" {
			return fmt.Errorf("todo_write: item %d has empty activeForm", i)
		}
		if len(it.Content) > maxContentLen {
			return fmt.Errorf("todo_write: item %d content too long (%d > %d)", i, len(it.Content), maxContentLen)
		}
		if len(it.ActiveForm) > maxActiveFormLen {
			return fmt.Errorf("todo_write: item %d activeForm too long (%d > %d)", i, len(it.ActiveForm), maxActiveFormLen)
		}
		switch it.Status {
		case statusPending, statusInProgress, statusCompleted:
		default:
			return fmt.Errorf("todo_write: item %d has invalid status %q", i, it.Status)
		}
	}
	return nil
}

func allCompleted(items []Item) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if it.Status != statusCompleted {
			return false
		}
	}
	return true
}

// paramsSchema is the JSON Schema for todo_write's input.
const paramsSchema = `{
	"type": "object",
	"properties": {
		"todos": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"content": {"type": "string", "description": "Imperative, e.g. 'Run tests'"},
					"activeForm": {"type": "string", "description": "Present continuous, e.g. 'Running tests'"},
					"status": {"type": "string", "enum": ["pending", "in_progress", "completed"]}
				},
				"required": ["content", "activeForm", "status"]
			}
		}
	},
	"required": ["todos"]
}`
