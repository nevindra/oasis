package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// --- Parallel tool execution tests ---

// barrierTool is a Tool where each Execute blocks until all concurrent calls
// have started. If tools run sequentially, this deadlocks (caught by timeout).
type barrierTool struct {
	name    string
	barrier chan struct{}
	started chan struct{}
}

func (b *barrierTool) Name() string { return b.name }

func (b *barrierTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: b.name, Description: "barrier tool"}
}

func (b *barrierTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	b.started <- struct{}{} // signal: I have started
	<-b.barrier             // wait for release
	return core.TextResult("done from " + b.name), nil
}

func TestLLMAgentParallelToolExecution(t *testing.T) {
	const numTools = 3
	barrier := make(chan struct{})
	started := make(chan struct{}, numTools)

	// Create tools that share a barrier
	var tools []core.AnyTool
	for i := 0; i < numTools; i++ {
		tools = append(tools, &barrierTool{
			name:    fmt.Sprintf("tool_%d", i),
			barrier: barrier,
			started: started,
		})
	}

	// Provider returns all tool calls at once, then a final response
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{
				{ID: "1", Name: "tool_0", Args: json.RawMessage(`{}`)},
				{ID: "2", Name: "tool_1", Args: json.RawMessage(`{}`)},
				{ID: "3", Name: "tool_2", Args: json.RawMessage(`{}`)},
			}},
			{Content: "all tools completed"},
		},
	}

	agent := New("parallel", "Tests parallel", provider, WithTools(tools...))

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = agent.Execute(context.Background(), AgentTask{Input: "go"})
		close(done)
	}()

	// All 3 tools must start before any can finish.
	// If sequential, tool_1 would block waiting for tool_0 to finish,
	// but tool_0 is waiting for all 3 to start — deadlock.
	for i := 0; i < numTools; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("tool did not start — tools likely running sequentially")
		}
	}

	// Release all tools
	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "all tools completed" {
		t.Errorf("Output = %q, want %q", result.Output, "all tools completed")
	}
}

// --- Plan execution tests ---

func TestLLMAgentPlanExecution(t *testing.T) {
	// Provider calls execute_plan with 3 steps, then synthesizes final response
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[
					{"tool":"greet","args":{}},
					{"tool":"greet","args":{}},
					{"tool":"greet","args":{}}
				]}`),
			}}},
			{Content: "all 3 greetings done"},
		},
	}

	agent := New("planner", "Plans tool calls", provider,
		WithTools(mockTool{}),
		WithPlanExecution(),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "greet 3 times"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "all 3 greetings done" {
		t.Errorf("Output = %q, want %q", result.Output, "all 3 greetings done")
	}
}

func TestLLMAgentPlanExecutionResultFormat(t *testing.T) {
	// Verify the structured per-step result format
	var capturedResult string
	captureProvider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[
					{"tool":"greet","args":{}},
					{"tool":"calc","args":{}}
				]}`),
			}}},
			{Content: "done"},
		},
	}

	agent := New("planner", "Plans", captureProvider,
		WithTools(mockTool{}, mockToolCalc{}),
		WithPlanExecution(),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_ = result

	// The plan result was fed back as a tool result message.
	// We can verify the format by calling executePlan directly.
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "result_" + tc.Name, Usage: core.Usage{InputTokens: 10}}
	}
	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"greet","args":{}},
		{"tool":"calc","args":{}}
	]}`), dispatch, 50, 10)
	capturedResult = dr.Content

	var steps []planStepResult
	if err := json.Unmarshal([]byte(capturedResult), &steps); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	if steps[0].Tool != "greet" || steps[0].Status != "ok" || steps[0].Result != "result_greet" {
		t.Errorf("step 0 = %+v, want tool=greet status=ok result=result_greet", steps[0])
	}
	if steps[1].Tool != "calc" || steps[1].Status != "ok" || steps[1].Result != "result_calc" {
		t.Errorf("step 1 = %+v, want tool=calc status=ok result=result_calc", steps[1])
	}
	if dr.Usage.InputTokens != 20 {
		t.Errorf("usage.InputTokens = %d, want 20", dr.Usage.InputTokens)
	}
}

func TestLLMAgentPlanExecutionErrorStep(t *testing.T) {
	// Verify that a failed step reports error without aborting other steps
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		if tc.Name == "fail" {
			return DispatchResult{Content: "error: tool broken", IsError: true}
		}
		return DispatchResult{Content: "ok_" + tc.Name}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"greet","args":{}},
		{"tool":"fail","args":{}},
		{"tool":"calc","args":{}}
	]}`), dispatch, 50, 10)

	var steps []planStepResult
	if err := json.Unmarshal([]byte(dr.Content), &steps); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if steps[0].Status != "ok" {
		t.Errorf("step 0 status = %q, want ok", steps[0].Status)
	}
	if steps[1].Status != "error" || steps[1].Error != "error: tool broken" {
		t.Errorf("step 1 = %+v, want status=error error='error: tool broken'", steps[1])
	}
	if steps[2].Status != "ok" {
		t.Errorf("step 2 status = %q, want ok", steps[2].Status)
	}
}

