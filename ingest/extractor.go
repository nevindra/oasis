package ingest

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	TypePDF       ContentType = "application/pdf"
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
	case "pdf":
		return TypePDF
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

	i := 0
	for i < len(content) {
		r, size := utf8.DecodeRuneInString(content[i:])

		if r == '<' {
			inTag = true
			tagName.Reset()
			collectingTagName = true
			i += size
			continue
		}

		if inTag {
			if collectingTagName {
				if unicode.IsSpace(r) || r == '>' || (r == '/' && tagName.Len() > 0) {
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
					tagName.WriteRune(r)
				}
			}
			if r == '>' {
				inTag = false
			}
			i += size
			continue
		}

		if inScript || inStyle {
			i += size
			continue
		}

		if r == '&' {
			if decoded, skip := decodeEntity(content, i); skip > 0 {
				result.WriteString(decoded)
				i += skip
				continue
			}
		}

		result.WriteRune(r)
		i += size
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

func decodeEntity(content string, start int) (string, int) {
	if start >= len(content) || content[start] != '&' {
		return "", 0
	}
	maxLen := 12
	end := start + maxLen
	if end > len(content) {
		end = len(content)
	}
	for j := start + 1; j < end; j++ {
		ch := content[j]
		if ch == ';' {
			entity := content[start : j+1]
			consumed := j - start + 1
			if decoded, ok := namedEntities[entity]; ok {
				return decoded, consumed
			}
			// Numeric entities: &#123; or &#x7B;
			if len(entity) > 3 && entity[1] == '#' {
				inner := entity[2 : len(entity)-1]
				var codepoint int64
				var err error
				if inner[0] == 'x' || inner[0] == 'X' {
					codepoint, err = strconv.ParseInt(inner[1:], 16, 32)
				} else {
					codepoint, err = strconv.ParseInt(inner, 10, 32)
				}
				if err == nil && codepoint > 0 && codepoint <= 0x10FFFF {
					return string(rune(codepoint)), consumed
				}
			}
			return "", 0
		}
		// Only ASCII letters, digits, and '#' are valid in entity references.
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '#') {
			return "", 0
		}
	}
	return "", 0
}

var namedEntities = map[string]string{
	"&amp;":    "&",
	"&lt;":     "<",
	"&gt;":     ">",
	"&quot;":   "\"",
	"&#39;":    "'",
	"&apos;":   "'",
	"&nbsp;":   " ",
	"&mdash;":  "\u2014",
	"&ndash;":  "\u2013",
	"&copy;":   "\u00A9",
	"&reg;":    "\u00AE",
	"&trade;":  "\u2122",
	"&hellip;": "\u2026",
	"&laquo;":  "\u00AB",
	"&raquo;":  "\u00BB",
	"&bull;":   "\u2022",
	"&middot;": "\u00B7",
	"&times;":  "\u00D7",
	"&divide;": "\u00F7",
	"&deg;":    "\u00B0",
	"&euro;":   "\u20AC",
	"&pound;":  "\u00A3",
	"&yen;":    "\u00A5",
	"&cent;":   "\u00A2",
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
	emptyCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if result.Len() > 0 {
				emptyCount++
			}
		} else {
			if emptyCount > 0 {
				result.WriteByte('\n')
				if emptyCount > 1 {
					result.WriteByte('\n')
				}
			} else if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(trimmed)
			emptyCount = 0
		}
	}

	return strings.TrimSpace(result.String())
}
