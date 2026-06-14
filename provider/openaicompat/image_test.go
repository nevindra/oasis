package openaicompat

import (
	"encoding/base64"
	"testing"
)

func TestParseResponseExtractsImage(t *testing.T) {
	// 1x1 transparent PNG.
	raw := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)

	resp := ChatResponse{
		Choices: []Choice{{
			Message: &ChoiceMessage{
				Role:    "assistant",
				Content: "here you go",
				Images: []ImageOut{{
					Type:     "image_url",
					ImageURL: &ImageURL{URL: uri},
				}},
			},
		}},
	}

	out, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if len(out.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(out.Attachments))
	}
	att := out.Attachments[0]
	if att.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", att.MimeType)
	}
	if string(att.Data) != string(raw) {
		t.Errorf("decoded image bytes mismatch")
	}
}

func TestWithModalitiesPromotesStringContent(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: StringContent("draw a fox")}},
	}
	WithModalities([]string{"image", "text"})(&req)

	if len(req.Modalities) != 2 {
		t.Fatalf("modalities not set: %v", req.Modalities)
	}
	if !req.Messages[0].Content.IsBlocks() {
		t.Fatalf("content not promoted to blocks: %+v", req.Messages[0].Content)
	}
	blocks := req.Messages[0].Content.Blocks
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "draw a fox" {
		t.Errorf("unexpected blocks: %+v", blocks)
	}
}

func TestWithModalitiesTextOnlyLeavesStringContent(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{{Role: "user", Content: StringContent("hello")}},
	}
	WithModalities([]string{"text"})(&req)

	if !req.Messages[0].Content.IsString() {
		t.Errorf("text-only request should keep string content, got %+v", req.Messages[0].Content)
	}
}
