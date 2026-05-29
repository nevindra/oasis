package dashscope_test

import (
	"context"
	"os"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/provider/catalog"
)

// TestLiveDashScopeViaCatalog exercises the full path: catalog registration →
// CreateProviderByID → ChatStream → downloaded image attachment.
// Network-gated: set DASHSCOPE_API_KEY to run.
func TestLiveDashScopeViaCatalog(t *testing.T) {
	key := os.Getenv("DASHSCOPE_API_KEY")
	if key == "" {
		t.Skip("DASHSCOPE_API_KEY not set")
	}

	cat := catalog.NewModelCatalog()
	if err := cat.Add("dashscope", key); err != nil {
		t.Fatalf("Add: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prov, err := cat.CreateProviderByID(ctx, "dashscope", "qwen-image-2.0")
	if err != nil {
		t.Fatalf("CreateProviderByID: %v", err)
	}

	resp, err := prov.ChatStream(ctx, oasis.ChatRequest{
		Messages:   []oasis.ChatMessage{oasis.UserMessage("a red fox sitting in a snowy forest, soft morning light")},
		Modalities: []string{"image", "text"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(resp.Attachments) == 0 {
		t.Fatalf("no attachments returned")
	}
	att := resp.Attachments[0]
	t.Logf("got attachment: mime=%s bytes=%d", att.MimeType, len(att.Data))
	if len(att.Data) < 1000 {
		t.Fatalf("image too small: %d bytes", len(att.Data))
	}
}

// TestLiveQwenCompatAutoRoute verifies that selecting a qwen-image model under
// the OpenAI-compatible "qwen" platform auto-routes to the native DashScope
// endpoint (matching the real user config: provider=qwen, model=qwen-image-2.0).
func TestLiveQwenCompatAutoRoute(t *testing.T) {
	key := os.Getenv("DASHSCOPE_API_KEY")
	if key == "" {
		t.Skip("DASHSCOPE_API_KEY not set")
	}

	cat := catalog.NewModelCatalog()
	if err := cat.Add("qwen", key); err != nil {
		t.Fatalf("Add qwen: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prov, err := cat.CreateProviderByID(ctx, "qwen", "qwen-image-2.0")
	if err != nil {
		t.Fatalf("CreateProviderByID: %v", err)
	}
	if prov.Name() != "dashscope" {
		t.Fatalf("expected auto-route to dashscope provider, got %q", prov.Name())
	}

	resp, err := prov.ChatStream(ctx, oasis.ChatRequest{
		Messages:   []oasis.ChatMessage{oasis.UserMessage("a serene mosque at sunset, islamic geometric patterns")},
		Modalities: []string{"image", "text"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(resp.Attachments) == 0 || len(resp.Attachments[0].Data) < 1000 {
		t.Fatalf("no/empty image returned")
	}
	t.Logf("auto-route OK: mime=%s bytes=%d", resp.Attachments[0].MimeType, len(resp.Attachments[0].Data))
}

// TestLiveWanImage verifies the Wan interleaved-streaming text-to-image path
// (provider=qwen auto-routes, model=wan2.7-image).
func TestLiveWanImage(t *testing.T) {
	key := os.Getenv("DASHSCOPE_API_KEY")
	if key == "" {
		t.Skip("DASHSCOPE_API_KEY not set")
	}

	cat := catalog.NewModelCatalog()
	if err := cat.Add("qwen", key); err != nil {
		t.Fatalf("Add qwen: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	prov, err := cat.CreateProviderByID(ctx, "qwen", "wan2.7-image")
	if err != nil {
		t.Fatalf("CreateProviderByID: %v", err)
	}
	if prov.Name() != "dashscope" {
		t.Fatalf("expected auto-route to dashscope, got %q", prov.Name())
	}

	resp, err := prov.ChatStream(ctx, oasis.ChatRequest{
		Messages:   []oasis.ChatMessage{oasis.UserMessage("a cute robot mascot, flat vector style")},
		Modalities: []string{"image", "text"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(resp.Attachments) == 0 || len(resp.Attachments[0].Data) < 1000 {
		t.Fatalf("no/empty image returned")
	}
	t.Logf("wan OK: images=%d mime=%s bytes=%d", len(resp.Attachments), resp.Attachments[0].MimeType, len(resp.Attachments[0].Data))
}
