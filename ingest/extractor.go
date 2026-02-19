package ingest

import (
	"strings"
	"unicode"

	oasis "github.com/nevindra/oasis"
)

// Extractor converts raw content to plain text.
type Extractor interface {
	Extract(content []byte) (string, error)
}

// ExtractResult holds extracted text and optional per-page/section metadata.
type ExtractResult struct {
	Text string
	Meta []PageMeta
}

// PageMeta holds metadata for a single page or section of extracted content.
// StartByte and EndByte mark the byte range in ExtractResult.Text that this
// metadata applies to, enabling the ingestor to assign metadata to chunks.
type PageMeta struct {
	PageNumber int
	Heading    string
	Images     []oasis.Image
	StartByte  int
	EndByte    int
}

// MetadataExtractor is an optional capability for extractors that produce
// structured metadata alongside text. If an Extractor also implements
// MetadataExtractor, the ingestor uses ExtractWithMeta instead of Extract.
type MetadataExtractor interface {
	ExtractWithMeta(content []byte) (ExtractResult, error)
}

// ContentType identifies the MIME type of content for extraction.
type ContentType string

const (
	TypePlainText ContentType = "text/plain"
	TypeHTML      ContentType = "text/html"
	TypeMarkdown  ContentType = "text/markdown"
	TypeCSV       ContentType = "text/csv"
	TypeJSON      ContentType = "application/json"
	TypeDOCX      ContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

// ContentTypeFromExtension maps file extensions to content types.
func ContentTypeFromExtension(ext string) ContentType {
	switch strings.ToLower(ext) {
	case "md", "markdown":
		return TypeMarkdown
	case "html", "htm":
		return TypeHTML
	case "csv":
		return TypeCSV
	case "json":
		return TypeJSON
	case "docx":
		return TypeDOCX
	default:
		return TypePlainText
	}
}

// --- Built-in extractors ---

// PlainTextExtractor returns content as-is.
type PlainTextExtractor struct{}

func (PlainTextExtractor) Extract(content []byte) (string, error) {
	return string(content), nil
}

// HTMLExtractor strips HTML tags, scripts, styles, and decodes entities.
type HTMLExtractor struct{}

func (HTMLExtractor) Extract(content []byte) (string, error) {
	return StripHTML(string(content)), nil
}

// MarkdownExtractor strips markdown formatting to produce plain text.
type MarkdownExtractor struct{}

func (MarkdownExtractor) Extract(content []byte) (string, error) {
	return stripMarkdown(string(content)), nil
}

// StripHTML removes HTML tags, scripts, styles, and decodes entities.
func StripHTML(content string) string {
	var result strings.Builder
	result.Grow(len(content))

	inTag := false
	inScript := false
	inStyle := false
	var tagName strings.Builder
	collectingTagName := false

	runes := []rune(content)
	n := len(runes)

	for i := 0; i < n; i++ {
		if runes[i] == '<' {
			inTag = true
			tagName.Reset()
			collectingTagName = true
			continue
		}

		if inTag {
			if collectingTagName {
				if unicode.IsSpace(runes[i]) || runes[i] == '>' || (runes[i] == '/' && tagName.Len() > 0) {
					collectingTagName = false
					lower := strings.ToLower(tagName.String())
					switch lower {
					case "script":
						inScript = true
					case "/script":
						inScript = false
					case "style":
						inStyle = true
					case "/style":
						inStyle = false
					}
					if isBlockTag(lower) {
						result.WriteByte('\n')
					}
				} else {
					tagName.WriteRune(runes[i])
				}
			}
			if runes[i] == '>' {
				inTag = false
				if collectingTagName {
					collectingTagName = false
					lower := strings.ToLower(tagName.String())
					switch lower {
					case "script":
						inScript = true
					case "/script":
						inScript = false
					case "style":
						inStyle = true
					case "/style":
						inStyle = false
					}
					if isBlockTag(lower) {
						result.WriteByte('\n')
					}
				}
			}
			continue
		}

		if inScript || inStyle {
			continue
		}

		if runes[i] == '&' {
			if decoded, skip := decodeEntity(runes, i); skip > 0 {
				result.WriteString(decoded)
				i += skip - 1
				continue
			}
		}

		result.WriteRune(runes[i])
	}

	return collapseWhitespace(result.String())
}

func isBlockTag(tag string) bool {
	tag = strings.TrimPrefix(tag, "/")
	switch tag {
	case "p", "div", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"li", "ul", "ol", "table", "tr", "blockquote", "pre",
		"section", "article", "header", "footer", "nav", "main":
		return true
	}
	return false
}

func decodeEntity(runes []rune, start int) (string, int) {
	if start >= len(runes) || runes[start] != '&' {
		return "", 0
	}
	maxLen := 10
	end := start + maxLen
	if end > len(runes) {
		end = len(runes)
	}
	for j := start + 1; j < end; j++ {
		if runes[j] == ';' {
			entity := string(runes[start : j+1])
			switch entity {
			case "&amp;":
				return "&", j - start + 1
			case "&lt;":
				return "<", j - start + 1
			case "&gt;":
				return ">", j - start + 1
			case "&quot;":
				return "\"", j - start + 1
			case "&#39;", "&apos;":
				return "'", j - start + 1
			case "&nbsp;":
				return " ", j - start + 1
			default:
				return "", 0
			}
		}
		if unicode.IsSpace(runes[j]) || runes[j] == '&' {
			return "", 0
		}
	}
	return "", 0
}

func stripMarkdown(content string) string {
	var result strings.Builder
	inCodeFence := false

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			result.WriteString(line)
			result.WriteByte('\n')
			continue
		}

		// Strip heading markers
		l := strings.TrimLeft(trimmed, "#")
		if len(l) < len(trimmed) {
			trimmed = strings.TrimSpace(l)
		}

		// Strip blockquote
		if strings.HasPrefix(trimmed, "> ") {
			trimmed = strings.TrimSpace(trimmed[2:])
		} else if strings.HasPrefix(trimmed, ">") {
			trimmed = strings.TrimSpace(trimmed[1:])
		}

		// Strip list markers
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
			trimmed = trimmed[2:]
		}

		// Strip bold/italic markers (simple approach)
		trimmed = strings.ReplaceAll(trimmed, "***", "")
		trimmed = strings.ReplaceAll(trimmed, "**", "")
		trimmed = strings.ReplaceAll(trimmed, "~~", "")
		// Single * and _ handled carefully — only strip paired ones
		trimmed = stripPairedChars(trimmed, '*')
		trimmed = stripPairedChars(trimmed, '_')

		// Strip links: [text](url) -> text
		trimmed = stripMarkdownLinks(trimmed)

		result.WriteString(trimmed)
		result.WriteByte('\n')
	}

	return collapseWhitespace(result.String())
}

