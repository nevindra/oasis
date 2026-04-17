// Package todo provides a Claude-Code-style task list tool ("todo_write")
// for oasis agents. Storage is delegated to a Backend interface so embedders
// can persist to whatever fits (in-memory, JSONB column, file, etc.).
package todo

import "context"

// Item is a single entry in an agent's task list.
type Item struct {
	Content    string `json:"content"`    // imperative, e.g. "Run tests"
	ActiveForm string `json:"activeForm"` // present continuous, e.g. "Running tests"
	Status     string `json:"status"`     // "pending" | "in_progress" | "completed"
}

// Backend is the storage adapter for the todo tool. Implementations
// must serialize concurrent calls to Set on the same key.
type Backend interface {
	Get(ctx context.Context, key string) ([]Item, error)
	Set(ctx context.Context, key string, items []Item) error
}
