package ingest

import "testing"

func TestChunkTextEmpty(t *testing.T) {
	chunks := ChunkText("", DefaultChunkerConfig())
	if len(chunks) != 0 {
		t.Error("expected empty")
	}
}

func TestChunkTextShort(t *testing.T) {
	chunks := ChunkText("Hello, world!", DefaultChunkerConfig())
	if len(chunks) != 1 || chunks[0] != "Hello, world!" {
		t.Error("expected single chunk")
	}
}

func TestChunkTextRespectMax(t *testing.T) {
	cfg := ChunkerConfig{MaxChars: 100, OverlapChars: 20}
	text := ""
	for i := 0; i < 50; i++ {
		text += "This is a test. "
	}
	chunks := ChunkText(text, cfg)
	if len(chunks) <= 1 {
		t.Error("expected multiple chunks")
	}
	for _, c := range chunks {
		if len(c) > cfg.MaxChars {
			t.Errorf("chunk length %d exceeds max %d", len(c), cfg.MaxChars)
		}
	}
}

func TestChunkTextParagraphSplitting(t *testing.T) {
	cfg := ChunkerConfig{MaxChars: 100, OverlapChars: 10}
	text := "First paragraph with some content.\n\nSecond paragraph with other content.\n\nThird paragraph with more."
	chunks := ChunkText(text, cfg)
	if len(chunks) == 0 {
		t.Error("expected chunks")
	}
	for _, c := range chunks {
		if c == "" {
			t.Error("empty chunk")
		}
	}
}

func TestChunkTextWordSplitting(t *testing.T) {
	cfg := ChunkerConfig{MaxChars: 50, OverlapChars: 10}
	text := ""
	for i := 0; i < 100; i++ {
		text += "word "
	}
	chunks := ChunkText(text, cfg)
	if len(chunks) <= 1 {
		t.Error("expected multiple chunks")
	}
	for _, c := range chunks {
		if len(c) > cfg.MaxChars {
			t.Errorf("chunk length %d exceeds max %d", len(c), cfg.MaxChars)
		}
	}
}
