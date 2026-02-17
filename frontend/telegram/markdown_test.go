package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownBold(t *testing.T) {
	result := MarkdownToHTML("This is **bold** text")
	if !strings.Contains(result, "<b>bold</b>") {
		t.Errorf("expected <b>bold</b>, got: %s", result)
	}
}

func TestMarkdownItalic(t *testing.T) {
	result := MarkdownToHTML("This is *italic* text")
	if !strings.Contains(result, "<i>italic</i>") {
		t.Errorf("expected <i>italic</i>, got: %s", result)
	}
}

func TestMarkdownCode(t *testing.T) {
	result := MarkdownToHTML("Use `println!` here")
	if !strings.Contains(result, "<code>println!</code>") {
		t.Errorf("expected <code>println!</code>, got: %s", result)
	}
}

func TestMarkdownCodeBlock(t *testing.T) {
	result := MarkdownToHTML("```go\nfunc main() {}\n```")
	if !strings.Contains(result, "<pre>") {
		t.Errorf("expected <pre>, got: %s", result)
	}
	if !strings.Contains(result, "func main()") {
		t.Errorf("expected func main(), got: %s", result)
	}
	if !strings.Contains(result, "</pre>") {
		t.Errorf("expected </pre>, got: %s", result)
	}
	if !strings.Contains(result, "language-go") {
		t.Errorf("expected language-go, got: %s", result)
	}
}

func TestMarkdownLink(t *testing.T) {
	result := MarkdownToHTML("[click here](https://example.com)")
	if !strings.Contains(result, `<a href="https://example.com">click here</a>`) {
		t.Errorf("expected link HTML, got: %s", result)
	}
}

func TestMarkdownHeader(t *testing.T) {
	result := MarkdownToHTML("### Section Title")
	if !strings.Contains(result, "<b>Section Title</b>") {
		t.Errorf("expected <b>Section Title</b>, got: %s", result)
	}
}

func TestMarkdownHTMLEscape(t *testing.T) {
	result := MarkdownToHTML("1 < 2 & 3 > 0")
	if !strings.Contains(result, "&lt;") {
		t.Errorf("expected &lt;, got: %s", result)
	}
	if !strings.Contains(result, "&amp;") {
		t.Errorf("expected &amp;, got: %s", result)
	}
	if !strings.Contains(result, "&gt;") {
		t.Errorf("expected &gt;, got: %s", result)
	}
}

func TestMarkdownBlockquote(t *testing.T) {
	result := MarkdownToHTML("> This is a quote")
	if !strings.Contains(result, "<blockquote>") {
		t.Errorf("expected <blockquote>, got: %s", result)
	}
	if !strings.Contains(result, "This is a quote") {
		t.Errorf("expected quote text, got: %s", result)
	}
	if !strings.Contains(result, "</blockquote>") {
		t.Errorf("expected </blockquote>, got: %s", result)
	}
}

func TestMarkdownList(t *testing.T) {
	result := MarkdownToHTML("- first\n- second\n- third")
	if !strings.Contains(result, "\u2022 first") {
		t.Errorf("expected bullet first, got: %s", result)
	}
	if !strings.Contains(result, "\u2022 second") {
		t.Errorf("expected bullet second, got: %s", result)
	}
	if !strings.Contains(result, "\u2022 third") {
		t.Errorf("expected bullet third, got: %s", result)
	}
}

func TestMarkdownStrikethrough(t *testing.T) {
	result := MarkdownToHTML("This is ~~deleted~~ text")
	if !strings.Contains(result, "<s>deleted</s>") {
		t.Errorf("expected <s>deleted</s>, got: %s", result)
	}
}

func TestMarkdownMixed(t *testing.T) {
	input := "### Konsep Utama\n**Loss Aversion**: Manusia *takut* kehilangan."
	result := MarkdownToHTML(input)
	if !strings.Contains(result, "<b>Konsep Utama</b>") {
		t.Errorf("expected <b>Konsep Utama</b>, got: %s", result)
	}
	if !strings.Contains(result, "<b>Loss Aversion</b>") {
		t.Errorf("expected <b>Loss Aversion</b>, got: %s", result)
	}
	if !strings.Contains(result, "<i>takut</i>") {
		t.Errorf("expected <i>takut</i>, got: %s", result)
	}
}

func TestMarkdownOrderedList(t *testing.T) {
	result := MarkdownToHTML("1. first\n2. second\n3. third")
	if !strings.Contains(result, "1. first") {
		t.Errorf("expected 1. first, got: %s", result)
	}
	if !strings.Contains(result, "2. second") {
		t.Errorf("expected 2. second, got: %s", result)
	}
	if !strings.Contains(result, "3. third") {
		t.Errorf("expected 3. third, got: %s", result)
	}
}

func TestMarkdownCodeBlockNoLang(t *testing.T) {
	result := MarkdownToHTML("```\nplain code\n```")
	if !strings.Contains(result, "<pre><code>") {
		t.Errorf("expected <pre><code>, got: %s", result)
	}
	if !strings.Contains(result, "plain code") {
		t.Errorf("expected plain code, got: %s", result)
	}
}

func TestSplitMessage(t *testing.T) {
	// Short message: no split
	chunks := splitMessage("hello")
	if len(chunks) != 1 || chunks[0] != "hello" {
		t.Errorf("expected single chunk, got: %v", chunks)
	}

	// Long message: split
	long := strings.Repeat("a", 5000)
	chunks = splitMessage(long)
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got: %d", len(chunks))
	}
	if len(chunks[0]) != 4096 {
		t.Errorf("first chunk should be 4096, got: %d", len(chunks[0]))
	}

	// Split at newline boundary
	msg := strings.Repeat("x", 4000) + "\n" + strings.Repeat("y", 200)
	chunks = splitMessage(msg)
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks for %d chars, got: %d", len(msg), len(chunks))
	}
	if len(chunks) == 2 && len(chunks[0]) != 4001 {
		t.Errorf("first chunk should split at newline (4001 chars), got: %d", len(chunks[0]))
	}
}
