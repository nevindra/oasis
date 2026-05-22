package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	oasis "github.com/nevindra/oasis"
)

const ToolSearchName = "ToolSearch"

const toolSearchDescription = `Find available tools by keyword search. Many MCP tools are loaded on-demand to save context — their input schemas are NOT visible until you query them.

WHEN TO USE:
- You see a tool name prefixed with "mcp__" but want to call it (you need the schema first)
- You're not sure which tool fits the user's request — search by capability ("read pdf", "send email")
- You see a tool description that's promising but want full input details

HOW TO USE:
1. Call ToolSearch with a keyword query (2-5 words)
2. Inspect returned tool schemas
3. Then call the actual tool with correctly-formed arguments

EXAMPLE:
User: "Create a GitHub issue about the login bug"
You: ToolSearch(query="create github issue") → returns mcp__github__create_issue with schema → call it`

const toolSearchInputSchema = `{
    "type": "object",
    "properties": {
        "query": {
            "type": "string",
            "description": "Keywords to match against tool names and descriptions. Use 2-5 specific words like 'create issue github' or 'read pdf file'."
        },
        "max_results": {
            "type": "integer",
            "description": "Maximum tools to return (default 10, max 25)"
        }
    },
    "required": ["query"]
}`

const (
	toolSearchDefaultMax = 10
	toolSearchHardMax    = 25
)

// toolSearchTool is an AnyTool that searches the registry's tools for
// deferred (schema-empty) entries matching a keyword query, lazy-loading
// their schemas via SchemaEnsurer when a match is selected.
type toolSearchTool struct {
	reg *Registry
}

func newToolSearchTool(r *Registry) *toolSearchTool { return &toolSearchTool{reg: r} }

func (t *toolSearchTool) Name() string { return ToolSearchName }

func (t *toolSearchTool) Definition() oasis.ToolDefinition {
	return oasis.ToolDefinition{
		Name:        ToolSearchName,
		Description: toolSearchDescription,
		Parameters:  json.RawMessage(toolSearchInputSchema),
	}
}

type toolSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type toolSearchMatch struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	LoadError   string          `json:"loadError,omitempty"`
}

type toolSearchOutput struct {
	Tools []toolSearchMatch `json:"tools"`
	Note  string            `json:"note,omitempty"`
}

func (t *toolSearchTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	var in toolSearchInput
	if err := json.Unmarshal(args, &in); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if in.Query == "" {
		return oasis.ToolResult{Error: "query must not be empty"}, nil
	}

	n := in.MaxResults
	if n <= 0 {
		n = toolSearchDefaultMax
	}
	if n > toolSearchHardMax {
		n = toolSearchHardMax
	}

	qWords := tokenizeQuery(in.Query)
	if len(qWords) == 0 {
		return oasis.ToolResult{Error: "query contained no searchable words"}, nil
	}

	type scored struct {
		def   oasis.ToolDefinition
		tool  oasis.AnyTool
		score float64
	}

	// Snapshot of the registry's tool list under toolMu.
	t.reg.toolMu.RLock()
	candidates := make([]oasis.AnyTool, 0, len(t.reg.toolList))
	candidates = append(candidates, t.reg.toolList...)
	t.reg.toolMu.RUnlock()

	var matches []scored
	for _, tl := range candidates {
		d := tl.Definition()
		// Deferred = no params loaded yet.
		if len(d.Parameters) != 0 {
			continue
		}
		s := scoreToolMatch(qWords, d.Name, d.Description)
		if s > 0 {
			matches = append(matches, scored{def: d, tool: tl, score: s})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})
	if len(matches) > n {
		matches = matches[:n]
	}

	out := toolSearchOutput{Tools: make([]toolSearchMatch, 0, len(matches))}
	for _, m := range matches {
		mj := toolSearchMatch{Name: m.def.Name, Description: m.def.Description}
		if ensurer, ok := m.tool.(oasis.SchemaEnsurer); ok {
			if err := ensurer.EnsureSchema(ctx); err != nil {
				mj.LoadError = err.Error()
			} else {
				mj.InputSchema = m.tool.Definition().Parameters
			}
		}
		out.Tools = append(out.Tools, mj)
	}
	if len(out.Tools) == 0 {
		out.Note = "No tools matched query. Try broader or different keywords."
	}

	content, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return oasis.ToolResult{Error: "format result: " + err.Error()}, nil
	}
	return oasis.ToolResult{Content: oasis.JSONContent(content)}, nil
}

func tokenizeQuery(query string) []string {
	var words []string
	var cur strings.Builder
	for _, r := range strings.ToLower(query) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func scoreToolMatch(queryWords []string, toolName, description string) float64 {
	name := strings.ToLower(toolName)
	desc := strings.ToLower(description)
	var score float64
	for _, w := range queryWords {
		if strings.Contains(name, w) {
			score += 3.0
		}
		if strings.Contains(desc, w) {
			score += 1.0
		}
	}
	return score
}

// DeferredToolsPromptSection returns the system-prompt block that explains
// the mcp__ deferral mechanism to the LLM. Prepend it to your prompt when
// using WithDeferredSchemas:
//
//	prompt := mcp.DeferredToolsPromptSection() + "\n\n" + userPrompt
//	agent := oasis.NewLLMAgent("a", "d", p,
//	    oasis.WithPrompt(prompt),
//	    oasis.WithTools(reg.Tools()...),
//	)
func DeferredToolsPromptSection() string {
	return `<deferred-tools>
You have access to additional tools whose schemas are loaded on-demand.
Tools prefixed with "mcp__" appear in your tool list with name and description
but WITHOUT input schemas — these are deferred. Before calling any deferred tool,
use the ToolSearch tool to load its schema:

  ToolSearch(query="<keywords describing what you need>")

This returns the full schema. After receiving the schema, call the tool normally.
Tools NOT prefixed with "mcp__" have full schemas and can be called directly.
</deferred-tools>`
}
