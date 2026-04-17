package oasis

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// toolSearchName is the public name of the auto-registered ToolSearch tool.
const toolSearchName = "ToolSearch"

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

// toolSearchTool is the internal Tool auto-registered by WithDeferredSchemas.
// It searches the agent's ToolRegistry for deferred tool definitions matching
// a keyword query and lazy-loads their schemas via ToolRegistry.EnsureSchema.
type toolSearchTool struct {
	registry *ToolRegistry
}

func newToolSearchTool(registry *ToolRegistry) *toolSearchTool {
	return &toolSearchTool{registry: registry}
}

func (t *toolSearchTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{
		Name:        toolSearchName,
		Description: toolSearchDescription,
		Parameters:  json.RawMessage(toolSearchInputSchema),
	}}
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

func (t *toolSearchTool) Execute(ctx context.Context, _ string, args json.RawMessage) (ToolResult, error) {
	var in toolSearchInput
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if in.Query == "" {
		return ToolResult{Error: "query must not be empty"}, nil
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
		return ToolResult{Error: "query contained no searchable words"}, nil
	}

	type scored struct {
		def   ToolDefinition
		score float64
	}
	defs := t.registry.DeferredDefinitions()
	var matches []scored
	for _, d := range defs {
		s := scoreToolMatch(qWords, d.Name, d.Description)
		if s > 0 {
			matches = append(matches, scored{def: d, score: s})
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
		if err := t.registry.EnsureSchema(ctx, m.def.Name); err != nil {
			mj.LoadError = err.Error()
		} else {
			for _, d := range t.registry.AllDefinitions() {
				if d.Name == m.def.Name {
					mj.InputSchema = d.Parameters
					break
				}
			}
		}
		out.Tools = append(out.Tools, mj)
	}
	if len(out.Tools) == 0 {
		out.Note = "No tools matched query. Try broader or different keywords."
	}

	content, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return ToolResult{Error: "format result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(content)}, nil
}

// tokenizeQuery splits a query into lowercase word tokens.
// Splits on whitespace and any non-letter/digit rune.
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

// scoreToolMatch scores how well the query words match a tool's name and
// description. Name matches are weighted 3x higher than description.
// Case-insensitive substring matching.
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

// deferredToolsPromptSection returns the system-prompt block prepended when
// WithDeferredSchemas is enabled. It explains the mcp__ deferral mechanism
// and instructs the model to call ToolSearch before invoking deferred tools.
func deferredToolsPromptSection() string {
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

// Compile-time assertion.
var _ Tool = (*toolSearchTool)(nil)
