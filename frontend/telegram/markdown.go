package telegram

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// MarkdownToHTML converts standard Markdown to Telegram-compatible HTML.
//
// Telegram supports: <b>, <i>, <s>, <u>, <code>, <pre>, <a href="">, <blockquote>, <tg-spoiler>.
// Headers are rendered as bold text. Unsupported elements pass through as text.
func MarkdownToHTML(md string) string {
	r := renderer.NewRenderer(
		renderer.WithNodeRenderers(
			util.Prioritized(&telegramRenderer{}, 1),
		),
	)

	gm := goldmark.New(
		goldmark.WithExtensions(extension.Strikethrough),
		goldmark.WithRenderer(r),
	)

	source := []byte(md)
	var buf bytes.Buffer
	if err := gm.Convert(source, &buf); err != nil {
		// Fallback: escape and return as-is.
		return htmlEscape(md)
	}

	return strings.TrimSpace(buf.String())
}

// htmlEscape escapes <, >, & for Telegram HTML.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramRenderer implements goldmark's renderer.NodeRenderer to produce
// Telegram-compatible HTML output.
type telegramRenderer struct {
	listCounter int
}

// RegisterFuncs registers render functions for each AST node kind.
func (r *telegramRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	// Block nodes
	reg.Register(ast.KindDocument, r.renderDocument)
	reg.Register(ast.KindHeading, r.renderHeading)
	reg.Register(ast.KindParagraph, r.renderParagraph)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindFencedCodeBlock, r.renderFencedCodeBlock)
	reg.Register(ast.KindCodeBlock, r.renderCodeBlock)
	reg.Register(ast.KindList, r.renderList)
	reg.Register(ast.KindListItem, r.renderListItem)
	reg.Register(ast.KindTextBlock, r.renderTextBlock)
	reg.Register(ast.KindThematicBreak, r.renderThematicBreak)
	reg.Register(ast.KindHTMLBlock, r.renderHTMLBlock)

	// Inline nodes
	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderString)
	reg.Register(ast.KindCodeSpan, r.renderCodeSpan)
	reg.Register(ast.KindEmphasis, r.renderEmphasis)
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindAutoLink, r.renderAutoLink)
	reg.Register(ast.KindImage, r.renderImage)
	reg.Register(ast.KindRawHTML, r.renderRawHTML)

	// Extension: strikethrough
	reg.Register(extast.KindStrikethrough, r.renderStrikethrough)
}

func (r *telegramRenderer) renderDocument(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderHeading(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("\n<b>")
	} else {
		_, _ = w.WriteString("</b>\n")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderParagraph(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		_, _ = w.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderBlockquote(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<blockquote>")
	} else {
		_, _ = w.WriteString("</blockquote>")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.FencedCodeBlock)
		lang := n.Language(source)
		if len(lang) > 0 {
			_, _ = fmt.Fprintf(w, "<pre><code class=\"language-%s\">", htmlEscape(string(lang)))
		} else {
			_, _ = w.WriteString("<pre><code>")
		}
		writeCodeBlockLines(w, source, node)
		_, _ = w.WriteString("</code></pre>")
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<pre><code>")
		writeCodeBlockLines(w, source, node)
		_, _ = w.WriteString("</code></pre>")
		return ast.WalkSkipChildren, nil
	}
	return ast.WalkContinue, nil
}

func writeCodeBlockLines(w util.BufWriter, source []byte, node ast.Node) {
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		_, _ = w.WriteString(htmlEscape(string(line.Value(source))))
	}
}

func (r *telegramRenderer) renderList(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.List)
	if entering {
		if n.IsOrdered() {
			r.listCounter = int(n.Start)
		} else {
			r.listCounter = 0
		}
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderListItem(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		parent := node.Parent().(*ast.List)
		if parent.IsOrdered() {
			_, _ = fmt.Fprintf(w, "%d. ", r.listCounter)
			r.listCounter++
		} else {
			_, _ = w.WriteString("\u2022 ")
		}
	} else {
		_, _ = w.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderTextBlock(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		// Only add newline if parent is not a list item (list items handle their own newlines)
		if node.Parent() != nil && node.Parent().Kind() != ast.KindListItem {
			_, _ = w.WriteString("\n")
		}
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderThematicBreak(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("\n---\n")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		lines := node.Lines()
		for i := 0; i < lines.Len(); i++ {
			line := lines.At(i)
			_, _ = w.Write(line.Value(source))
		}
	}
	return ast.WalkContinue, nil
}

// --- Inline renderers ---

func (r *telegramRenderer) renderText(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Text)
	segment := n.Segment
	value := segment.Value(source)

	_, _ = w.WriteString(htmlEscape(string(value)))

	if n.SoftLineBreak() {
		_, _ = w.WriteString("\n")
	}
	if n.HardLineBreak() {
		_, _ = w.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderString(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.String)
	_, _ = w.WriteString(htmlEscape(string(n.Value)))
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderCodeSpan(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<code>")
	} else {
		_, _ = w.WriteString("</code>")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderEmphasis(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Emphasis)
	if n.Level == 2 {
		// **bold**
		if entering {
			_, _ = w.WriteString("<b>")
		} else {
			_, _ = w.WriteString("</b>")
		}
	} else {
		// *italic*
		if entering {
			_, _ = w.WriteString("<i>")
		} else {
			_, _ = w.WriteString("</i>")
		}
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if entering {
		_, _ = fmt.Fprintf(w, "<a href=\"%s\">", htmlEscape(string(n.Destination)))
	} else {
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.AutoLink)
	if entering {
		url := string(n.URL(source))
		_, _ = fmt.Fprintf(w, "<a href=\"%s\">%s</a>", htmlEscape(url), htmlEscape(url))
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderImage(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	// Telegram doesn't support inline images in HTML, render as link
	n := node.(*ast.Image)
	if entering {
		_, _ = fmt.Fprintf(w, "<a href=\"%s\">", htmlEscape(string(n.Destination)))
	} else {
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.RawHTML)
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		_, _ = w.Write(seg.Value(source))
	}
	return ast.WalkContinue, nil
}

func (r *telegramRenderer) renderStrikethrough(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString("<s>")
	} else {
		_, _ = w.WriteString("</s>")
	}
	return ast.WalkContinue, nil
}
