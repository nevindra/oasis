package ingest

import (
	"strings"
	"testing"
)

func TestPlainTextExtractorIdentity(t *testing.T) {
	e := PlainTextExtractor{}
	out, err := e.Extract([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
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

func TestMarkdownExtractorHeadings(t *testing.T) {
	e := MarkdownExtractor{}
	out, err := e.Extract([]byte("# Title\n## Subtitle"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Subtitle") {
		t.Errorf("headings not extracted: %q", out)
	}
	if strings.Contains(out, "#") {
		t.Error("heading markers not stripped")
	}
}

func TestMarkdownExtractorLinks(t *testing.T) {
	e := MarkdownExtractor{}
	out, err := e.Extract([]byte("Click [here](https://example.com) for more"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "here") {
		t.Error("link text lost")
	}
	if strings.Contains(out, "https://example.com") {
		t.Error("URL not stripped")
	}
}

func TestMarkdownExtractorEmphasis(t *testing.T) {
	e := MarkdownExtractor{}
	out, err := e.Extract([]byte("This is **bold** and *italic*"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bold") || !strings.Contains(out, "italic") {
		t.Errorf("emphasis text lost: %q", out)
	}
	if strings.Contains(out, "*") {
		t.Error("emphasis markers not stripped")
	}
}

func TestContentTypeFromExtension(t *testing.T) {
	if ContentTypeFromExtension("md") != TypeMarkdown {
		t.Error("expected TypeMarkdown")
	}
	if ContentTypeFromExtension("html") != TypeHTML {
		t.Error("expected TypeHTML")
	}
	if ContentTypeFromExtension("txt") != TypePlainText {
		t.Error("expected TypePlainText")
	}
}

func TestContentTypeFromExtensionNew(t *testing.T) {
	tests := []struct {
		ext  string
		want ContentType
	}{
		{"csv", TypeCSV},
		{"json", TypeJSON},
		{"docx", TypeDOCX},
		{"pdf", TypePDF},
		{"CSV", TypeCSV},
		{"JSON", TypeJSON},
		{"PDF", TypePDF},
	}
	for _, tt := range tests {
		if got := ContentTypeFromExtension(tt.ext); got != tt.want {
			t.Errorf("ContentTypeFromExtension(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

func TestHTMLExtractor(t *testing.T) {
	e := HTMLExtractor{}
	out, err := e.Extract([]byte("<p>Hello <b>world</b></p>"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "world") {
		t.Errorf("missing content: %q", out)
	}
}

func TestMarkdownExtractor(t *testing.T) {
	e := MarkdownExtractor{}
	out, err := e.Extract([]byte("# Title\n\nSome **bold** text"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "bold") {
		t.Errorf("content not extracted: %q", out)
	}
	if strings.Contains(out, "#") || strings.Contains(out, "**") {
		t.Error("formatting not stripped")
	}
}