func stripPairedChars(s string, ch byte) string {
	// Count occurrences; if even, remove all
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			count++
		}
	}
	if count >= 2 && count%2 == 0 {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] != ch {
				b.WriteByte(s[i])
			}
		}
		return b.String()
	}
	return s
}

func stripMarkdownLinks(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '[' {
			// Find ]
			j := strings.IndexByte(s[i:], ']')
			if j > 0 {
				closeBracket := i + j
				// Check for (url) after ]
				if closeBracket+1 < len(s) && s[closeBracket+1] == '(' {
					closeParen := strings.IndexByte(s[closeBracket+1:], ')')
					if closeParen > 0 {
						// Write just the link text
						result.WriteString(s[i+1 : closeBracket])
						i = closeBracket + 1 + closeParen + 1
						continue
					}
				}
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

func collapseWhitespace(text string) string {
	var result strings.Builder
	lines := strings.Split(text, "\n")
	lastWasEmpty := false
	emptyCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			emptyCount++
			if emptyCount <= 2 && result.Len() > 0 {
				lastWasEmpty = true
			}
		} else {
			if lastWasEmpty {
				result.WriteByte('\n')
				if emptyCount > 1 {
					result.WriteByte('\n')
				}
			} else if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(trimmed)
			lastWasEmpty = false
			emptyCount = 0
		}
	}

	return strings.TrimSpace(result.String())
}