func TestLLMAgentPlanExecutionRecursionPrevented(t *testing.T) {
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"execute_plan","args":{"steps":[]}}
	]}`), dispatch, 50, 10)

	if dr.Content != "error: execute_plan steps cannot call execute_plan" {
		t.Errorf("expected recursion error, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionEmptySteps(t *testing.T) {
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[]}`), dispatch, 50, 10)
	if dr.Content != "error: execute_plan requires at least one step" {
		t.Errorf("expected empty steps error, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionInvalidArgs(t *testing.T) {
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`not json`), dispatch, 50, 10)
	if len(dr.Content) < 7 || dr.Content[:7] != "error: " {
		t.Errorf("expected error for invalid args, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionNotEnabledIgnored(t *testing.T) {
	// When WithPlanExecution is NOT set, execute_plan is treated as unknown tool
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[{"tool":"greet","args":{}}]}`),
			}}},
			{Content: "recovered"},
		},
	}

	agent := New("nope", "No plan", provider,
		WithTools(mockTool{}),
		// Note: WithPlanExecution() NOT set
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "recovered" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered")
	}
}

// --- Plan execution edge cases ---

func TestPlanExecutionMaxStepsCap(t *testing.T) {
	// execute_plan should reject plans with more than maxPlanSteps.
	steps := make([]json.RawMessage, maxPlanSteps+1)
	for i := range steps {
		steps[i] = json.RawMessage(fmt.Sprintf(`{"tool":"greet","args":{}}`, ))
	}
	stepsJSON, _ := json.Marshal(struct {
		Steps []json.RawMessage `json:"steps"`
	}{Steps: steps})

	dispatch := func(_ context.Context, _ core.ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), stepsJSON, dispatch, 50, 10)
	if !strings.Contains(dr.Content, fmt.Sprintf("limited to %d", maxPlanSteps)) {
		t.Errorf("error = %q, want mention of step limit", dr.Content)
	}
}

func TestPlanExecutionBlocksAskUser(t *testing.T) {
	// ask_user should be blocked from within execute_plan steps.
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		if tc.Name == "ask_user" {
			return DispatchResult{Content: "error: ask_user cannot be called from within execute_plan", IsError: true}
		}
		return DispatchResult{Content: "ok"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"ask_user","args":{"question":"really?"}}
	]}`), dispatch, 50, 10)

	var steps []planStepResult
	if err := json.Unmarshal([]byte(dr.Content), &steps); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].Status != "error" {
		t.Errorf("step status = %q, want %q", steps[0].Status, "error")
	}
	if !strings.Contains(steps[0].Error, "ask_user") {
		t.Errorf("error = %q, want mention of ask_user", steps[0].Error)
	}
}

