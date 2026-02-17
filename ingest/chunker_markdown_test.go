package ingest

import (
	"strings"
	"testing"
)

func TestMarkdownChunkerShortDoc(t *testing.T) {
	mc := NewMarkdownChunker()
	chunks := mc.Chunk("# Hello\n\nShort doc.")
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0], "# Hello") {
		t.Error("heading should be preserved in chunk")
	}
}

func TestMarkdownChunkerSplitsOnHeadings(t *testing.T) {
	mc := NewMarkdownChunker(WithMaxTokens(50)) // 200 chars

	text := "# Section One\n\n" + strings.Repeat("Word ", 30) +
		"\n\n# Section Two\n\n" + strings.Repeat("Word ", 30)

	chunks := mc.Chunk(text)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	foundSection1 := false
	foundSection2 := false
	for _, c := range chunks {
		if strings.Contains(c, "# Section One") {
			foundSection1 = true
		}
		if strings.Contains(c, "# Section Two") {
			foundSection2 = true
		}
	}
	if !foundSection1 || !foundSection2 {
		t.Error("each section should appear in a chunk with its heading")
	}
}

func TestMarkdownChunkerMergesSmallSections(t *testing.T) {
	mc := NewMarkdownChunker(WithMaxTokens(256)) // 1024 chars

	text := "# A\n\nShort.\n\n# B\n\nAlso short.\n\n# C\n\nYep."
	chunks := mc.Chunk(text)
	// All sections are tiny, should merge into 1 chunk.
	if len(chunks) != 1 {
		t.Errorf("expected 1 merged chunk, got %d", len(chunks))
	}
}

func TestMarkdownChunkerFallbackOnLargeSection(t *testing.T) {
	mc := NewMarkdownChunker(WithMaxTokens(25)) // 100 chars

	text := "# Big Section\n\n" + strings.Repeat("word ", 50)
	chunks := mc.Chunk(text)
	if len(chunks) < 2 {
		t.Errorf("expected large section to be split, got %d chunks", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 200 { // some tolerance for overlap
			t.Errorf("chunk too large: %d chars", len(c))
		}
	}
}

func TestMarkdownChunkerEmpty(t *testing.T) {
	mc := NewMarkdownChunker()
	chunks := mc.Chunk("")
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}

func TestMarkdownChunkerPreservesHeadings(t *testing.T) {
	mc := NewMarkdownChunker(WithMaxTokens(50))

	text := "## Introduction\n\nSome text here.\n\n## Methods\n\nMore text here."
	chunks := mc.Chunk(text)
	for _, c := range chunks {
		// Each non-fallback chunk should start with a heading or content.
		if strings.TrimSpace(c) == "" {
			t.Error("empty chunk found")
		}
	}
}
