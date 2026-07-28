package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

// imageTool returns a tool result with an image attachment, like
// browser_read(action='screenshot') does.
type imageTool struct {
	calls *int
}

func (s imageTool) Name() string { return "shot" }
func (s imageTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "shot", Description: "Take a screenshot"}
}
func (s imageTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	*s.calls++
	return core.ToolResult{
		Content:     "screenshot captured; the image is attached to this result",
		Attachments: []core.Attachment{{MimeType: "image/png", Data: []byte(fmt.Sprintf("png-%d", *s.calls))}},
	}, nil
}

// pdfTool returns a non-image attachment, which must NOT be re-injected.
type pdfTool struct{}

func (pdfTool) Name() string { return "export" }
func (pdfTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "export", Description: "Export a PDF"}
}
func (pdfTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.ToolResult{
		Content:     "pdf exported",
		Attachments: []core.Attachment{{MimeType: "application/pdf", Data: []byte("pdf-bytes")}},
	}, nil
}

// captureRequests wraps a mockProvider so each ChatRequest's messages are
// snapshotted for later assertions.
func captureRequests(p *mockProvider) *[][]core.ChatMessage {
	var mu sync.Mutex
	reqs := &[][]core.ChatMessage{}
	p.onChat = func(req *core.ChatRequest) {
		mu.Lock()
		defer mu.Unlock()
		snap := make([]core.ChatMessage, len(req.Messages))
		copy(snap, req.Messages)
		*reqs = append(*reqs, snap)
	}
	return reqs
}

func imageMessages(msgs []core.ChatMessage) []core.ChatMessage {
	var out []core.ChatMessage
	for _, m := range msgs {
		if m.Role == core.RoleUser && (m.Content == toolImageMessageContent || m.Content == toolImagePrunedContent) {
			out = append(out, m)
		}
	}
	return out
}

func TestToolImageInjectedAsUserMessage(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "shot", Args: json.RawMessage(`{}`)}}},
			{Content: "I can see the page."},
		},
	}
	reqs := captureRequests(provider)

	calls := 0
	a := New("viewer", "Views pages", provider, WithTools(imageTool{calls: &calls}))
	result, err := a.Execute(context.Background(), AgentTask{Input: "screenshot the page"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "I can see the page." {
		t.Errorf("Output = %q", result.Output)
	}

	if len(*reqs) != 2 {
		t.Fatalf("LLM requests = %d, want 2", len(*reqs))
	}
	second := (*reqs)[1]
	imgs := imageMessages(second)
	if len(imgs) != 1 {
		t.Fatalf("injected image messages = %d, want 1; messages: %+v", len(imgs), roles(second))
	}
	m := imgs[0]
	if len(m.Attachments) != 1 || m.Attachments[0].MimeType != "image/png" || string(m.Attachments[0].Data) != "png-1" {
		t.Errorf("injected attachments = %+v, want the tool's png bytes", m.Attachments)
	}
	// The image message must come after the tool-result message so providers
	// keep tool results adjacent to the assistant tool-call message.
	toolIdx, imgIdx := -1, -1
	for i, msg := range second {
		if msg.Role == core.RoleTool {
			toolIdx = i
		}
		if msg.Role == core.RoleUser && msg.Content == toolImageMessageContent {
			imgIdx = i
		}
	}
	if imgIdx < toolIdx {
		t.Errorf("image message at %d precedes tool result at %d", imgIdx, toolIdx)
	}
}

func TestToolImagePruningKeepsNewest(t *testing.T) {
	toolCall := func(id string) core.ChatResponse {
		return core.ChatResponse{ToolCalls: []core.ToolCall{{ID: id, Name: "shot", Args: json.RawMessage(`{}`)}}}
	}
	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{toolCall("1"), toolCall("2"), toolCall("3"), {Content: "done"}},
	}
	reqs := captureRequests(provider)

	calls := 0
	a := New("viewer", "Views pages", provider, WithTools(imageTool{calls: &calls}), WithLimits(Limits{MaxIter: 5}))
	if _, err := a.Execute(context.Background(), AgentTask{Input: "keep looking"}); err != nil {
		t.Fatal(err)
	}

	if len(*reqs) != 4 {
		t.Fatalf("LLM requests = %d, want 4", len(*reqs))
	}
	last := (*reqs)[3]
	imgs := imageMessages(last)
	if len(imgs) != 3 {
		t.Fatalf("image messages = %d, want 3", len(imgs))
	}
	if imgs[0].Content != toolImagePrunedContent || len(imgs[0].Attachments) != 0 {
		t.Errorf("oldest image message not pruned: %q with %d attachments", imgs[0].Content, len(imgs[0].Attachments))
	}
	if string(imgs[1].Attachments[0].Data) != "png-2" || string(imgs[2].Attachments[0].Data) != "png-3" {
		t.Errorf("newest two must keep attachments png-2/png-3, got %q/%q",
			imgs[1].Attachments[0].Data, imgs[2].Attachments[0].Data)
	}
}

func TestNonImageAttachmentsNotInjected(t *testing.T) {
	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "export", Args: json.RawMessage(`{}`)}}},
			{Content: "exported"},
		},
	}
	reqs := captureRequests(provider)

	a := New("exporter", "Exports", provider, WithTools(pdfTool{}))
	result, err := a.Execute(context.Background(), AgentTask{Input: "export the page"})
	if err != nil {
		t.Fatal(err)
	}

	if imgs := imageMessages((*reqs)[1]); len(imgs) != 0 {
		t.Errorf("pdf attachment was injected as image message: %+v", imgs)
	}
	// It still reaches the caller through the final result.
	if len(result.Attachments) != 1 || result.Attachments[0].MimeType != "application/pdf" {
		t.Errorf("final result attachments = %+v, want the pdf", result.Attachments)
	}
}

func roles(msgs []core.ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role)
	}
	return out
}
