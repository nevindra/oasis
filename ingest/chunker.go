package ingest

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Chunker splits text into chunks suitable for embedding.
type Chunker interface {
	Chunk(text string) []string
}

// EmbedFunc embeds texts into vectors. Matches the EmbeddingProvider.Embed
// method signature so provider.Embed can be passed directly.
type EmbedFunc func(ctx context.Context, texts []string) ([][]float32, error)

// ContextChunker extends Chunker with context-aware chunking.
// Implementations that call external services (embedding APIs, databases)
// should implement this interface. The Ingestor uses ChunkContext when
// available, falling back to Chunk otherwise.
type ContextChunker interface {
	Chunker
	ChunkContext(ctx context.Context, text string) ([]string, error)
}

// --- ChunkerOption for configuring chunkers ---

// ChunkerOption configures a chunker implementation.
type ChunkerOption func(*chunkerConfig)

type chunkerConfig struct {
	maxTokens            int
	overlapTokens        int
	breakpointPercentile int
}

func defaultChunkerConfig() chunkerConfig {
	return chunkerConfig{maxTokens: 512, overlapTokens: 50, breakpointPercentile: 25}
}

// WithMaxTokens sets the maximum tokens per chunk (approximated as tokens*4 chars).
func WithMaxTokens(n int) ChunkerOption {
	return func(c *chunkerConfig) { c.maxTokens = n }
}

// WithOverlapTokens sets the overlap between chunks in tokens.
func WithOverlapTokens(n int) ChunkerOption {
	return func(c *chunkerConfig) { c.overlapTokens = n }
}

// WithBreakpointPercentile sets the similarity percentile for semantic split
// detection. Sentences where consecutive cosine similarity falls below this
// percentile become chunk boundaries. Default: 25 (split at the biggest 25%
// of similarity drops). Lower = fewer splits. Higher = more splits.
func WithBreakpointPercentile(p int) ChunkerOption {
	return func(c *chunkerConfig) { c.breakpointPercentile = p }
}

// --- RecursiveChunker ---

// RecursiveChunker splits text by paragraphs, then sentences, then words.
// It improves on basic sentence detection by skipping common abbreviations
// (Mr., Dr., vs., etc., e.g., i.e.), decimal numbers (3.14, $1.50),
// and handling CJK sentence-ending punctuation (。！？).
type RecursiveChunker struct {
	maxChars     int
	overlapChars int
}

// NewRecursiveChunker creates a RecursiveChunker with the given options.
func NewRecursiveChunker(opts ...ChunkerOption) *RecursiveChunker {
	cfg := defaultChunkerConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &RecursiveChunker{
		maxChars:     cfg.maxTokens * 4,
		overlapChars: cfg.overlapTokens * 4,
	}
}

// Chunk splits text into overlapping chunks.
func (rc *RecursiveChunker) Chunk(text string) []string {
	return chunkText(text, rc.maxChars, rc.overlapChars)
}

// chunkText splits text into overlapping chunks using recursive splitting.
// Strategy: split on paragraphs (\n\n), then sentences, then words.
func chunkText(text string, maxChars, overlapChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= maxChars {
		return []string{text}
	}

	segments := splitRecursive(text, maxChars)
	return mergeWithOverlap(segments, maxChars, overlapChars)
}

func splitRecursive(text string, maxChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= maxChars {
		return []string{text}
	}

	// Level 1: paragraph boundaries
	paragraphs := strings.Split(text, "\n\n")
	if len(paragraphs) > 1 {
		var segments []string
		for _, p := range paragraphs {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if len(p) <= maxChars {
				segments = append(segments, p)
			} else {
				segments = append(segments, splitOnSentences(p, maxChars)...)
			}
		}
		return segments
	}

	// Level 2: sentence boundaries
	sentenceSegments := splitOnSentences(text, maxChars)
	if len(sentenceSegments) > 1 {
		return sentenceSegments
	}

	// Level 3: word boundaries
	return splitOnWords(text, maxChars)
}

func splitOnSentences(text string, maxChars int) []string {
	boundaries := findSentenceBoundaries(text)
	if len(boundaries) == 0 {
		return splitOnWords(text, maxChars)
	}

	var segments []string
	start := 0
	lastGood := -1

	for _, boundary := range boundaries {
		candidate := text[start:boundary]
		if len(candidate) <= maxChars {
			lastGood = boundary
		} else {
			if lastGood > start {
				seg := strings.TrimSpace(text[start:lastGood])
				if seg != "" {
					if len(seg) <= maxChars {
						segments = append(segments, seg)
					} else {
						segments = append(segments, splitOnWords(seg, maxChars)...)
					}
				}
				start = lastGood
				candidate = text[start:boundary]
				if len(strings.TrimSpace(candidate)) <= maxChars {
					lastGood = boundary
				} else {
					lastGood = -1
				}
			} else {
				seg := strings.TrimSpace(text[start:boundary])
				if seg != "" {
					segments = append(segments, splitOnWords(seg, maxChars)...)
				}
				start = boundary
				lastGood = -1
			}
		}
	}

	if lastGood > start {
		seg := strings.TrimSpace(text[start:lastGood])
		if seg != "" {
			if len(seg) <= maxChars {
				segments = append(segments, seg)
			} else {
				segments = append(segments, splitOnWords(seg, maxChars)...)
			}
		}
		start = lastGood
	}

	remaining := strings.TrimSpace(text[start:])
	if remaining != "" {
		if len(remaining) <= maxChars {
			segments = append(segments, remaining)
		} else {
			segments = append(segments, splitOnWords(remaining, maxChars)...)
		}
	}

	return segments
}

