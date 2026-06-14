// memory/tools.go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nevindra/oasis/core"
)

// RememberTool returns a core.AnyTool that lets the LLM save a memory item.
// Schema: {content: string, kind?: string, scope?: string, tags?: []string, pinned?: bool}
func (m *AgentMemory) RememberTool() core.AnyTool { return rememberTool{m: m} }

// RecallTool returns a core.AnyTool that lets the LLM search memory.
// Schema: {query: string, kind?: string, scope?: string, k?: int}
// Returns: a JSON array of {id, kind, content, scope, createdAt, score?}
func (m *AgentMemory) RecallTool() core.AnyTool { return recallTool{m: m} }

// ForgetTool lets the LLM delete or correct memory.
// Schema: {id?: string, match?: string, kind?: string, olderThanSeconds?: int}
func (m *AgentMemory) ForgetTool() core.AnyTool { return forgetTool{m: m} }

// PinTool lets the LLM pin/unpin an item. Schema: {id: string, pinned: bool}
func (m *AgentMemory) PinTool() core.AnyTool { return pinTool{m: m} }

// AllTools returns the four memory tools as a slice for convenient inclusion.
func (m *AgentMemory) AllTools() []core.AnyTool {
	return []core.AnyTool{m.RememberTool(), m.RecallTool(), m.ForgetTool(), m.PinTool()}
}

// --- rememberTool ---

type rememberTool struct{ m *AgentMemory }

func (rememberTool) Name() string { return "memory.remember" }
func (rememberTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "memory.remember",
		Description: "Save a memory item for future recall. Args: content (required), kind (default 'fact'), scope ('thread'|'resource'|'agent'), tags, pinned.",
	}
}

func (t rememberTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var a struct {
		Content string   `json:"content"`
		Kind    string   `json:"kind,omitempty"`
		Scope   string   `json:"scope,omitempty"`
		Tags    []string `json:"tags,omitempty"`
		Pinned  bool     `json:"pinned,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return errResult("invalid args: " + err.Error()), nil
	}
	if a.Content == "" {
		return errResult("invalid args: 'content' is required"), nil
	}
	kind := core.MemoryKind(a.Kind)
	if kind == "" {
		kind = KindFact
	}
	scope := scopeFromStr(a.Scope)
	item := core.MemoryItem{
		ID:      core.NewID(),
		Kind:    kind,
		Content: a.Content,
		Scope:   scope,
		Source:  core.MemorySource{Kind: "tool"},
		Tags:    a.Tags,
		Pinned:  a.Pinned,
	}
	if err := t.m.Remember(ctx, item); err != nil {
		return errResult("remember failed: " + err.Error()), nil
	}
	return textResult(fmt.Sprintf("saved %s/%s", kind, item.ID)), nil
}

// --- recallTool ---

type recallTool struct{ m *AgentMemory }

func (recallTool) Name() string { return "memory.recall" }
func (recallTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "memory.recall",
		Description: "Search memory for items related to a query. Args: query (required), kind, scope, k.",
	}
}

func (t recallTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var a struct {
		Query string `json:"query"`
		Kind  string `json:"kind,omitempty"`
		Scope string `json:"scope,omitempty"`
		K     int    `json:"k,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return errResult("invalid args: " + err.Error()), nil
	}
	if a.Query == "" {
		return errResult("invalid args: 'query' is required"), nil
	}
	k := a.K
	if k <= 0 {
		k = 5
	}
	opts := []RecallOption{RecallLimit(k)}
	if a.Kind != "" {
		opts = append(opts, RecallKind(core.MemoryKind(a.Kind)))
	}
	sc := scopeFromStr(a.Scope)
	opts = append(opts, RecallScope(sc))
	items, err := t.m.Recall(ctx, a.Query, opts...)
	if err != nil {
		return errResult("recall failed: " + err.Error()), nil
	}
	type row struct {
		ID        string  `json:"id"`
		Kind      string  `json:"kind"`
		Content   string  `json:"content"`
		Scope     string  `json:"scope"`
		CreatedAt int64   `json:"createdAt"`
		Score     float32 `json:"score"`
	}
	out := make([]row, 0, len(items))
	for _, it := range items {
		out = append(out, row{
			ID:        it.Item.ID,
			Kind:      string(it.Item.Kind),
			Content:   it.Item.Content,
			Scope:     string(it.Item.Scope.Kind) + ":" + it.Item.Scope.Ref,
			CreatedAt: it.Item.CreatedAt,
			Score:     it.Score,
		})
	}
	b, _ := json.Marshal(out)
	return core.ToolResult{Content: string(b)}, nil
}

