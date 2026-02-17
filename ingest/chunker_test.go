package ingest

import (
	"strings"
	"testing"
)

func TestChunkTextEmpty(t *testing.T) {
	rc := NewRecursiveChunker()
	chunks := rc.Chunk("")
	if len(chunks) != 0 {
		t.Error("expected empty")
	}
}

func TestChunkTextShort(t *testing.T) {
	rc := NewRecursiveChunker()
	chunks := rc.Chunk("Hello, world!")
	if len(chunks) != 1 || chunks[0] != "Hello, world!" {
		t.Error("expected single chunk")
	}
}

func TestChunkTextRespectMax(t *testing.T) {
	rc := NewRecursiveChunker(WithMaxTokens(25), WithOverlapTokens(5))
	text := ""
	for i := 0; i < 50; i++ {
		text += "This is a test. "
	}
	chunks := rc.Chunk(text)
	if len(chunks) <= 1 {
		t.Error("expected multiple chunks")
	}
	maxChars := 25 * 4
	for _, c := range chunks {
		if len(c) > maxChars {
			t.Errorf("chunk length %d exceeds max %d", len(c), maxChars)
		}
	}
}

func TestChunkTextParagraphSplitting(t *testing.T) {
	rc := NewRecursiveChunker(WithMaxTokens(25), WithOverlapTokens(2))
	text := "First paragraph with some content.\n\nSecond paragraph with other content.\n\nThird paragraph with more."
	chunks := rc.Chunk(text)
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
	rc := NewRecursiveChunker(WithMaxTokens(12), WithOverlapTokens(2))
	text := ""
	for i := 0; i < 100; i++ {
		text += "word "
	}
	chunks := rc.Chunk(text)
	if len(chunks) <= 1 {
		t.Error("expected multiple chunks")
	}
	maxChars := 12 * 4
	for _, c := range chunks {
		if len(c) > maxChars {
			t.Errorf("chunk length %d exceeds max %d", len(c), maxChars)
		}
	}
}

// --- RecursiveChunker interface tests ---

func TestRecursiveChunkerImplementsInterface(t *testing.T) {
	var _ Chunker = (*RecursiveChunker)(nil)
}

func TestRecursiveChunkerWithOptions(t *testing.T) {
	rc := NewRecursiveChunker(WithMaxTokens(100), WithOverlapTokens(10))
	chunks := rc.Chunk("Hello, world!")
	if len(chunks) != 1 || chunks[0] != "Hello, world!" {
		t.Error("expected single chunk")
	}
}

// --- Improved sentence boundary tests ---

func TestSentenceBoundarySkipsAbbreviations(t *testing.T) {
	text := "Mr. Smith went to Washington. He met Dr. Jones there. They discussed the plan."
	boundaries := findSentenceBoundaries(text)

	// Should NOT split on "Mr." or "Dr." but SHOULD split after "Washington." and "there."
	rc := NewRecursiveChunker(WithMaxTokens(15), WithOverlapTokens(0))
	chunks := rc.Chunk(text)
	for _, c := range chunks {
		if strings.HasPrefix(c, "Smith") {
			t.Error("split on 'Mr.' abbreviation")
		}
		if strings.HasPrefix(c, "Jones") {
			t.Error("split on 'Dr.' abbreviation")
		}
	}
	_ = boundaries // ensure the function runs without panic
}

func TestSentenceBoundarySkipsDecimals(t *testing.T) {
	text := "The value is 3.14 and the cost is $1.50 per unit. Next sentence here."
	boundaries := findSentenceBoundaries(text)

	// Should NOT split on "3.14" or "1.50".
	for _, b := range boundaries {
		segment := text[:b]
		if strings.HasSuffix(strings.TrimSpace(segment), "3.1") || strings.HasSuffix(strings.TrimSpace(segment), "1.5") {
			t.Errorf("incorrectly split on decimal number at position %d", b)
		}
	}
}

func TestSentenceBoundaryCJKPunctuation(t *testing.T) {
	text := "这是第一句话。这是第二句话！这是第三句话？"
	boundaries := findSentenceBoundaries(text)
	if len(boundaries) < 3 {
		t.Errorf("expected at least 3 CJK boundaries, got %d", len(boundaries))
	}
}

func TestSentenceBoundaryEgIe(t *testing.T) {
	text := "Some items (e.g. apples, oranges) are fruit. Other items (i.e. carrots) are vegetables."
	boundaries := findSentenceBoundaries(text)

	// The text should have a boundary between the two sentences.
	if len(boundaries) == 0 {
		t.Error("expected at least one boundary")
	}
}
