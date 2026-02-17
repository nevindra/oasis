package ingest

import (
	"strings"
	"unicode"
)

// ChunkerConfig controls text chunking behavior.
type ChunkerConfig struct {
	MaxChars     int // Max characters per chunk (default 2048 ≈ 512 tokens)
	OverlapChars int // Overlap between chunks (default 200 ≈ 50 tokens)
}

// DefaultChunkerConfig returns sensible defaults (512 tokens, 50 token overlap).
func DefaultChunkerConfig() ChunkerConfig {
	return ChunkerConfig{MaxChars: 2048, OverlapChars: 200}
}

// ChunkText splits text into overlapping chunks using recursive splitting.
// Strategy: split on paragraphs (\n\n), then sentences, then words.
func ChunkText(text string, cfg ChunkerConfig) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= cfg.MaxChars {
		return []string{text}
	}

	segments := splitRecursive(text, cfg.MaxChars)
	return mergeWithOverlap(segments, cfg)
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

// findSentenceBoundaries returns byte positions after ". X" / "! X" / "? X"
// where X is uppercase, or after ".\n".
func findSentenceBoundaries(text string) []int {
	var boundaries []int
	bytes := []byte(text)
	n := len(bytes)

	for i := 0; i < n; i++ {
		if bytes[i] == '.' || bytes[i] == '!' || bytes[i] == '?' {
			if i+1 < n && (bytes[i+1] == ' ' || bytes[i+1] == '\n') {
				if bytes[i+1] == '\n' {
					boundaries = append(boundaries, i+1)
				} else if i+2 < n && unicode.IsUpper(rune(bytes[i+2])) {
					boundaries = append(boundaries, i+2)
				} else if i+2 >= n {
					boundaries = append(boundaries, n)
				}
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

func mergeWithOverlap(segments []string, cfg ChunkerConfig) []string {
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

		if needed <= cfg.MaxChars {
			if current.Len() > 0 {
				current.WriteByte('\n')
			}
			current.WriteString(seg)
		} else {
			if current.Len() > 0 {
				chunk := current.String()
				chunks = append(chunks, chunk)

				overlap := getOverlapSuffix(chunk, cfg.OverlapChars)
				current.Reset()
				if overlap != "" && len(overlap)+1+len(seg) <= cfg.MaxChars {
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
