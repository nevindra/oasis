package ingest

import (
	"strings"
	"testing"
)

func TestPipelineIngestTextBasic(t *testing.T) {
	p := NewPipeline(512, 50)
	r := p.IngestText("Hello, world!", "test", "Test Doc")
	if r.Document.Title != "Test Doc" {
		t.Errorf("wrong title: %s", r.Document.Title)
	}
	if r.Document.Source != "test" {
		t.Errorf("wrong source: %s", r.Document.Source)
	}
	if len(r.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(r.Chunks))
	}
	if r.Chunks[0].DocumentID != r.Document.ID {
		t.Error("chunk doesn't reference document")
	}
	if r.Chunks[0].Content != "Hello, world!" {
		t.Errorf("wrong chunk content: %s", r.Chunks[0].Content)
	}
}

func TestPipelineIngestHTML(t *testing.T) {
	p := NewPipeline(512, 50)
	r := p.IngestHTML("<p>Hello <b>world</b></p>", "https://example.com")
	if r.Document.Source != "https://example.com" {
		t.Error("wrong source")
	}
	if len(r.Chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if !strings.Contains(r.Chunks[0].Content, "Hello") {
		t.Error("content not extracted")
	}
}

func TestPipelineIngestFile(t *testing.T) {
	p := NewPipeline(512, 50)
	r := p.IngestFile("# Hello\n\nSome **bold** text.", "notes/readme.md")
	if r.Document.Title != "readme.md" {
		t.Errorf("wrong title: %s", r.Document.Title)
	}
	if len(r.Chunks) == 0 {
		t.Fatal("expected chunks")
	}
	if strings.Contains(r.Chunks[0].Content, "**") {
		t.Error("markdown not stripped")
	}
}

func TestPipelineMultipleChunks(t *testing.T) {
	p := &Pipeline{cfg: ChunkerConfig{MaxChars: 50, OverlapChars: 10}}
	text := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph that is a bit longer than fifty chars definitely."
	r := p.IngestText(text, "test", "")
	if len(r.Chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(r.Chunks))
	}
	for i, c := range r.Chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk %d has wrong index %d", i, c.ChunkIndex)
		}
	}
}