// --- dispatchParallel tests ---

func TestDispatchParallelContextCancellation(t *testing.T) {
	// When context is cancelled mid-dispatch, remaining results should
	// be filled with context error markers.
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	dispatch := func(ctx context.Context, tc core.ToolCall) DispatchResult {
		callCount++
		if tc.Name == "slow" {
			// Simulate a slow tool — cancel the context and block.
			cancel()
			<-ctx.Done()
			return DispatchResult{Content: "error: " + ctx.Err().Error(), IsError: true}
		}
		return DispatchResult{Content: "fast result"}
	}

	calls := []core.ToolCall{
		{ID: "1", Name: "fast", Args: json.RawMessage(`{}`)},
		{ID: "2", Name: "slow", Args: json.RawMessage(`{}`)},
	}

	results := dispatchParallel(ctx, calls, dispatch, 10)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// At least one result should contain a context error.
	hasCtxErr := false
	for _, r := range results {
		if strings.Contains(r.content, "context canceled") {
			hasCtxErr = true
		}
	}
	if !hasCtxErr {
		t.Error("expected at least one result with context cancellation error")
	}
}

func TestDispatchParallelSingleCallNoGoroutine(t *testing.T) {
	// Single call should take the fast path (inline, no goroutine).
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		return DispatchResult{Content: "single result"}
	}

	calls := []core.ToolCall{{ID: "1", Name: "tool", Args: json.RawMessage(`{}`)}}
	results := dispatchParallel(context.Background(), calls, dispatch, 10)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].content != "single result" {
		t.Errorf("content = %q, want %q", results[0].content, "single result")
	}
}

func TestDispatchParallelToolPanicRecovery(t *testing.T) {
	// A tool that panics should be caught by safeDispatch.
	dispatch := func(_ context.Context, tc core.ToolCall) DispatchResult {
		if tc.Name == "panicker" {
			panic("tool exploded")
		}
		return DispatchResult{Content: "ok"}
	}

	calls := []core.ToolCall{
		{ID: "1", Name: "safe", Args: json.RawMessage(`{}`)},
		{ID: "2", Name: "panicker", Args: json.RawMessage(`{}`)},
	}

	results := dispatchParallel(context.Background(), calls, dispatch, 10)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].content != "ok" {
		t.Errorf("safe tool content = %q, want %q", results[0].content, "ok")
	}
	if !results[1].isError {
		t.Error("panicked tool should be marked as error")
	}
	if !strings.Contains(results[1].content, "panic") {
		t.Errorf("panic result = %q, want mention of panic", results[1].content)
	}
}

// --- Tool result chunking test ---

func TestToolResultChunkedTransparently(t *testing.T) {
	// Verify that large tool results are split into multiple tool-result messages
	// (all with the same call ID) rather than being truncated with a hint.
	bigContent := strings.Repeat("x", maxToolResultMessageLen+1000)

	// Create a tool that returns a very large result.
	largeTool := &largeTool{content: bigContent}

	var capturedMessages []core.ChatMessage
	// Use a callbackProvider to capture the second request's messages.
	cbProvider := &sequentialCallbackProvider{
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "large", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
		onChat: func(req core.ChatRequest) {
			capturedMessages = req.Messages
		},
	}

	agent := New("chunker", "Tests chunking", cbProvider,
		WithTools(largeTool),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	// Collect all tool-result messages for call ID "1".
	var toolResultMsgs []core.ChatMessage
	for _, m := range capturedMessages {
		if m.ToolCallID == "1" {
			toolResultMsgs = append(toolResultMsgs, m)
		}
	}
	if len(toolResultMsgs) < 2 {
		t.Fatalf("expected >=2 tool-result chunks, got %d", len(toolResultMsgs))
	}

	// Each individual chunk must fit within the limit.
	for i, m := range toolResultMsgs {
		if len([]rune(m.Content)) > maxToolResultMessageLen {
			t.Errorf("chunk %d: len = %d runes, want <= %d", i, len([]rune(m.Content)), maxToolResultMessageLen)
		}
	}

	// Reassembled content must equal the original.
	var reassembled strings.Builder
	for _, m := range toolResultMsgs {
		reassembled.WriteString(m.Content)
	}
	if reassembled.String() != bigContent {
		t.Error("reassembled chunks do not equal original content")
	}

	// No truncation hints or read_full_result markers in any chunk.
	for i, m := range toolResultMsgs {
		if strings.Contains(m.Content, "read_full_result") {
			t.Errorf("chunk %d should not contain read_full_result hint", i)
		}
		if strings.Contains(m.Content, "[output truncated") {
			t.Errorf("chunk %d should not contain truncation marker", i)
		}
	}

	// Step trace should exist and have the right name.
	if len(result.Steps) == 0 {
		t.Fatal("expected at least one step trace")
	}
	if result.Steps[0].Name != "large" {
		t.Errorf("step name = %q, want %q", result.Steps[0].Name, "large")
	}
}

// largeTool is a tool that returns a very large result.
type largeTool struct {
	content string
}

func (l *largeTool) Name() string { return "large" }
func (l *largeTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "large", Description: "Returns large content"}
}

