package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
)

// DispatchResult holds the result of a single tool or agent dispatch.
type DispatchResult struct {
	Content     string
	Usage       Usage
	Attachments []Attachment
	// IsError signals that Content represents an error message rather than
	// a successful tool result. This enables structural error detection
	// without relying on string-prefix heuristics.
	IsError bool
}

// DispatchFunc executes a single tool call and returns the result.
// LLMAgent provides one that calls ToolRegistry.Execute + ask_user.
// Network provides one that also routes to subagents via the agent_* prefix.
type DispatchFunc func(ctx context.Context, tc ToolCall) DispatchResult

// ToolExecFunc executes a tool by name. Abstracts ToolRegistry.Execute so
// dispatch functions work without an intermediate registry allocation.
type ToolExecFunc = func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)

// ToolExecStreamFunc executes a tool with streaming progress support.
// Abstracts ToolRegistry.ExecuteStream.
type ToolExecStreamFunc = func(ctx context.Context, name string, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error)

// rawMessageToString converts json.RawMessage to a string for DispatchResult.Content.
// JSON string literals are unquoted so plain-text tools produce readable text.
// JSON objects and arrays are returned as-is (verbatim JSON string).
func rawMessageToString(raw json.RawMessage) string {
	if len(raw) >= 2 && raw[0] == '"' {
		if s, err := strconv.Unquote(string(raw)); err == nil {
			return s
		}
	}
	return string(raw)
}

// toolResultToDispatch converts a ToolResult and error into a DispatchResult.
// Centralizes the error-prefix convention used across all tool dispatch paths.
func toolResultToDispatch(result ToolResult, err error) DispatchResult {
	if err != nil {
		return DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	if result.Error != "" {
		return DispatchResult{Content: "error: " + result.Error, IsError: true}
	}
	return DispatchResult{Content: rawMessageToString(result.Content), Attachments: result.Attachments}
}

// DispatchTool executes a tool via the given executor and converts the result
// to a DispatchResult. When executeToolStream is non-nil and ch is non-nil,
// it uses the streaming executor instead.
// Shared by LLMAgent and Network for the common tool path.
// Exported for network subpackage access.
func DispatchTool(ctx context.Context, executeTool ToolExecFunc, executeToolStream ToolExecStreamFunc, name string, args json.RawMessage, ch chan<- StreamEvent) DispatchResult {
	if ch != nil && executeToolStream != nil {
		return toolResultToDispatch(executeToolStream(ctx, name, args, ch))
	}
	return toolResultToDispatch(executeTool(ctx, name, args))
}

// AgentRouter is an optional hook between built-ins and standard tool dispatch.
// Returning (result, true) short-circuits dispatch with that result.
// Returning (_, false) falls through to regular tool dispatch.
type AgentRouter func(ctx context.Context, tc ToolCall) (DispatchResult, bool)

// StandardDispatchConfig is the configuration for NewStandardDispatch.
type StandardDispatchConfig struct {
	Builtins          func(ctx context.Context, tc ToolCall, dispatch DispatchFunc) (DispatchResult, bool)
	SpawnHandler      func(ctx context.Context, args json.RawMessage, defs []ToolDefinition, exec ToolExecFunc) DispatchResult
	AgentRouter       AgentRouter
	ExecuteTool       ToolExecFunc
	ExecuteToolStream ToolExecStreamFunc
	ResolvedToolDefs  []ToolDefinition
	StreamCh          chan<- StreamEvent
	// ResolvePolicy returns the ToolPolicy for a tool name. nil = no policy
	// lookup. Returning (_, false) means no policy applies (pass-through).
	// LLMAgent passes a closure over Config.resolveToolPolicy.
	ResolvePolicy func(name string) (core.ToolPolicy, bool)
	// IsStreamingTool reports whether the tool registered under name is a
	// StreamingAnyTool. Used to bypass policy wrapping for streaming tools.
	// nil ⇒ treat all tools as non-streaming.
	IsStreamingTool func(name string) bool
	// Logger is used to emit a one-time warning when a streaming tool
	// has a policy registered. nil = no logging.
	Logger *slog.Logger
}

// NewStandardDispatch builds the recursive DispatchFunc.
// Order: Builtins → spawn_agent → AgentRouter → (policy/streaming) → DispatchTool.
func NewStandardDispatch(cfg StandardDispatchConfig) DispatchFunc {
	// streamPolicyWarned tracks tool names for which a policy was registered
	// but the tool resolved as streaming; we log a warning once per name.
	var streamPolicyWarned sync.Map

	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		if cfg.Builtins != nil {
			if r, ok := cfg.Builtins(ctx, tc, dispatch); ok {
				return r
			}
		}
		if tc.Name == "spawn_agent" && cfg.SpawnHandler != nil {
			return cfg.SpawnHandler(ctx, tc.Args, cfg.ResolvedToolDefs, cfg.ExecuteTool)
		}
		if cfg.AgentRouter != nil {
			if r, ok := cfg.AgentRouter(ctx, tc); ok {
				return r
			}
		}

		isStreaming := cfg.IsStreamingTool != nil && cfg.IsStreamingTool(tc.Name)

		// Streaming-tool bypass: policy never applies to a streaming tool.
		// Warn once if a user attempted to attach a policy to a streaming tool.
		if isStreaming {
			if cfg.ResolvePolicy != nil {
				if _, hasPolicy := cfg.ResolvePolicy(tc.Name); hasPolicy {
					if _, already := streamPolicyWarned.LoadOrStore(tc.Name, struct{}{}); !already && cfg.Logger != nil {
						cfg.Logger.Warn("tool policy ignored: tool is a StreamingAnyTool", "tool", tc.Name)
					}
				}
			}
			if cfg.StreamCh != nil && cfg.ExecuteToolStream != nil {
				return toolResultToDispatch(cfg.ExecuteToolStream(ctx, tc.Name, tc.Args, cfg.StreamCh))
			}
			return toolResultToDispatch(cfg.ExecuteTool(ctx, tc.Name, tc.Args))
		}

		// Non-streaming path: apply policy if one is registered for this name.
		if cfg.ResolvePolicy != nil {
			if policy, ok := cfg.ResolvePolicy(tc.Name); ok {
				return toolResultToDispatch(runWithPolicy(ctx, policy, func(c context.Context) (ToolResult, error) {
					return cfg.ExecuteTool(c, tc.Name, tc.Args)
				}))
			}
		}
		return DispatchTool(ctx, cfg.ExecuteTool, cfg.ExecuteToolStream, tc.Name, tc.Args, cfg.StreamCh)
	}
	return dispatch
}

