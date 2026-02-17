package remember

import (
	"context"
	"encoding/json"
	"fmt"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/ingest"
)

// Tool saves information to the knowledge base.
type Tool struct {
	ingestor *ingest.Ingestor
}

// New creates a RememberTool backed by an Ingestor.
func New(store oasis.Store, embedding oasis.EmbeddingProvider) *Tool {
	return &Tool{
		ingestor: ingest.NewIngestor(store, embedding),
	}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{{
		Name:        "remember",
		Description: "Save information to the user's knowledge base. Use when the user explicitly asks to remember or save something.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","description":"The content to save"}},"required":["content"]}`),
	}}
}

func (t *Tool) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	var params struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	result, err := t.IngestText(ctx, params.Content, "message")
	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}
	return oasis.ToolResult{Content: result}, nil
}

// IngestText chunks, embeds, and stores text content. Exported for use by the App layer.
func (t *Tool) IngestText(ctx context.Context, content, source string) (string, error) {
	r, err := t.ingestor.IngestText(ctx, content, source, "")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved and indexed %d chunk(s) to knowledge base.", r.ChunkCount), nil
}

// IngestFile chunks, embeds, and stores a file's content. Exported for use by the App layer.
func (t *Tool) IngestFile(ctx context.Context, content, filename string) (string, error) {
	r, err := t.ingestor.IngestFile(ctx, []byte(content), filename)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("File %q ingested: %d chunk(s) indexed.", filename, r.ChunkCount), nil
}

// IngestURL ingests HTML content from a URL. Exported for use by the App layer.
func (t *Tool) IngestURL(ctx context.Context, html, sourceURL string) (string, error) {
	r, err := t.ingestor.IngestFile(ctx, []byte(html), sourceURL+".html")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("URL ingested: %d chunk(s) indexed from %s", r.ChunkCount, sourceURL), nil
}
