package ingest

import (
	"regexp"
	"strings"
)

var _ Chunker = (*MarkdownChunker)(nil)

var headingRe = regexp.MustCompile(`(?m)^#{1,6}\s`)

// MarkdownChunker splits text at markdown heading boundaries.
// It preserves heading markers in chunks for better LLM context.
//
// Strategy:
//  1. Split on heading boundaries (^#{1,6} )
//  2. Heading + content = candidate chunk
//  3. If too large → fall back to RecursiveChunker for that section
//  4. If too small → merge with next section up to maxBytes
type MarkdownChunker struct {
	maxBytes  int
	fallback  *RecursiveChunker
}

// NewMarkdownChunker creates a MarkdownChunker with the given options.
// Options WithMaxTokens and WithOverlapTokens are respected.
func NewMarkdownChunker(opts ...ChunkerOption) *MarkdownChunker {
	cfg := defaultChunkerConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &MarkdownChunker{
		maxBytes: cfg.maxTokens * 4,
		fallback: NewRecursiveChunker(opts...),
	}
}

// Chunk splits markdown text into chunks respecting heading boundaries.
func (mc *MarkdownChunker) Chunk(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= mc.maxBytes {
		return []string{text}
	}

	sections := mc.splitSections(text)
	return mc.mergeSections(sections)
}

// splitSections splits markdown text into sections at heading boundaries.
func (mc *MarkdownChunker) splitSections(text string) []string {
	locs := headingRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return []string{text}
	}

	var sections []string
	// Content before first heading (if any).
	if locs[0][0] > 0 {
		pre := strings.TrimSpace(text[:locs[0][0]])
		if pre != "" {
			sections = append(sections, pre)
		}
	}

	for i, loc := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		section := strings.TrimSpace(text[loc[0]:end])
		if section != "" {
			sections = append(sections, section)
		}
	}

	return sections
}

// mergeSections merges small sections together and splits large ones.
func (mc *MarkdownChunker) mergeSections(sections []string) []string {
	var chunks []string
	var current strings.Builder

	for _, section := range sections {
		// Section too large on its own — split with fallback chunker.
		if len(section) > mc.maxBytes {
			// Flush current buffer first.
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, mc.fallback.Chunk(section)...)
			continue
		}

		needed := len(section)
		if current.Len() > 0 {
			needed = current.Len() + 2 + len(section) // "\n\n" separator
		}

		if needed <= mc.maxBytes {
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(section)
		} else {
			// Flush and start new.
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			current.WriteString(section)
		}
	}

	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}