// --- parallel tool dispatch ---

// toolExecResult holds the result of a single parallel tool call.
type toolExecResult struct {
	content     string
	usage       Usage
	attachments []Attachment
	duration    time.Duration
	isError     bool
}

// indexedResult pairs a tool execution result with its position in the
// original call slice, allowing channel-based collection in order.
type indexedResult struct {
	idx    int
	result toolExecResult
}

// safeDispatch wraps a dispatch call with panic recovery. If the dispatched
// tool panics, the panic is caught and converted to an error result instead
// of crashing the process. Matches the recovery pattern used for subagent
// dispatch in Network.makeDispatch.
func safeDispatch(ctx context.Context, tc ToolCall, dispatch DispatchFunc) (dr DispatchResult) {
	defer func() {
		if p := recover(); p != nil {
			dr = DispatchResult{Content: fmt.Sprintf("error: tool %q panic: %v", tc.Name, p), IsError: true}
		}
	}()
	return dispatch(ctx, tc)
}

// dispatchParallel runs all tool calls concurrently via the dispatch function
// and returns results in the same order as the input calls.
// Single calls run inline (no goroutine). Multiple calls use a fixed worker
// pool of min(len(calls), maxWorkers) goroutines pulling from a shared work
// channel, avoiding unbounded goroutine creation.
//
// The collection loop is context-aware: if ctx is cancelled while tool calls
// are still in-flight, the function returns immediately with context-error
// results for incomplete calls instead of blocking indefinitely.
func dispatchParallel(ctx context.Context, calls []ToolCall, dispatch DispatchFunc, maxWorkers int) []toolExecResult {
	// Fast path: single call, no goroutine needed.
	if len(calls) == 1 {
		start := time.Now()
		dr := safeDispatch(ctx, calls[0], dispatch)
		return []toolExecResult{{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError}}
	}

	resultCh := make(chan indexedResult, len(calls))

	// Work channel: each item is an (index, ToolCall) pair for workers to consume.
	type workItem struct {
		idx int
		tc  ToolCall
	}
	workCh := make(chan workItem, len(calls))
	for i, tc := range calls {
		workCh <- workItem{idx: i, tc: tc}
	}
	close(workCh)

	// Spawn a fixed pool of workers — never more goroutines than needed.
	numWorkers := min(len(calls), maxWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for range numWorkers {
		go func() {
			defer wg.Done()
			for w := range workCh {
				if ctx.Err() != nil {
					resultCh <- indexedResult{w.idx, toolExecResult{content: "error: " + ctx.Err().Error(), isError: true}}
					continue
				}
				start := time.Now()
				dr := safeDispatch(ctx, w.tc, dispatch)
				resultCh <- indexedResult{w.idx, toolExecResult{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError}}
			}
		}()
	}

	// Close resultCh once all workers are done.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results, bailing out if ctx is cancelled while calls are in-flight.
	results := make([]toolExecResult, len(calls))
	seen := make([]bool, len(calls))
collect:
	for received := 0; received < len(calls); received++ {
		select {
		case r, ok := <-resultCh:
			if !ok {
				break collect
			}
			results[r.idx] = r.result
			seen[r.idx] = true
		case <-ctx.Done():
			errResult := toolExecResult{content: "error: " + ctx.Err().Error(), isError: true}
			for i := range results {
				if !seen[i] {
					results[i] = errResult
				}
			}
			return results
		}
	}
	return results
}