// abbreviations that should NOT be treated as sentence boundaries.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true,
	"prof": true, "sr": true, "jr": true,
	"vs": true, "etc": true, "inc": true, "ltd": true,
	"e.g": true, "i.e": true, "viz": true, "al": true,
	"approx": true, "dept": true, "est": true,
	"fig": true, "no": true, "vol": true,
}

// isAbbreviation checks if the text ending at position pos (the '.')
// is a common abbreviation.
func isAbbreviation(text string, dotPos int) bool {
	// Walk backward to find the start of the word before the dot.
	start := dotPos
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if !unicode.IsLetter(r) && r != '.' {
			break
		}
		start -= size
	}
	word := strings.ToLower(text[start:dotPos])
	return abbreviations[word]
}

// isDecimalDot checks if the dot at position pos is part of a number (e.g. 3.14, $1.50).
func isDecimalDot(text string, dotPos int) bool {
	if dotPos == 0 || dotPos+1 >= len(text) {
		return false
	}
	prevByte := text[dotPos-1]
	nextByte := text[dotPos+1]
	return prevByte >= '0' && prevByte <= '9' && nextByte >= '0' && nextByte <= '9'
}

// findSentenceBoundaries returns byte positions suitable for splitting text
// at sentence boundaries. Handles ASCII punctuation (.!?) with abbreviation
// and decimal number awareness, plus CJK sentence-ending punctuation (。！？).
func findSentenceBoundaries(text string) []int {
	var boundaries []int
	runes := []rune(text)
	n := len(runes)

	// Build a byte-offset map for rune positions.
	byteOffsets := make([]int, n+1)
	off := 0
	for i, r := range runes {
		byteOffsets[i] = off
		off += utf8.RuneLen(r)
	}
	byteOffsets[n] = off

	for i := 0; i < n; i++ {
		r := runes[i]

		// CJK sentence-ending punctuation — always a boundary after.
		if r == '。' || r == '！' || r == '？' {
			boundaries = append(boundaries, byteOffsets[i+1])
			continue
		}

		if r != '.' && r != '!' && r != '?' {
			continue
		}

		dotBytePos := byteOffsets[i]

		// Skip decimal numbers like 3.14.
		if r == '.' && isDecimalDot(text, dotBytePos) {
			continue
		}

		// Skip abbreviations like Mr., Dr., etc.
		if r == '.' && isAbbreviation(text, dotBytePos) {
			continue
		}

		// Need whitespace or newline after punctuation.
		if i+1 < n && (runes[i+1] == ' ' || runes[i+1] == '\n') {
			if runes[i+1] == '\n' {
				boundaries = append(boundaries, byteOffsets[i+1])
			} else if i+2 < n && unicode.IsUpper(runes[i+2]) {
				boundaries = append(boundaries, byteOffsets[i+2])
			} else if i+2 >= n {
				boundaries = append(boundaries, byteOffsets[n])
			}
		}
	}
	return boundaries
}

func splitOnWords(text string, maxChars int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var segments []string
	var current strings.Builder

	for _, word := range words {
		if len(word) > maxChars {
			if current.Len() > 0 {
				segments = append(segments, strings.TrimSpace(current.String()))
				current.Reset()
			}
			for i := 0; i < len(word); i += maxChars {
				end := i + maxChars
				if end > len(word) {
					end = len(word)
				}
				segments = append(segments, word[i:end])
			}
			continue
		}

		needed := len(word)
		if current.Len() > 0 {
			needed = current.Len() + 1 + len(word)
		}

		if needed > maxChars {
			if current.Len() > 0 {
				segments = append(segments, strings.TrimSpace(current.String()))
				current.Reset()
			}
			current.WriteString(word)
		} else {
			if current.Len() > 0 {
				current.WriteByte(' ')
			}
			current.WriteString(word)
		}
	}

	if current.Len() > 0 {
		segments = append(segments, strings.TrimSpace(current.String()))
	}

	return segments
}

func mergeWithOverlap(segments []string, maxChars, overlapChars int) []string {
	if len(segments) == 0 {
		return nil
	}

	var chunks []string
	var current strings.Builder

	for _, seg := range segments {
		needed := len(seg)
		if current.Len() > 0 {
			needed = current.Len() + 1 + len(seg)
		}

		if needed <= maxChars {
			if current.Len() > 0 {
				current.WriteByte('\n')
			}
			current.WriteString(seg)
		} else {
			if current.Len() > 0 {
				chunk := current.String()
				chunks = append(chunks, chunk)

				overlap := getOverlapSuffix(chunk, overlapChars)
				current.Reset()
				if overlap != "" && len(overlap)+1+len(seg) <= maxChars {
					current.WriteString(overlap)
					current.WriteByte('\n')
				}
			}
			current.WriteString(seg)
		}
	}

	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	// Filter empty
	var result []string
	for _, c := range chunks {
		if strings.TrimSpace(c) != "" {
			result = append(result, c)
		}
	}
	return result
}

func getOverlapSuffix(text string, n int) string {
	if len(text) <= n {
		return text
	}
	suffix := text[len(text)-n:]
	if idx := strings.Index(suffix, " "); idx >= 0 {
		return strings.TrimSpace(suffix[idx+1:])
	}
	return strings.TrimSpace(suffix)
}
