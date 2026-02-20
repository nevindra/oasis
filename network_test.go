package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

// TestNetworkPassesImagesToSubAgent verifies that when a Network routes a task
// to a sub-agent, the parent task's Images are forwarded to the sub-agent.
func TestNetworkPassesImagesToSubAgent(t *testing.T) {
	var receivedTask AgentTask

	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task AgentTask) (AgentResult, error) {
			receivedTask = task
			return AgentResult{Output: "done"}, nil
		},
	}

	// Router returns a tool call to agent_worker.
	routerArgs, _ := json.Marshal(map[string]string{"task": "analyze the image"})
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "agent_worker", Args: routerArgs}}},
			{Content: "done"},
		},
	}

	network := NewNetwork("net", "test", router, WithAgents(sub))

	images := []Attachment{
		{MimeType: "image/jpeg", Base64: "abc123"},
	}
	task := AgentTask{
		Input:  "analyze this image",
		Attachments: images,
	}

	_, err := network.Execute(context.Background(), task)
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
		response: ChatResponse{Content: "ok"},
		onChat: func(req ChatRequest) {
			for _, m := range req.Messages {
				if m.Role == "system" {
					capturedPrompt = m.Content
				}
			}
		},
	}

	network := NewNetwork("dynamic", "Dynamic", router,
		WithDynamicPrompt(func(_ context.Context, task AgentTask) string {
			return "router for " + task.TaskUserID()
		}),
	)

	network.Execute(context.Background(), AgentTask{
		Input:   "test",
		Context: map[string]any{ContextUserID: "bob"},
	})

	if capturedPrompt != "router for bob" {
		t.Errorf("prompt = %q, want %q", capturedPrompt, "router for bob")
	}
}

func TestNetworkTaskFromContextInTool(t *testing.T) {
	var gotUserID string
	ctxTool := &contextReadingTool{
		onExecute: func(ctx context.Context) {
			if task, ok := TaskFromContext(ctx); ok {
				gotUserID = task.TaskUserID()
			}
		},
	}

	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "ctx_reader", Args: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}

	network := NewNetwork("ctx", "Context test", router, WithTools(ctxTool))
	network.Execute(context.Background(), AgentTask{
		Input:   "test",
		Context: map[string]any{ContextUserID: "user-99"},
	})

	if gotUserID != "user-99" {
		t.Errorf("gotUserID = %q, want %q", gotUserID, "user-99")
	}
}

func TestNetworkDynamicModel(t *testing.T) {
	routerA := &mockProvider{name: "router-a", responses: []ChatResponse{{Content: "from A"}}}
	routerB := &mockProvider{name: "router-b", responses: []ChatResponse{{Content: "from B"}}}

	network := NewNetwork("dynamic", "Dynamic model", routerA,
		WithDynamicModel(func(_ context.Context, task AgentTask) Provider {
			if task.Context["tier"] == "pro" {
				return routerB
			}
			return routerA
		}),
	)

	result, _ := network.Execute(context.Background(), AgentTask{
		Input:   "hi",
		Context: map[string]any{"tier": "pro"},
	})
	if result.Output != "from B" {
		t.Errorf("Output = %q, want %q", result.Output, "from B")
	}
}
