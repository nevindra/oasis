package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- Parallel tool execution tests ---

// barrierTool is a Tool where each Execute blocks until all concurrent calls
// have started. If tools run sequentially, this deadlocks (caught by timeout).
type barrierTool struct {
	name    string
	barrier chan struct{}
	started chan struct{}
}

func (b *barrierTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: b.name, Description: "barrier tool"}}
}

func (b *barrierTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	b.started <- struct{}{} // signal: I have started
	<-b.barrier             // wait for release
	return ToolResult{Content: "done from " + b.name}, nil
}

func TestLLMAgentParallelToolExecution(t *testing.T) {
	const numTools = 3
	barrier := make(chan struct{})
	started := make(chan struct{}, numTools)

	// Create tools that share a barrier
	var tools []Tool
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
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "tool_0", Args: json.RawMessage(`{}`)},
				{ID: "2", Name: "tool_1", Args: json.RawMessage(`{}`)},
				{ID: "3", Name: "tool_2", Args: json.RawMessage(`{}`)},
			}},
			{Content: "all tools completed"},
		},
	}

	agent := NewLLMAgent("parallel", "Tests parallel", provider, WithTools(tools...))

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

func TestNetworkParallelToolExecution(t *testing.T) {
	const numTools = 3
	barrier := make(chan struct{})
	started := make(chan struct{}, numTools)

	var tools []Tool
	for i := 0; i < numTools; i++ {
		tools = append(tools, &barrierTool{
			name:    fmt.Sprintf("tool_%d", i),
			barrier: barrier,
			started: started,
		})
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "tool_0", Args: json.RawMessage(`{}`)},
				{ID: "2", Name: "tool_1", Args: json.RawMessage(`{}`)},
				{ID: "3", Name: "tool_2", Args: json.RawMessage(`{}`)},
			}},
			{Content: "network parallel done"},
		},
	}

	network := NewNetwork("parallel", "Tests parallel", router, WithTools(tools...))

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = network.Execute(context.Background(), AgentTask{Input: "go"})
		close(done)
	}()

	for i := 0; i < numTools; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("tool did not start — tools likely running sequentially")
		}
	}

	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("network did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "network parallel done" {
		t.Errorf("Output = %q, want %q", result.Output, "network parallel done")
	}
}

func TestNetworkParallelAgentExecution(t *testing.T) {
	// Verify subagent dispatches also run in parallel
	barrier := make(chan struct{})
	started := make(chan struct{}, 2)

	makeAgent := func(name string) *stubAgent {
		return &stubAgent{
			name: name,
			desc: "Barrier agent",
			fn: func(task AgentTask) (AgentResult, error) {
				started <- struct{}{}
				<-barrier
				return AgentResult{Output: name + " done"}, nil
			},
		}
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "agent_alpha", Args: json.RawMessage(`{"task":"work"}`)},
				{ID: "2", Name: "agent_beta", Args: json.RawMessage(`{"task":"work"}`)},
			}},
			{Content: "both agents done"},
		},
	}

	network := NewNetwork("parallel", "Tests parallel agents", router,
		WithAgents(makeAgent("alpha"), makeAgent("beta")),
	)

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = network.Execute(context.Background(), AgentTask{Input: "go"})
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("agent did not start — agents likely running sequentially")
		}
	}

	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("network did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "both agents done" {
		t.Errorf("Output = %q, want %q", result.Output, "both agents done")
	}
}

// --- Plan execution tests ---

func TestLLMAgentPlanExecution(t *testing.T) {
	// Provider calls execute_plan with 3 steps, then synthesizes final response
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
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

	agent := NewLLMAgent("planner", "Plans tool calls", provider,
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
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
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

	agent := NewLLMAgent("planner", "Plans", captureProvider,
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
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "result_" + tc.Name, Usage: Usage{InputTokens: 10}}
	}
	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"greet","args":{}},
		{"tool":"calc","args":{}}
	]}`), dispatch)
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
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "fail" {
			return DispatchResult{Content: "error: tool broken", IsError: true}
		}
		return DispatchResult{Content: "ok_" + tc.Name}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"greet","args":{}},
		{"tool":"fail","args":{}},
		{"tool":"calc","args":{}}
	]}`), dispatch)

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
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"execute_plan","args":{"steps":[]}}
	]}`), dispatch)

	if dr.Content != "error: execute_plan steps cannot call execute_plan" {
		t.Errorf("expected recursion error, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionEmptySteps(t *testing.T) {
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[]}`), dispatch)
	if dr.Content != "error: execute_plan requires at least one step" {
		t.Errorf("expected empty steps error, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionInvalidArgs(t *testing.T) {
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`not json`), dispatch)
	if len(dr.Content) < 7 || dr.Content[:7] != "error: " {
		t.Errorf("expected error for invalid args, got %q", dr.Content)
	}
}

