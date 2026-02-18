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
