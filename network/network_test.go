package network

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/skills"
)

// TestNetworkPassesImagesToSubAgent verifies that when a Network routes a task
// to a sub-agent, the parent task's Images are forwarded to the sub-agent.
func TestNetworkPassesImagesToSubAgent(t *testing.T) {
	var receivedTask agent.AgentTask

	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			receivedTask = task
			return agent.AgentResult{Output: "done"}, nil
		},
	}

	// Router returns a tool call to agent_worker.
	routerArgs, _ := json.Marshal(map[string]string{"task": "analyze the image"})
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_worker", Args: routerArgs}}},
			{Content: "done"},
		},
	}

	net := NewNetwork("net", "test", router, agent.WithAgents(sub))

	images := []core.Attachment{
		mustAttachmentBase64(t, "image/jpeg", "YWJjMTIz"),
	}
	task := agent.AgentTask{
		Input:       "analyze this image",
		Attachments: images,
	}

	_, err := net.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	if len(receivedTask.Attachments) != 1 {
		t.Fatalf("sub-agent received %d images, want 1", len(receivedTask.Attachments))
	}
	if receivedTask.Attachments[0].MimeType != "image/jpeg" || string(receivedTask.Attachments[0].Data) != "abc123" {
		t.Errorf("sub-agent image = %+v, want {image/jpeg, abc123}", receivedTask.Attachments[0])
	}
}

func TestNetworkDynamicPrompt(t *testing.T) {
	var capturedPrompt string
	router := &callbackProvider{
		name:     "router",
		response: core.ChatResponse{Content: "ok"},
		onChat: func(req core.ChatRequest) {
			for _, m := range req.Messages {
				if m.Role == "system" {
					capturedPrompt = m.Content
				}
			}
		},
	}

	net := NewNetwork("dynamic", "Dynamic", router,
		agent.WithDynamicPrompt(func(_ context.Context, task agent.AgentTask) string {
			return "router for " + task.UserID
		}),
	)

	net.Execute(context.Background(), agent.AgentTask{
		Input:  "test",
		UserID: "bob",
	})

	if capturedPrompt != "router for bob" {
		t.Errorf("prompt = %q, want %q", capturedPrompt, "router for bob")
	}
}