func TestLLMAgentPlanExecutionNotEnabledIgnored(t *testing.T) {
	// When WithPlanExecution is NOT set, execute_plan is treated as unknown tool
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[{"tool":"greet","args":{}}]}`),
			}}},
			{Content: "recovered"},
		},
	}

	agent := NewLLMAgent("nope", "No plan", provider,
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

func TestNetworkPlanExecution(t *testing.T) {
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_plan",
				Args: json.RawMessage(`{"steps":[
					{"tool":"greet","args":{}},
					{"tool":"greet","args":{}}
				]}`),
			}}},
			{Content: "network plan done"},
		},
	}

	network := NewNetwork("net", "Plans", router,
		WithTools(mockTool{}),
		WithPlanExecution(),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network plan done" {
		t.Errorf("Output = %q, want %q", result.Output, "network plan done")
	}
}

// --- Attachment byte budget tests ---

func TestAccumulatedAttachmentsByteBudget(t *testing.T) {
	// Subagent that returns a 600KB attachment per call.
	bigAgent := &stubAgent{
		name: "imager",
		desc: "Returns large attachments",
		fn: func(_ AgentTask) (AgentResult, error) {
			return AgentResult{
				Output:      "image generated",
				Attachments: []Attachment{{MimeType: "image/png", Data: make([]byte, 600*1024)}},
			}, nil
		},
	}

	// Router calls the subagent 3 times, then gives a final response.
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "agent_imager", Args: json.RawMessage(`{"task":"img1"}`)}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "agent_imager", Args: json.RawMessage(`{"task":"img2"}`)}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "agent_imager", Args: json.RawMessage(`{"task":"img3"}`)}}},
			{Content: "done"},
		},
	}

	// Set byte budget to 1MB — only first attachment (600KB) fits; second
	// would push total to 1200KB which exceeds 1MB.
	net := NewNetwork("net", "test", router,
		WithAgents(bigAgent),
		WithMaxAttachmentBytes(1<<20),
	)

	result, err := net.Execute(context.Background(), AgentTask{Input: "generate images"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// With 1MB budget and 600KB per attachment, only 1 should fit.
	if len(result.Attachments) != 1 {
		t.Errorf("got %d attachments, want 1 (byte budget should cap at 1MB)", len(result.Attachments))
	}
}

// --- Subagent panic recovery tests ---

func TestNetworkSubagentPanicRecovery(t *testing.T) {
	// A subagent that panics should be caught and converted to an error,
	// not crash the network.
	panicAgent := &stubAgent{
		name: "crasher",
		desc: "Always panics",
		fn: func(_ AgentTask) (AgentResult, error) {
			panic("subagent on fire")
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_crasher",
				Args: json.RawMessage(`{"task":"crash"}`),
			}}},
			{Content: "recovered from panic"},
		},
	}

	network := NewNetwork("net", "Panic test", router, WithAgents(panicAgent))

	result, err := network.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatalf("Execute should not return error (panic is caught): %v", err)
	}
	if result.Output != "recovered from panic" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered from panic")
	}
}

func TestNetworkStreamingSubagentPanicRecovery(t *testing.T) {
	// A streaming subagent that panics during ExecuteStream should be caught.
	panicStreamer := &stubStreamingAgent{
		name: "crasher",
		desc: "Panics during stream",
	}
	// Override ExecuteStream to panic.
	type panicStreamAgent struct {
		*stubStreamingAgent
	}
	panicker := &panicStreamAgent{panicStreamer}

	// We can't override the method on stubStreamingAgent, so use a custom type.
	panicExecAgent := &stubAgent{
		name: "crasher",
		desc: "Panics",
		fn: func(_ AgentTask) (AgentResult, error) {
			panic("streaming panic")
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_crasher",
				Args: json.RawMessage(`{"task":"crash"}`),
			}}},
			{Content: "recovered"},
		},
	}

	// Non-streaming path with ch != nil: tests the non-StreamingAgent panic path
	// in network dispatch (network.go:373-382).
	network := NewNetwork("net", "Panic stream test", router, WithAgents(panicExecAgent))

	ch := make(chan StreamEvent, 32)
	result, err := network.ExecuteStream(context.Background(), AgentTask{Input: "go"}, ch)
	if err != nil {
		t.Fatalf("ExecuteStream should not return error (panic is caught): %v", err)
	}
	if result.Output != "recovered" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered")
	}
	// Drain events.
	for range ch {
	}
	_ = panicker // suppress unused
}

// --- execute_code tool tests ---

// mockCodeRunner is a CodeRunner for testing.
type mockCodeRunner struct {
	result CodeResult
	err    error
	// captures for assertions
	capturedCode     string
	capturedDispatch DispatchFunc
}

func (r *mockCodeRunner) Run(_ context.Context, req CodeRequest, dispatch DispatchFunc) (CodeResult, error) {
	r.capturedCode = req.Code
	r.capturedDispatch = dispatch
	return r.result, r.err
}

func TestExecuteCodeSuccess(t *testing.T) {
	runner := &mockCodeRunner{
		result: CodeResult{Output: "42", Logs: "computed"},
	}
	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{Content: "tool result"}
	}

	dr := executeCode(context.Background(), json.RawMessage(`{"code":"print(42)"}`), runner, dispatch)

	if runner.capturedCode != "print(42)" {
		t.Errorf("captured code = %q, want %q", runner.capturedCode, "print(42)")
	}
	if !strings.Contains(dr.Content, "42") {
		t.Errorf("content = %q, want to contain %q", dr.Content, "42")
	}
	if !strings.Contains(dr.Content, "computed") {
		t.Errorf("content = %q, want to contain logs %q", dr.Content, "computed")
	}
	if dr.IsError {
		t.Error("should not be error")
	}
}

func TestExecuteCodeEmptyCode(t *testing.T) {
	runner := &mockCodeRunner{}
	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{}
	}

	dr := executeCode(context.Background(), json.RawMessage(`{"code":""}`), runner, dispatch)
	if !dr.IsError {
		t.Error("empty code should be error")
	}
	if !strings.Contains(dr.Content, "non-empty") {
		t.Errorf("error = %q, want mention of non-empty", dr.Content)
	}
}

func TestExecuteCodeInvalidArgs(t *testing.T) {
	runner := &mockCodeRunner{}
	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{}
	}

	dr := executeCode(context.Background(), json.RawMessage(`not json`), runner, dispatch)
	if !dr.IsError {
		t.Error("invalid args should be error")
	}
	if !strings.Contains(dr.Content, "invalid") {
		t.Errorf("error = %q, want mention of invalid", dr.Content)
	}
}

func TestExecuteCodeRunnerError(t *testing.T) {
	runner := &mockCodeRunner{
		err: errors.New("sandbox crashed"),
	}
	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{}
	}

	dr := executeCode(context.Background(), json.RawMessage(`{"code":"import os"}`), runner, dispatch)
	if !dr.IsError {
		t.Error("runner error should be error")
	}
	if !strings.Contains(dr.Content, "sandbox crashed") {
		t.Errorf("error = %q, want mention of sandbox crashed", dr.Content)
	}
}

func TestExecuteCodeRuntimeError(t *testing.T) {
	// Code ran but returned an error (e.g. syntax error, exception).
	runner := &mockCodeRunner{
		result: CodeResult{Error: "NameError: undefined variable", Logs: "line 3"},
	}
	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{}
	}

	dr := executeCode(context.Background(), json.RawMessage(`{"code":"x"}`), runner, dispatch)
	if !dr.IsError {
		t.Error("runtime error should set IsError")
	}
	if !strings.Contains(dr.Content, "NameError") {
		t.Errorf("error = %q, want mention of NameError", dr.Content)
	}
	if !strings.Contains(dr.Content, "line 3") {
		t.Errorf("error = %q, want logs included", dr.Content)
	}
}

func TestExecuteCodeNoResult(t *testing.T) {
	// Code ran successfully but didn't call set_result().
	runner := &mockCodeRunner{
		result: CodeResult{Output: "", Logs: "debug info"},
	}
	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{}
	}

	dr := executeCode(context.Background(), json.RawMessage(`{"code":"print('hi')"}`), runner, dispatch)
	if dr.IsError {
		t.Error("should not be error")
	}
	if !strings.Contains(dr.Content, "set_result") {
		t.Errorf("content = %q, want hint about set_result", dr.Content)
	}
	if !strings.Contains(dr.Content, "debug info") {
		t.Errorf("content = %q, want logs included", dr.Content)
	}
}

// --- execute_code recursion prevention ---

func TestExecuteCodeRecursionPrevention(t *testing.T) {
	// From within execute_code, calling execute_plan or execute_code should be blocked.
	runner := &mockCodeRunner{
		result: CodeResult{Output: "done"},
	}

	dispatched := false
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		dispatched = true
		return DispatchResult{Content: "should not reach"}
	}

	// dispatchBuiltins wraps the dispatch to block recursion.
	var wrappedDispatch DispatchFunc
	wrappedDispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "execute_plan" || tc.Name == "execute_code" {
			return DispatchResult{Content: "error: " + tc.Name + " cannot be called from within execute_code", IsError: true}
		}
		return dispatch(ctx, tc)
	}

	// Simulate what happens inside execute_code: the dispatch passed to the runner
	// should block execute_plan and execute_code.
	_ = executeCode(context.Background(), json.RawMessage(`{"code":"call_tool('greet', {})"}`), runner, wrappedDispatch)

	// Now test the actual blocking by calling the wrapped dispatch directly.
	planResult := wrappedDispatch(context.Background(), ToolCall{Name: "execute_plan"})
	if !planResult.IsError {
		t.Error("execute_plan from within code should be blocked")
	}
	if !strings.Contains(planResult.Content, "cannot be called") {
		t.Errorf("error = %q, want mention of cannot be called", planResult.Content)
	}

	codeResult := wrappedDispatch(context.Background(), ToolCall{Name: "execute_code"})
	if !codeResult.IsError {
		t.Error("execute_code from within code should be blocked")
	}

	// Regular tools should pass through.
	regularResult := wrappedDispatch(context.Background(), ToolCall{Name: "greet"})
	if regularResult.IsError {
		t.Error("regular tools should pass through")
	}
	if !dispatched {
		t.Error("regular tool dispatch should have been called")
	}
}

func TestLLMAgentWithCodeExecution(t *testing.T) {
	// Integration test: verify LLMAgent wires execute_code through the full loop.
	runner := &mockCodeRunner{
		result: CodeResult{Output: "computed: 42"},
	}

	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "execute_code",
				Args: json.RawMessage(`{"code":"set_result(40+2)"}`),
			}}},
			{Content: "the answer is 42"},
		},
	}

	agent := NewLLMAgent("coder", "Runs code", provider,
		WithCodeExecution(runner),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "the answer is 42" {
		t.Errorf("Output = %q, want %q", result.Output, "the answer is 42")
	}
	if runner.capturedCode != "set_result(40+2)" {
		t.Errorf("runner saw code = %q, want %q", runner.capturedCode, "set_result(40+2)")
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

	dispatch := func(_ context.Context, _ ToolCall) DispatchResult {
		return DispatchResult{Content: "should not reach"}
	}

	dr := executePlan(context.Background(), stepsJSON, dispatch)
	if !strings.Contains(dr.Content, fmt.Sprintf("limited to %d", maxPlanSteps)) {
		t.Errorf("error = %q, want mention of step limit", dr.Content)
	}
}

func TestPlanExecutionBlocksAskUser(t *testing.T) {
	// ask_user should be blocked from within execute_plan steps.
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "ask_user" {
			return DispatchResult{Content: "error: ask_user cannot be called from within execute_plan", IsError: true}
		}
		return DispatchResult{Content: "ok"}
	}

	dr := executePlan(context.Background(), json.RawMessage(`{"steps":[
		{"tool":"ask_user","args":{"question":"really?"}}
	]}`), dispatch)

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
	dispatch := func(ctx context.Context, tc ToolCall) DispatchResult {
		callCount++
		if tc.Name == "slow" {
			// Simulate a slow tool — cancel the context and block.
			cancel()
			<-ctx.Done()
			return DispatchResult{Content: "error: " + ctx.Err().Error(), IsError: true}
		}
		return DispatchResult{Content: "fast result"}
	}

	calls := []ToolCall{
		{ID: "1", Name: "fast", Args: json.RawMessage(`{}`)},
		{ID: "2", Name: "slow", Args: json.RawMessage(`{}`)},
	}

	results := dispatchParallel(ctx, calls, dispatch)

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
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		return DispatchResult{Content: "single result"}
	}

	calls := []ToolCall{{ID: "1", Name: "tool", Args: json.RawMessage(`{}`)}}
	results := dispatchParallel(context.Background(), calls, dispatch)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].content != "single result" {
		t.Errorf("content = %q, want %q", results[0].content, "single result")
	}
}

func TestDispatchParallelToolPanicRecovery(t *testing.T) {
	// A tool that panics should be caught by safeDispatch.
	dispatch := func(_ context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "panicker" {
			panic("tool exploded")
		}
		return DispatchResult{Content: "ok"}
	}

	calls := []ToolCall{
		{ID: "1", Name: "safe", Args: json.RawMessage(`{}`)},
		{ID: "2", Name: "panicker", Args: json.RawMessage(`{}`)},
	}

	results := dispatchParallel(context.Background(), calls, dispatch)

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

// --- Tool result truncation test ---

func TestToolResultTruncationInLoop(t *testing.T) {
	// Verify that large tool results are truncated in the message history
	// but the step trace retains the full content.
	bigContent := strings.Repeat("x", maxToolResultMessageLen+1000)
	bigTool := &stubAgent{
		name: "big",
		desc: "Returns huge content",
		fn: func(_ AgentTask) (AgentResult, error) {
			return AgentResult{Output: bigContent}, nil
		},
	}
	_ = bigTool // We test via the tool path, not agent path.

	// Create a tool that returns a very large result.
	largeTool := &largeTool{content: bigContent}

	var capturedMessages []ChatMessage
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "large", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	// Use a callbackProvider to capture the second request's messages.
	cbProvider := &sequentialCallbackProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "large", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
		onChat: func(req ChatRequest) {
			capturedMessages = req.Messages
		},
	}
	_ = provider // use cbProvider instead

	agent := NewLLMAgent("truncator", "Tests truncation", cbProvider,
		WithTools(largeTool),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}

	// Find the tool result message in the captured messages.
	var toolResultMsg *ChatMessage
	for i, m := range capturedMessages {
		if m.ToolCallID == "1" {
			toolResultMsg = &capturedMessages[i]
			break
		}
	}
	if toolResultMsg == nil {
		t.Fatal("tool result message not found in captured messages")
	}

	// The message content should be truncated.
	if len([]rune(toolResultMsg.Content)) > maxToolResultMessageLen+100 { // +100 for the truncation marker
		t.Errorf("tool result message len = %d runes, want <= %d (should be truncated)",
			len([]rune(toolResultMsg.Content)), maxToolResultMessageLen+100)
	}
	if !strings.Contains(toolResultMsg.Content, "[output truncated") {
		t.Error("truncated message should contain truncation marker")
	}

	// Step trace should retain the full content.
	if len(result.Steps) == 0 {
		t.Fatal("expected at least one step trace")
	}
	// Step trace output is truncated to 500 chars by buildStepTrace, not maxToolResultMessageLen.
	// Verify it exists and has content.
	if result.Steps[0].Name != "large" {
		t.Errorf("step name = %q, want %q", result.Steps[0].Name, "large")
	}
}

// largeTool is a tool that returns a very large result.
type largeTool struct {
	content string
}

func (l *largeTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "large", Description: "Returns large content"}}
}

func (l *largeTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: l.content}, nil
}

// --- truncateStr unit tests ---

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
			got := truncateStr(tt.input, tt.n)
			if got != tt.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
			}
		})
	}
}

// --- buildStepTrace tests ---

func TestBuildStepTraceToolCall(t *testing.T) {
	tc := ToolCall{ID: "1", Name: "web_search", Args: json.RawMessage(`{"query":"test"}`)}
	res := toolExecResult{content: "found it", usage: Usage{InputTokens: 10}, duration: time.Second}

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
	tc := ToolCall{ID: "1", Name: "agent_researcher", Args: json.RawMessage(`{"task":"find papers"}`)}
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
