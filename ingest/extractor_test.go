package ingest

import (
	"strings"
	"testing"
)

func TestExtractTextPlain(t *testing.T) {
	out := ExtractText("hello world", PlainText)
	if out != "hello world" {
		t.Errorf("expected hello world, got %q", out)
	}
}

func TestStripHTMLBasic(t *testing.T) {
	out := StripHTML("<p>Hello <b>world</b></p>")
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
		t.Errorf("missing content: %q", out)
	}
	if strings.Contains(out, "<") {
		t.Error("HTML tags not stripped")
	}
}

func TestStripHTMLEntities(t *testing.T) {
	out := StripHTML("Tom &amp; Jerry &lt;3")
	if !strings.Contains(out, "Tom & Jerry") {
		t.Errorf("entities not decoded: %q", out)
	}
}

func TestStripHTMLScript(t *testing.T) {
	out := StripHTML("<p>Hello</p><script>alert('xss')</script><p>World</p>")
	if strings.Contains(out, "alert") {
		t.Error("script content not stripped")
	}
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Error("text content lost")
	}
}

func TestStripMarkdownHeadings(t *testing.T) {
	out := ExtractText("# Title\n## Subtitle", Markdown)
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Subtitle") {
		t.Errorf("headings not extracted: %q", out)
	}
	if strings.Contains(out, "#") {
		t.Error("heading markers not stripped")
	}
}

func TestStripMarkdownLinks(t *testing.T) {
	out := ExtractText("Click [here](https://example.com) for more", Markdown)
	if !strings.Contains(out, "here") {
		t.Error("link text lost")
	}
	if strings.Contains(out, "https://example.com") {
		t.Error("URL not stripped")
	}
}

func TestStripMarkdownEmphasis(t *testing.T) {
	out := ExtractText("This is **bold** and *italic*", Markdown)
	if !strings.Contains(out, "bold") || !strings.Contains(out, "italic") {
		t.Errorf("emphasis text lost: %q", out)
	}
	if strings.Contains(out, "*") {
		t.Error("emphasis markers not stripped")
	}
}

func TestContentTypeFromExtension(t *testing.T) {
	if ContentTypeFromExtension("md") != Markdown {
		t.Error("expected Markdown")
	}
	if ContentTypeFromExtension("html") != HTML {
		t.Error("expected HTML")
	}
	if ContentTypeFromExtension("txt") != PlainText {
		t.Error("expected PlainText")
	}
}