func (l *largeTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.TextResult(l.content), nil
}

// --- TruncateStr unit tests ---

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{"short ASCII", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncate ASCII", "hello world", 5, "hello"},
		{"empty string", "", 5, ""},
		{"zero limit", "hello", 0, ""},
		{"unicode not split", "héllo wörld", 5, "héllo"},
		{"multibyte within limit", "日本語テスト", 3, "日本語"},
		{"multibyte exact", "日本語", 3, "日本語"},
		{"multibyte over", "日本語", 2, "日本"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateStr(tt.input, tt.n)
			if got != tt.want {
				t.Errorf("TruncateStr(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
			}
		})
	}
}

// --- buildStepTrace tests ---

func TestBuildStepTraceToolCall(t *testing.T) {
	tc := core.ToolCall{ID: "1", Name: "web_search", Args: json.RawMessage(`{"query":"test"}`)}
	res := toolExecResult{content: "found it", usage: core.Usage{InputTokens: 10}, duration: time.Second}

	trace := buildStepTrace(tc, res)

	if trace.Name != "web_search" {
		t.Errorf("Name = %q, want %q", trace.Name, "web_search")
	}
	if trace.Type != "tool" {
		t.Errorf("Type = %q, want %q", trace.Type, "tool")
	}
	if trace.Input != `{"query":"test"}` {
		t.Errorf("Input = %q", trace.Input)
	}
	if trace.Output != "found it" {
		t.Errorf("Output = %q, want %q", trace.Output, "found it")
	}
}

func TestBuildStepTraceAgentDelegation(t *testing.T) {
	tc := core.ToolCall{ID: "1", Name: "agent_researcher", Args: json.RawMessage(`{"task":"find papers"}`)}
	res := toolExecResult{content: "3 papers found"}

	trace := buildStepTrace(tc, res)

	if trace.Name != "researcher" {
		t.Errorf("Name = %q, want %q (agent_ prefix should be stripped)", trace.Name, "researcher")
	}
	if trace.Type != "agent" {
		t.Errorf("Type = %q, want %q", trace.Type, "agent")
	}
	if trace.Input != "find papers" {
		t.Errorf("Input = %q, want %q (should extract task field)", trace.Input, "find papers")
	}
}