// --- forgetTool ---

type forgetTool struct{ m *AgentMemory }

func (forgetTool) Name() string { return "memory.forget" }
func (forgetTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "memory.forget",
		Description: "Delete one or more memory items. Args: id (exact) OR match (substring) and/or kind and/or olderThanSeconds.",
	}
}

func (t forgetTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var a struct {
		ID               string `json:"id,omitempty"`
		Match            string `json:"match,omitempty"`
		Kind             string `json:"kind,omitempty"`
		OlderThanSeconds int64  `json:"olderThanSeconds,omitempty"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return errResult("invalid args: " + err.Error()), nil
	}
	if a.ID != "" {
		n, err := t.m.Forget(ctx, ForgetByID(a.ID))
		if err != nil {
			return errResult(err.Error()), nil
		}
		return textResult(fmt.Sprintf("deleted %d items", n)), nil
	}
	if a.Match != "" {
		spec := ForgetByMatch(a.Match)
		if a.Kind != "" {
			spec.Kind = core.MemoryKind(a.Kind)
		}
		if a.OlderThanSeconds > 0 {
			spec.Older = time.Duration(a.OlderThanSeconds) * time.Second
		}
		n, err := t.m.Forget(ctx, spec)
		if err != nil {
			return errResult(err.Error()), nil
		}
		return textResult(fmt.Sprintf("deleted %d items", n)), nil
	}
	if a.Kind != "" || a.OlderThanSeconds > 0 {
		spec := ForgetSpec{}
		if a.Kind != "" {
			spec.Kind = core.MemoryKind(a.Kind)
		}
		if a.OlderThanSeconds > 0 {
			spec.Older = time.Duration(a.OlderThanSeconds) * time.Second
		}
		n, err := t.m.Forget(ctx, spec)
		if err != nil {
			return errResult(err.Error()), nil
		}
		return textResult(fmt.Sprintf("deleted %d items", n)), nil
	}
	return errResult("must specify id, match, kind, or olderThanSeconds"), nil
}

// --- pinTool ---

type pinTool struct{ m *AgentMemory }

func (pinTool) Name() string { return "memory.pin" }
func (pinTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "memory.pin",
		Description: "Pin or unpin a memory item. Pinned items are always loaded into context. Args: id (required), pinned (bool, required).",
	}
}

func (t pinTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var a struct {
		ID     string `json:"id"`
		Pinned bool   `json:"pinned"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return errResult("invalid args: " + err.Error()), nil
	}
	if a.ID == "" {
		return errResult("'id' is required"), nil
	}
	if err := t.m.Pin(ctx, a.ID, a.Pinned); err != nil {
		return errResult(err.Error()), nil
	}
	state := "unpinned"
	if a.Pinned {
		state = "pinned"
	}
	return textResult(state + " " + a.ID), nil
}

// --- helpers ---

// scopeFromStr maps a scope string to a core.MemoryScope with an empty Ref.
// Callers that have thread/chat context should call Scoped directly.
func scopeFromStr(s string) core.MemoryScope {
	switch core.MemoryScopeKind(strings.ToLower(s)) {
	case ScopeThread:
		return Scoped(ScopeThread, "")
	case ScopeAgent:
		return Scoped(ScopeAgent, "")
	case ScopeGlobal:
		return Scoped(ScopeGlobal, "")
	default:
		return Scoped(ScopeResource, "")
	}
}

func textResult(s string) core.ToolResult {
	return core.ToolResult{Content: s}
}

// errResult returns a ToolResult with the Error field set.
func errResult(msg string) core.ToolResult {
	return core.ToolResult{Error: msg}
}

// Compile-time guard: tools satisfy core.AnyTool.
var (
	_ core.AnyTool = rememberTool{}
	_ core.AnyTool = recallTool{}
	_ core.AnyTool = forgetTool{}
	_ core.AnyTool = pinTool{}
)
