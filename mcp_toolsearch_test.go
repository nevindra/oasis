package oasis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenizeQuery(t *testing.T) {
	got := tokenizeQuery("Create a GitHub issue")
	want := []string{"create", "a", "github", "issue"}
	if len(got) != len(want) {
		t.Fatalf("len: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got %q want %q", got[i], want[i])
		}
	}
}

func TestScoreToolMatch_NameWeightedHigher(t *testing.T) {
	s1 := scoreToolMatch([]string{"issue"}, "mcp__gh__create_issue", "makes things")
	s2 := scoreToolMatch([]string{"issue"}, "mcp__gh__foo", "issue tracker helper")
	if s1 <= s2 {
		t.Errorf("s1=%v s2=%v; name match should outrank description", s1, s2)
	}
}

func TestScoreToolMatch_CaseInsensitive(t *testing.T) {
	s := scoreToolMatch([]string{"create"}, "CREATE_ISSUE", "Create issues")
	if s == 0 {
		t.Error("case-insensitive matching failed")
	}
}

func TestScoreToolMatch_NoMatchZero(t *testing.T) {
	s := scoreToolMatch([]string{"xyz"}, "create_issue", "create GitHub issues")
	if s != 0 {
		t.Errorf("expected 0, got %v", s)
	}
}

// fakeDeferredTool is a Tool that participates in deferred-schema loading.
type fakeDeferredTool struct {
	name      string
	desc      string
	loaded    bool
	schema    json.RawMessage
	loadErr   error
	loadCount int
}

func (f *fakeDeferredTool) Definitions() []ToolDefinition {
	d := ToolDefinition{Name: f.name, Description: f.desc}
	if f.loaded {
		d.Parameters = f.schema
	}
	return []ToolDefinition{d}
}
func (f *fakeDeferredTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "ok"}, nil
}
func (f *fakeDeferredTool) EnsureSchema(_ context.Context) error {
	f.loadCount++
	if f.loadErr != nil {
		return f.loadErr
	}
	f.loaded = true
	return nil
}

func newRegistryWith(tools ...Tool) *ToolRegistry {
	r := NewToolRegistry()
	for _, t := range tools {
		r.Add(t)
	}
	return r
}

func TestToolSearch_Execute_HappyPath(t *testing.T) {
	r := newRegistryWith(
		&fakeDeferredTool{name: "mcp__gh__create_issue", desc: "create an issue", schema: json.RawMessage(`{"type":"object"}`)},
		&fakeDeferredTool{name: "mcp__gh__list_repos", desc: "list repositories", schema: json.RawMessage(`{"type":"object"}`)},
	)
	ts := newToolSearchTool(r)

	args, _ := json.Marshal(map[string]interface{}{"query": "create issue"})
	res, err := ts.Execute(context.Background(), toolSearchName, args)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("ToolResult.Error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "mcp__gh__create_issue") {
		t.Errorf("content: %s", res.Content)
	}
	if !strings.Contains(res.Content, "inputSchema") {
		t.Errorf("expected schema in response: %s", res.Content)
	}
}

func TestToolSearch_Execute_EmptyQuery(t *testing.T) {
	r := newRegistryWith(&fakeDeferredTool{name: "x"})
	ts := newToolSearchTool(r)
	args, _ := json.Marshal(map[string]interface{}{"query": ""})
	res, _ := ts.Execute(context.Background(), toolSearchName, args)
	if res.Error == "" {
		t.Error("empty query should error")
	}
}

func TestToolSearch_Execute_MaxResultsClamp(t *testing.T) {
	r := NewToolRegistry()
	for i := 0; i < 30; i++ {
		// Distinct names sharing the substring "tool" so all match a single keyword.
		r.Add(&fakeDeferredTool{name: "mcp__s__tool_" + string(rune('a'+i%26)) + string(rune('A'+i%26)), desc: "test"})
	}
	ts := newToolSearchTool(r)

	for _, tc := range []struct {
		input, want int
	}{
		{0, 10}, {-1, 10}, {50, 25}, {5, 5},
	} {
		args, _ := json.Marshal(map[string]interface{}{"query": "tool", "max_results": tc.input})
		res, _ := ts.Execute(context.Background(), toolSearchName, args)
		n := strings.Count(res.Content, `"name":`)
		if n != tc.want {
			t.Errorf("max_results=%d: got %d tools, want %d", tc.input, n, tc.want)
		}
	}
}

func TestToolSearch_Execute_LoadsSchemaViaEnsure(t *testing.T) {
	tool := &fakeDeferredTool{name: "mcp__s__x", desc: "do x"}
	r := newRegistryWith(tool)
	ts := newToolSearchTool(r)

	args, _ := json.Marshal(map[string]interface{}{"query": "do"})
	if _, err := ts.Execute(context.Background(), toolSearchName, args); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if tool.loadCount != 1 {
		t.Errorf("EnsureSchema not called; loadCount=%d", tool.loadCount)
	}
}

func TestToolSearch_Execute_NoMatchesReturnsNote(t *testing.T) {
	r := newRegistryWith(&fakeDeferredTool{name: "mcp__s__alpha", desc: "alpha"})
	ts := newToolSearchTool(r)
	args, _ := json.Marshal(map[string]interface{}{"query": "nonexistent_keyword_xyzzy"})
	res, _ := ts.Execute(context.Background(), toolSearchName, args)
	if res.Error != "" {
		t.Errorf("should not error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "No tools matched") {
		t.Errorf("content: %s", res.Content)
	}
}

func TestToolSearch_Definitions(t *testing.T) {
	r := NewToolRegistry()
	ts := newToolSearchTool(r)
	defs := ts.Definitions()
	if len(defs) != 1 {
		t.Fatalf("want 1 def, got %d", len(defs))
	}
	def := defs[0]
	if def.Name != "ToolSearch" {
		t.Errorf("name: %s", def.Name)
	}
	if len(def.Parameters) == 0 {
		t.Error("schema empty")
	}
	if !strings.Contains(def.Description, "mcp__") {
		t.Errorf("description should mention mcp__ prefix: %s", def.Description)
	}
}