// TestTerminateIteration_PinsContractFields verifies that terminateIteration
// preserves the AgentResult fields that every error-tail call site sets:
// Usage, Steps, FinishReason, Warnings, ProviderMeta, Files, Iterations, Sources.
// Pinning this contract lets us safely replace 6 hand-rolled tails with the helper.
func TestTerminateIteration_PinsContractFields(t *testing.T) {
	state := &loopState{
		totalUsage:       core.Usage{InputTokens: 7, OutputTokens: 11},
		steps:            []StepTrace{{Name: "x"}},
		lastWarnings:     []string{"w"},
		lastProviderMeta: json.RawMessage(`{"k":"v"}`),
		files:            []core.Attachment{{MimeType: "text/plain"}},
		iterations:       []core.IterationTrace{{Iter: 0}},
		sources:          []core.Source{{URL: "https://example.test"}},
	}
	cfg := LoopConfig{Name: "test", Config: Config{Logger: nopLogger}}
	extra := AgentResult{SuspendPayload: json.RawMessage(`"x"`), SuspendProtocol: "tag"}
	res := terminateIteration(context.Background(), &cfg, nil, state, core.FinishSuspended, extra, nil)
	if res.outcome != iterDone {
		t.Fatalf("outcome = %v, want iterDone", res.outcome)
	}
	if res.final.FinishReason != core.FinishSuspended {
		t.Fatalf("FinishReason = %v, want FinishSuspended", res.final.FinishReason)
	}
	if res.final.Usage != state.totalUsage {
		t.Fatalf("Usage = %+v, want %+v", res.final.Usage, state.totalUsage)
	}
	if len(res.final.Steps) != 1 || len(res.final.Warnings) != 1 ||
		len(res.final.Files) != 1 || len(res.final.Iterations) != 1 ||
		len(res.final.Sources) != 1 {
		t.Fatalf("contract fields not propagated: %+v", res.final)
	}
	if string(res.final.SuspendPayload) != `"x"` || res.final.SuspendProtocol != "tag" {
		t.Fatalf("extra fields not merged: %+v", res.final)
	}
}

// --- splitContentRunes unit tests ---

func TestSplitContentRunes_FitsInOne(t *testing.T) {
	chunks := splitContentRunes("hello", 100)
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Fatalf("expected single chunk %q, got %v", "hello", chunks)
	}
}

func TestSplitContentRunes_ExactBoundary(t *testing.T) {
	chunks := splitContentRunes("abcde", 5)
	if len(chunks) != 1 || chunks[0] != "abcde" {
		t.Fatalf("expected single chunk, got %v", chunks)
	}
}

func TestSplitContentRunes_SplitsEvenly(t *testing.T) {
	s := strings.Repeat("x", 200)
	chunks := splitContentRunes(s, 100)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != strings.Repeat("x", 100) || chunks[1] != strings.Repeat("x", 100) {
		t.Error("chunks do not equal expected 100-x slices")
	}
}

func TestSplitContentRunes_Reassembly(t *testing.T) {
	original := strings.Repeat("abc", 70_000) // 210_000 runes
	chunks := splitContentRunes(original, 100_000)
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(chunks))
	}
	var reassembled strings.Builder
	for _, c := range chunks {
		reassembled.WriteString(c)
	}
	if reassembled.String() != original {
		t.Error("reassembled string does not equal original")
	}
}

// toolThenTextProvider drives a fresh tool-call -> final-text pair on EVERY
// Execute call: each iteration of the agent loop pops the next scripted
// response, cycling back to the start of the pair once a turn ends. This lets a
// single agent (and thus a single shared loopStatePool) be run repeatedly while
// guaranteeing every run populates Steps and Iterations.
type toolThenTextProvider struct {
	idx int
}

func (p *toolThenTextProvider) Name() string { return "tool-then-text" }

func (p *toolThenTextProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	if ch != nil {
		defer close(ch)
	}
	// Even calls: emit a tool call (forces a step + a non-final iteration).
	// Odd calls: emit final text (terminates the turn).
	turn := p.idx
	p.idx++
	if turn%2 == 0 {
		return core.ChatResponse{
			ToolCalls: []core.ToolCall{{ID: "tc", Name: "greet", Args: json.RawMessage(`{}`)}},
		}, nil
	}
	return core.ChatResponse{Content: "done"}, nil
}

