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
	store     oasis.VectorStore
	embedding oasis.EmbeddingProvider
	pipeline  *ingest.Pipeline
}

// New creates a RememberTool. maxTokens defaults to 500, overlap to 50.
func New(store oasis.VectorStore, embedding oasis.EmbeddingProvider) *Tool {
	return &Tool{
		store:     store,
		embedding: embedding,
		pipeline:  ingest.NewPipeline(500, 50),
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
	r := t.pipeline.IngestText(content, source, "")

	if len(r.Chunks) > 0 {
		texts := make([]string, len(r.Chunks))
		for i, c := range r.Chunks {
			texts[i] = c.Content
		}

		embeddings, err := t.embedding.Embed(ctx, texts)
		if err != nil {
			return "", fmt.Errorf("embed: %w", err)
		}

		for i := range r.Chunks {
			if i < len(embeddings) {
				r.Chunks[i].Embedding = embeddings[i]
			}
		}
	}

	if err := t.store.StoreDocument(ctx, r.Document, r.Chunks); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return fmt.Sprintf("Saved and indexed %d chunk(s) to knowledge base.", len(r.Chunks)), nil
}

// IngestFile chunks, embeds, and stores a file's content. Exported for use by the App layer.
func (t *Tool) IngestFile(ctx context.Context, content, filename string) (string, error) {
	r := t.pipeline.IngestFile(content, filename)

	if len(r.Chunks) > 0 {
		texts := make([]string, len(r.Chunks))
		for i, c := range r.Chunks {
			texts[i] = c.Content
		}

		embeddings, err := t.embedding.Embed(ctx, texts)
		if err != nil {
			return "", fmt.Errorf("embed: %w", err)
		}

		for i := range r.Chunks {
			if i < len(embeddings) {
				r.Chunks[i].Embedding = embeddings[i]
			}
		}
	}

	if err := t.store.StoreDocument(ctx, r.Document, r.Chunks); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return fmt.Sprintf("File %q ingested: %d chunk(s) indexed.", filename, len(r.Chunks)), nil
}

// IngestURL ingests HTML content from a URL. Exported for use by the App layer.
func (t *Tool) IngestURL(ctx context.Context, html, sourceURL string) (string, error) {
	r := t.pipeline.IngestHTML(html, sourceURL)

	if len(r.Chunks) > 0 {
		texts := make([]string, len(r.Chunks))
		for i, c := range r.Chunks {
			texts[i] = c.Content
		}

		embeddings, err := t.embedding.Embed(ctx, texts)
		if err != nil {
			return "", fmt.Errorf("embed: %w", err)
		}

		for i := range r.Chunks {
			if i < len(embeddings) {
				r.Chunks[i].Embedding = embeddings[i]
			}
		}
	}

	if err := t.store.StoreDocument(ctx, r.Document, r.Chunks); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	return fmt.Sprintf("URL ingested: %d chunk(s) indexed from %s", len(r.Chunks), sourceURL), nil
}