func TestNetworkTaskFromContextInTool(t *testing.T) {
	var gotUserID string
	ctxTool := &contextReadingTool{
		onExecute: func(ctx context.Context) {
			if task, ok := agent.TaskFromContext(ctx); ok {
				gotUserID = task.UserID
			}
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "ctx_reader", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	net := NewNetwork("ctx", "Context test", router, agent.WithTools(ctxTool))
	net.Execute(context.Background(), agent.AgentTask{
		Input:  "test",
		UserID: "user-99",
	})

	if gotUserID != "user-99" {
		t.Errorf("gotUserID = %q, want %q", gotUserID, "user-99")
	}
}

func TestNetworkDynamicModel(t *testing.T) {
	routerA := &mockProvider{name: "router-a", responses: []core.ChatResponse{{Content: "from A"}}}
	routerB := &mockProvider{name: "router-b", responses: []core.ChatResponse{{Content: "from B"}}}

	net := NewNetwork("dynamic", "Dynamic model", routerA,
		agent.WithDynamicModel(func(_ context.Context, task agent.AgentTask) core.Provider {
			if task.Extra["tier"] == "pro" {
				return routerB
			}
			return routerA
		}),
	)

	result, _ := net.Execute(context.Background(), agent.AgentTask{
		Input: "hi",
		Extra: map[string]any{"tier": "pro"},
	})
	if result.Output != "from B" {
		t.Errorf("Output = %q, want %q", result.Output, "from B")
	}
}

// --- Test helpers ---

type stubAgent struct {
	name string
	desc string
	fn   func(agent.AgentTask) (agent.AgentResult, error)
}

func (s *stubAgent) Name() string        { return s.name }
func (s *stubAgent) Description() string { return s.desc }
func (s *stubAgent) Execute(ctx context.Context, task agent.AgentTask) (agent.AgentResult, error) {
	return s.fn(task)
}

type mockProvider struct {
	name      string
	responses []core.ChatResponse
	idx       int
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	if m.idx >= len(m.responses) {
		return core.ChatResponse{}, context.Canceled
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}

type callbackProvider struct {
	name     string
	response core.ChatResponse
	onChat   func(core.ChatRequest)
}

func (c *callbackProvider) Name() string { return c.name }
func (c *callbackProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	if c.onChat != nil {
		c.onChat(req)
	}
	return c.response, nil
}

type contextReadingTool struct {
	onExecute func(context.Context)
}

func (t *contextReadingTool) Name() string { return "ctx_reader" }
func (t *contextReadingTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "ctx_reader",
		Description: "reads context",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}
}
func (t *contextReadingTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	if t.onExecute != nil {
		t.onExecute(ctx)
	}
	return core.ToolResult{}, nil
}

// TestNetworkWithSkillsRegistersSkillTools verifies that a Network built with
// agent.WithSkills registers skill_discover and skill_activate in its tool
// registry, mirroring the same wiring that LLMAgent performs.
func TestNetworkWithSkillsRegistersSkillTools(t *testing.T) {
	provider := &mockProvider{
		name:      "router",
		responses: []core.ChatResponse{{Content: "ok"}},
	}

	net := NewNetwork("skills-net", "test", provider,
		agent.WithSkills(&stubSkillProvider{}),
	)

	defs := net.Tools().AllDefinitions()
	toolNames := make(map[string]bool, len(defs))
	for _, d := range defs {
		toolNames[d.Name] = true
	}

	for _, want := range []string{"skill_discover", "skill_activate"} {
		if !toolNames[want] {
			t.Errorf("tool %q not found in Network tool registry; registered tools: %v", want, toolNames)
		}
	}
}

// stubSkillProvider is a minimal skills.SkillProvider that satisfies the
// interface without any backing store. Used only to verify tool registration.
// mustAttachmentBase64 fails the test if base64 decode fails. Used to keep
// test data readable while still routing through the validating constructor.
func mustAttachmentBase64(t *testing.T, mime, encoded string) core.Attachment {
	t.Helper()
	att, err := core.NewAttachmentFromBase64(mime, encoded)
	if err != nil {
		t.Fatalf("decode test attachment: %v", err)
	}
	return att
}

// BenchmarkNetworkBuildToolDefs measures per-call allocations in buildToolDefs
// across varying agent counts. The package-level schema var and pre-sized slice
// should keep allocs/op constant regardless of agent count.
func BenchmarkNetworkBuildToolDefs(b *testing.B) {
	for _, n := range []int{1, 5, 20} {
		n := n
		b.Run(fmt.Sprintf("agents=%d", n), func(b *testing.B) {
			router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
			agents := make([]agent.Agent, n)
			for i := range agents {
				agents[i] = &stubAgent{
					name: fmt.Sprintf("worker%d", i),
					desc: "does work",
					fn:   func(task agent.AgentTask) (agent.AgentResult, error) { return agent.AgentResult{Output: "ok"}, nil },
				}
			}
			opts := make([]agent.AgentOption, 0, 1)
			opts = append(opts, agent.WithAgents(agents...))
			net := NewNetwork("bench", "bench", router, opts...)
			toolDefs := []core.ToolDefinition{
				{Name: "extra", Description: "extra tool", Parameters: json.RawMessage(`{}`)},
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = net.buildToolDefs(toolDefs)
			}
		})
	}
}

type stubSkillProvider struct{}

func (s *stubSkillProvider) Discover(_ context.Context) ([]skills.SkillSummary, error) {
	return nil, nil
}

func (s *stubSkillProvider) Activate(_ context.Context, name string) (skills.Skill, error) {
	return skills.Skill{}, nil
}

// TestNetwork_ExecuteWith_NilSameAsExecute verifies that ExecuteWith(nil) behaves
// identically to Execute.
func TestNetwork_ExecuteWith_NilSameAsExecute(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{Output: "result"}, nil
		},
	}
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_worker", Args: []byte(`{"task":"x"}`)}}},
			{Content: "done"},
		},
	}
	net := NewNetwork("net", "test", router, agent.WithAgents(sub))

	r1, err1 := net.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err1 != nil {
		t.Fatalf("Execute: %v", err1)
	}

	// Reset router for second call
	router.idx = 0
	r2, err2 := net.ExecuteWith(context.Background(), core.AgentTask{Input: "x"}, nil)
	if err2 != nil {
		t.Fatalf("ExecuteWith(nil): %v", err2)
	}

	if r1.Output != r2.Output {
		t.Fatalf("Execute(%q) != ExecuteWith(nil, %q)", r1.Output, r2.Output)
	}
}

// TestNetwork_ExecuteWith_EmptySameAsExecute verifies that ExecuteWith with empty
// RunOptions behaves identically to Execute.
func TestNetwork_ExecuteWith_EmptySameAsExecute(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{Output: "result"}, nil
		},
	}
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_worker", Args: []byte(`{"task":"x"}`)}}},
			{Content: "done"},
		},
	}
	net := NewNetwork("net", "test", router, agent.WithAgents(sub))

	r1, err1 := net.Execute(context.Background(), core.AgentTask{Input: "x"})
	if err1 != nil {
		t.Fatalf("Execute: %v", err1)
	}

	// Reset router for second call
	router.idx = 0
	r2, err2 := net.ExecuteWith(context.Background(), core.AgentTask{Input: "x"}, &agent.RunOptions{})
	if err2 != nil {
		t.Fatalf("ExecuteWith(&{}): %v", err2)
	}

	if r1.Output != r2.Output {
		t.Fatalf("Execute(%q) != ExecuteWith(&{}, %q)", r1.Output, r2.Output)
	}
}