// cloneSteps and cloneIterations take deep, value-level snapshots so the
// comparison target does NOT alias the pooled backing array under test — only a
// genuine post-Execute snapshot can detect the pool overwriting result 1.
func cloneSteps(in []StepTrace) []StepTrace {
	out := make([]StepTrace, len(in))
	copy(out, in)
	return out
}

func cloneIterations(in []core.IterationTrace) []core.IterationTrace {
	out := make([]core.IterationTrace, len(in))
	copy(out, in)
	return out
}

// TestExecuteResultNotCorruptedByPooledStateReuse is a regression test for the
// loopState pool aliasing bug: patchTerminal assigns the pooled, append-built
// slices (Steps, Iterations, Warnings, Files, Sources) directly into the
// returned AgentResult. If releaseLoopState truncated those slices to [:0] and
// returned the backing array to the pool, the NEXT Execute in the process would
// append into the very same arrays and silently mutate the previously returned
// result. The fix nils those fields on release so the escaped backing arrays
// are owned solely by the AgentResult. Here we run Execute twice on the same
// agent (each run drives a tool call, so Steps/Iterations are non-empty),
// snapshot result 1's contents, run a third Execute to churn the pool, then
// assert result 1 is still bit-identical to its snapshot.
func TestExecuteResultNotCorruptedByPooledStateReuse(t *testing.T) {
	p := &toolThenTextProvider{}
	a := New("reuse", "exercises pooled state reuse", p, WithTools(mockTool{}))
	ctx := context.Background()

	// Run 1: retain the result and a deep snapshot of its trace slices.
	r1, err := a.Execute(ctx, AgentTask{Input: "first"})
	if err != nil {
		t.Fatalf("Execute run 1: %v", err)
	}
	if len(r1.Steps) == 0 {
		t.Fatalf("run 1 produced no steps; test cannot detect corruption")
	}
	if len(r1.Iterations) == 0 {
		t.Fatalf("run 1 produced no iterations; test cannot detect corruption")
	}
	stepsSnapshot := cloneSteps(r1.Steps)
	itersSnapshot := cloneIterations(r1.Iterations)

	// Run 2 (and 3) on the SAME agent. With the bug, releaseLoopState from run 1
	// returns r1's backing arrays to the pool; these runs reacquire that state
	// and append into the same arrays, overwriting r1.Steps[0] / r1.Iterations[0]
	// in place.
	if _, err := a.Execute(ctx, AgentTask{Input: "second"}); err != nil {
		t.Fatalf("Execute run 2: %v", err)
	}
	if _, err := a.Execute(ctx, AgentTask{Input: "third"}); err != nil {
		t.Fatalf("Execute run 3: %v", err)
	}

	// Result 1 must be untouched by the later runs.
	if !reflect.DeepEqual(r1.Steps, stepsSnapshot) {
		t.Errorf("result 1 Steps were corrupted by a later Execute:\n got  %+v\n want %+v", r1.Steps, stepsSnapshot)
	}
	if !reflect.DeepEqual(r1.Iterations, itersSnapshot) {
		t.Errorf("result 1 Iterations were corrupted by a later Execute:\n got  %+v\n want %+v", r1.Iterations, itersSnapshot)
	}
}

func TestSplitContentRunes_MultibyteUTF8(t *testing.T) {
	// 2-byte runes (é = U+00E9). Each rune is 2 bytes in UTF-8.
	original := strings.Repeat("é", 5)
	chunks := splitContentRunes(original, 3)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks for 5 runes with max=3, got %d", len(chunks))
	}
	if chunks[0] != strings.Repeat("é", 3) || chunks[1] != strings.Repeat("é", 2) {
		t.Errorf("unexpected chunks: %v", chunks)
	}
	// Verify bytes are valid UTF-8 (no broken sequences).
	for i, c := range chunks {
		for j, r := range c {
			if r == '�' {
				t.Errorf("chunk %d position %d: replacement rune (broken UTF-8)", i, j)
			}
		}
	}
}
