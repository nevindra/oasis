// Binary mcp-docs is an MCP server that exposes Oasis framework documentation
// to AI assistants (Claude Code, Cursor, Windsurf, etc.) via the Model Context
// Protocol over stdio.
//
// Usage in .mcp.json:
//
//	{
//	  "mcpServers": {
//	    "oasis": {
//	      "type": "stdio",
//	      "command": "go",
//	      "args": ["run", "github.com/nevindra/oasis/cmd/mcp-docs@latest"]
//	    }
//	  }
//	}
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path"
	"strings"

	"github.com/nevindra/oasis/docs"
	"github.com/nevindra/oasis/mcp"
)

// docEntry is a documentation resource registered with the MCP server.
type docEntry struct {
	uri         string
	name        string
	description string
	content     string
}

func main() {
	log.SetOutput(os.Stderr)
	log.SetPrefix("")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	allDocs := loadDocs()

	srv := mcp.New("oasis-docs", "0.3.0")

	// Register each doc as an MCP resource.
	for _, d := range allDocs {
		srv.AddResource(mcp.Resource{
			URI:         d.uri,
			Name:        d.name,
			Description: d.description,
			MimeType:    "text/markdown",
			Read:        func() string { return d.content },
		})
	}

	// Register search_docs tool.
	srv.AddTool(mcp.ToolHandler{
		Definition: mcp.ToolDefinition{
			Name:        "search_docs",
			Description: "Search Oasis framework documentation by keyword. Returns matching sections with their resource URIs.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query (case-insensitive keyword or phrase)",
					},
				},
				"required": []string{"query"},
			},
		},
		Execute: searchDocsHandler(allDocs),
	})

	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("mcp-docs: %v", err)
	}
}

// loadDocs reads all embedded documentation files and returns them as doc entries.
func loadDocs() []docEntry {
	var entries []docEntry

	sections := []struct {
		dir         string
		uriPrefix   string
		description string
	}{
		{"concepts", "oasis://concepts/", "Concept: "},
		{"guides", "oasis://guides/", "Guide: "},
		{"api", "oasis://api/", "API Reference: "},
		{"configuration", "oasis://configuration/", "Configuration: "},
		{"getting-started", "oasis://getting-started/", "Getting Started: "},
	}

	for _, sec := range sections {
		dirEntries, err := fs.ReadDir(docs.FS, sec.dir)
		if err != nil {
			continue
		}
		for _, e := range dirEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			content, err := fs.ReadFile(docs.FS, path.Join(sec.dir, e.Name()))
			if err != nil {
				continue
			}

			slug := strings.TrimSuffix(e.Name(), ".md")
			title := toTitle(slug)

			entries = append(entries, docEntry{
				uri:         sec.uriPrefix + slug,
				name:        title,
				description: sec.description + title,
				content:     string(content),
			})
		}
	}

	// Also include CONTRIBUTING.md as a top-level resource.
	if content, err := fs.ReadFile(docs.FS, "CONTRIBUTING.md"); err == nil {
		entries = append(entries, docEntry{
			uri:         "oasis://contributing",
			name:        "Contributing",
			description: "Engineering principles and coding conventions",
			content:     string(content),
		})
	}

	return entries
}

// searchDocsHandler returns a tool handler that searches across all docs.
func searchDocsHandler(allDocs []docEntry) func(context.Context, json.RawMessage) mcp.ToolCallResult {
	return func(_ context.Context, args json.RawMessage) mcp.ToolCallResult {
		var params struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return mcp.ErrorResult("invalid args: " + err.Error())
		}
		if params.Query == "" {
			return mcp.ErrorResult("query is required")
		}

		query := strings.ToLower(params.Query)
		var matches []string

		for _, d := range allDocs {
			lower := strings.ToLower(d.content)
			if !strings.Contains(lower, query) {
				continue
			}

			// Find matching lines for context.
			lines := strings.Split(d.content, "\n")
			var snippets []string
			for i, line := range lines {
				if strings.Contains(strings.ToLower(line), query) {
					start := max(i-1, 0)
					end := min(i+2, len(lines))
					snippet := strings.Join(lines[start:end], "\n")
					snippets = append(snippets, strings.TrimSpace(snippet))
					if len(snippets) >= 3 {
						break
					}
				}
			}

			entry := fmt.Sprintf("## %s (%s)\n\n%s", d.name, d.uri, strings.Join(snippets, "\n\n---\n\n"))
			matches = append(matches, entry)
		}

		if len(matches) == 0 {
			return mcp.TextResult(fmt.Sprintf("No results found for %q. Try a different keyword.", params.Query))
		}

		result := fmt.Sprintf("Found %d matching document(s):\n\n%s", len(matches), strings.Join(matches, "\n\n===\n\n"))
		return mcp.TextResult(result)
	}
}

// toTitle converts a slug like "input-handler" to "Input Handler".
func toTitle(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
