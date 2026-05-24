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

	// Build full-text search index.
	idx := newSearchIndex(allDocs)

	// Register search_docs tool.
	srv.AddTool(mcp.ToolHandler{
		Definition: mcp.ToolDefinition{
			Name:        "search_docs",
			Description: "Search Oasis framework documentation. Supports multi-word queries — results are ranked by relevance using BM25 scoring.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query (e.g. \"network multi-agent\", \"streaming memory\", \"tool\")",
					},
				},
				"required": []string{"query"},
			},
		},
		Execute: func(_ context.Context, args json.RawMessage) mcp.ToolCallResult {
			var params struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return mcp.ErrorResult("invalid args: " + err.Error())
			}
			if params.Query == "" {
				return mcp.ErrorResult("query is required")
			}
			results := idx.search(params.Query)
			return mcp.TextResult(formatResults(params.Query, results))
		},
	})

	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("mcp-docs: %v", err)
	}
}

// topicFolders lists every per-topic documentation directory in the new
// topic-grouped layout. Each folder contains three files (index.md, api.md,
// examples.md), exposed as oasis://<topic>/<file> resources.
var topicFolders = []string{
	"agent", "network", "workflow",
	"memory", "rag", "skills",
	"tools", "sandbox", "providers",
	"observability", "processors", "store",
}

// fileKinds maps the canonical per-topic filenames to their description label.
// Order is fixed so the resource list is stable across runs.
var fileKinds = []struct {
	file  string
	label string
}{
	{"index.md", "Concept"},
	{"api.md", "API Reference"},
	{"examples.md", "Examples"},
}

// loadDocs reads all embedded documentation files and returns them as doc entries.
func loadDocs() []docEntry {
	var entries []docEntry

	// Landing page.
	if content, err := fs.ReadFile(docs.FS, "index.md"); err == nil {
		entries = append(entries, docEntry{
			uri:         "oasis://index",
			name:        "Oasis Overview",
			description: "Landing page — what Oasis is and where to go next",
			content:     string(content),
		})
	}

	// Getting started — a single index.md inside the folder.
	if content, err := fs.ReadFile(docs.FS, "getting-started/index.md"); err == nil {
		entries = append(entries, docEntry{
			uri:         "oasis://getting-started",
			name:        "Getting Started",
			description: "Install Oasis and build your first agent",
			content:     string(content),
		})
	}

	// Per-topic resources: 12 topics × 3 files each.
	for _, topic := range topicFolders {
		title := toTitle(topic)
		for _, kind := range fileKinds {
			content, err := fs.ReadFile(docs.FS, path.Join(topic, kind.file))
			if err != nil {
				continue
			}
			slug := strings.TrimSuffix(kind.file, ".md")
			entries = append(entries, docEntry{
				uri:         "oasis://" + topic + "/" + slug,
				name:        title + " — " + kind.label,
				description: kind.label + ": " + title,
				content:     string(content),
			})
		}
	}

	return entries
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
