package ingest

import (
	"strings"
	"unicode"
)

// ContentType determines how to extract plain text.
type ContentType int

const (
	PlainText ContentType = iota
	Markdown
	HTML
)

// ContentTypeFromExtension maps file extensions to content types.
func ContentTypeFromExtension(ext string) ContentType {
	switch strings.ToLower(ext) {
	case "md", "markdown":
		return Markdown
	case "html", "htm":
		return HTML
	default:
		return PlainText
	}
}

// ExtractText converts content to plain text based on its type.
func ExtractText(content string, ct ContentType) string {
	switch ct {
	case Markdown:
		return stripMarkdown(content)
	case HTML:
		return StripHTML(content)
	default:
		return content
	}
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
