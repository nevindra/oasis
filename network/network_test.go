package network

import (
	"context"
	"encoding/json"
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
		{MimeType: "image/jpeg", Base64: "abc123"},
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
	if receivedTask.Attachments[0].MimeType != "image/jpeg" || receivedTask.Attachments[0].Base64 != "abc123" {
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
func (m *mockProvider) Chat(ctx context.Context, req core.ChatRequest) (core.ChatResponse, error) {
	if m.idx >= len(m.responses) {
		return core.ChatResponse{}, context.Canceled
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}
func (m *mockProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return m.Chat(ctx, req)
}

type callbackProvider struct {
	name     string
	response core.ChatResponse
	onChat   func(core.ChatRequest)
}

func (c *callbackProvider) Name() string { return c.name }
func (c *callbackProvider) Chat(ctx context.Context, req core.ChatRequest) (core.ChatResponse, error) {
	if c.onChat != nil {
		c.onChat(req)
	}
	return c.response, nil
}
func (c *callbackProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return c.Chat(ctx, req)
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

	defs := net.Tools.AllDefinitions()
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
type stubSkillProvider struct{}

func (s *stubSkillProvider) Discover(_ context.Context) ([]skills.SkillSummary, error) {
	return nil, nil
}

func (s *stubSkillProvider) Activate(_ context.Context, name string) (skills.Skill, error) {
	return skills.Skill{}, nil
}