// TestNetwork_ExecuteWith_AppliesOverrides verifies that ExecuteWith applies
// router-level RunOptions overrides instead of rejecting them. RunOptions
// are NOT propagated to subagents — that limitation is documented on the
// method.
func TestNetwork_ExecuteWith_AppliesOverrides(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{Output: "result"}, nil
		},
	}
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{{Content: "ok"}},
	}
	net := NewNetwork("net", "test", router, agent.WithAgents(sub))

	r, err := net.ExecuteWith(context.Background(), core.AgentTask{Input: "x"}, &agent.RunOptions{Limits: &agent.Limits{MaxIter: 3}})
	if err != nil {
		t.Fatalf("ExecuteWith(MaxIter=3): expected success now that overrides are applied, got %v", err)
	}
	if r.Output != "ok" {
		t.Errorf("Output: want %q, got %q", "ok", r.Output)
	}
}

// TestNetwork_ExecuteWith_InvalidOverrideErrors verifies that ExecuteWith
// still rejects invalid RunOptions values.
func TestNetwork_ExecuteWith_InvalidOverrideErrors(t *testing.T) {
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := NewNetwork("net", "test", router)
	_, err := net.ExecuteWith(context.Background(), core.AgentTask{Input: "x"}, &agent.RunOptions{Limits: &agent.Limits{MaxIter: -1}})
	if err == nil {
		t.Fatalf("ExecuteWith with invalid MaxIter: expected validation error")
	}
}

// TestNetwork_ExecuteStreamWith_NilSameAsExecuteStream verifies that
// ExecuteStreamWith(nil) behaves identically to ExecuteStream.
func TestNetwork_ExecuteStreamWith_NilSameAsExecuteStream(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{Output: "result"}, nil
		},
	}
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_worker", Args: []byte(`{"task":"x"}`)}}},
			{Content: "done"},
		},
	}
	net := NewNetwork("net", "test", router, agent.WithAgents(sub))

	ch1 := make(chan core.StreamEvent, 100)
	r1, err1 := net.ExecuteStream(context.Background(), core.AgentTask{Input: "x"}, ch1)
	for range ch1 {
	}
	if err1 != nil {
		t.Fatalf("ExecuteStream: %v", err1)
	}

	// Reset router for second call
	router.idx = 0
	ch2 := make(chan core.StreamEvent, 100)
	r2, err2 := net.ExecuteStreamWith(context.Background(), core.AgentTask{Input: "x"}, ch2, nil)
	for range ch2 {
	}
	if err2 != nil {
		t.Fatalf("ExecuteStreamWith(nil): %v", err2)
	}

	if r1.Output != r2.Output {
		t.Fatalf("ExecuteStream(%q) != ExecuteStreamWith(nil, %q)", r1.Output, r2.Output)
	}
}

// TestNetwork_ExecuteStreamWith_AppliesOverrides verifies that
// ExecuteStreamWith applies router-level RunOptions and streams events through
// the channel until close — no longer rejects overrides.
func TestNetwork_ExecuteStreamWith_AppliesOverrides(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{Output: "result"}, nil
		},
	}
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{{Content: "ok"}},
	}
	net := NewNetwork("net", "test", router, agent.WithAgents(sub))

	ch := make(chan core.StreamEvent, 100)
	r, err := net.ExecuteStreamWith(context.Background(), core.AgentTask{Input: "x"}, ch, &agent.RunOptions{Limits: &agent.Limits{MaxIter: 3}})
	for range ch {
	}
	if err != nil {
		t.Fatalf("ExecuteStreamWith(MaxIter=3): expected success, got %v", err)
	}
	if r.Output != "ok" {
		t.Errorf("Output: want %q, got %q", "ok", r.Output)
	}
}

// TestNetwork_ExecuteStreamWith_InvalidOverrideClosesChannel verifies that
// invalid RunOptions still close the channel and return a validation error.
func TestNetwork_ExecuteStreamWith_InvalidOverrideClosesChannel(t *testing.T) {
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := NewNetwork("net", "test", router)
	ch := make(chan core.StreamEvent, 100)
	_, err := net.ExecuteStreamWith(context.Background(), core.AgentTask{Input: "x"}, ch, &agent.RunOptions{Limits: &agent.Limits{MaxIter: -1}})
	if err == nil {
		t.Fatalf("ExecuteStreamWith with invalid MaxIter: expected validation error")
	}
	if _, ok := <-ch; ok {
		t.Fatalf("channel should be closed on validation error")
	}
}

// TestNetwork_SatisfiesAgentWithOptions verifies that Network implements
// both AgentWithOptions and StreamingAgentWithOptions interfaces.
func TestNetwork_SatisfiesAgentWithOptions(t *testing.T) {
	var _ agent.AgentWithOptions = (*Network)(nil)
	var _ agent.StreamingAgentWithOptions = (*Network)(nil)
}
