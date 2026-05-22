package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/mcp"
)

func TestTokenizeQuery(t *testing.T) {
	got := mcp.TokenizeQueryForTest("Create a GitHub issue")
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
	s1 := mcp.ScoreToolMatchForTest([]string{"issue"}, "mcp__gh__create_issue", "makes things")
	s2 := mcp.ScoreToolMatchForTest([]string{"issue"}, "mcp__gh__foo", "issue tracker helper")
	if s1 <= s2 {
		t.Errorf("s1=%v s2=%v; name match should outrank description", s1, s2)
	}
}

func TestScoreToolMatch_CaseInsensitive(t *testing.T) {
	s := mcp.ScoreToolMatchForTest([]string{"create"}, "CREATE_ISSUE", "Create issues")
	if s == 0 {
		t.Error("case-insensitive matching failed")
	}
}

func TestScoreToolMatch_NoMatchZero(t *testing.T) {
	s := mcp.ScoreToolMatchForTest([]string{"xyz"}, "create_issue", "create GitHub issues")
	if s != 0 {
		t.Errorf("expected 0, got %v", s)
	}
}

// fakeDeferredTool implements oasis.AnyTool + oasis.SchemaEnsurer for deferred-schema tests.
type fakeDeferredTool struct {
	name      string
	desc      string
	loaded    bool
	schema    json.RawMessage
	loadErr   error
	loadCount int
}

func (f *fakeDeferredTool) Name() string { return f.name }

func (f *fakeDeferredTool) Definition() oasis.ToolDefinition {
	d := oasis.ToolDefinition{Name: f.name, Description: f.desc}
	if f.loaded {
		d.Parameters = f.schema
	}
	return d
}
func (f *fakeDeferredTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (oasis.ToolResult, error) {
	return oasis.ToolResult{Content: json.RawMessage("ok")}, nil
}
func (f *fakeDeferredTool) EnsureSchema(_ context.Context) error {
	f.loadCount++
	if f.loadErr != nil {
		return f.loadErr
	}
	f.loaded = true
	return nil
}

func newRegistryWithFakeTools(tools ...oasis.AnyTool) *mcp.Registry {
	r := mcp.NewRegistry()
	for _, t := range tools {
		r.AddToolForTest(t)
	}
	return r
}

func TestToolSearch_Execute_HappyPath(t *testing.T) {
	r := newRegistryWithFakeTools(
		&fakeDeferredTool{name: "mcp__gh__create_issue", desc: "create an issue", schema: json.RawMessage(`{"type":"object"}`)},
		&fakeDeferredTool{name: "mcp__gh__list_repos", desc: "list repositories", schema: json.RawMessage(`{"type":"object"}`)},
	)
	ts := mcp.NewToolSearchToolForTest(r)

	args, _ := json.Marshal(map[string]interface{}{"query": "create issue"})
	res, err := ts.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("ToolResult.Error: %s", res.Error)
	}
	if !strings.Contains(string(res.Content), "mcp__gh__create_issue") {
		t.Errorf("content: %s", string(res.Content))
	}
	if !strings.Contains(string(res.Content), "inputSchema") {
		t.Errorf("expected schema in response: %s", string(res.Content))
	}
}

func TestToolSearch_Execute_EmptyQuery(t *testing.T) {
	r := newRegistryWithFakeTools(&fakeDeferredTool{name: "x"})
	ts := mcp.NewToolSearchToolForTest(r)
	args, _ := json.Marshal(map[string]interface{}{"query": ""})
	res, _ := ts.ExecuteRaw(context.Background(), args)
	if res.Error == "" {
		t.Error("empty query should error")
	}
}

func TestToolSearch_Execute_MaxResultsClamp(t *testing.T) {
	r := mcp.NewRegistry()
	for i := 0; i < 30; i++ {
		// Distinct names sharing the substring "tool" so all match a single keyword.
		r.AddToolForTest(&fakeDeferredTool{name: "mcp__s__tool_" + string(rune('a'+i%26)) + string(rune('A'+i%26)), desc: "test"})
	}
	ts := mcp.NewToolSearchToolForTest(r)

	for _, tc := range []struct {
		input, want int
	}{
		{0, 10}, {-1, 10}, {50, 25}, {5, 5},
	} {
		args, _ := json.Marshal(map[string]interface{}{"query": "tool", "max_results": tc.input})
		res, _ := ts.ExecuteRaw(context.Background(), args)
		n := strings.Count(string(res.Content), `"name":`)
		if n != tc.want {
			t.Errorf("max_results=%d: got %d tools, want %d", tc.input, n, tc.want)
		}
	}
}

func TestToolSearch_Execute_LoadsSchemaViaEnsure(t *testing.T) {
	tool := &fakeDeferredTool{name: "mcp__s__x", desc: "do x"}
	r := newRegistryWithFakeTools(tool)
	ts := mcp.NewToolSearchToolForTest(r)

	args, _ := json.Marshal(map[string]interface{}{"query": "do"})
	if _, err := ts.ExecuteRaw(context.Background(), args); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if tool.loadCount != 1 {
		t.Errorf("EnsureSchema not called; loadCount=%d", tool.loadCount)
	}
}

func TestToolSearch_Execute_NoMatchesReturnsNote(t *testing.T) {
	r := newRegistryWithFakeTools(&fakeDeferredTool{name: "mcp__s__alpha", desc: "alpha"})
	ts := mcp.NewToolSearchToolForTest(r)
	args, _ := json.Marshal(map[string]interface{}{"query": "nonexistent_keyword_xyzzy"})
	res, _ := ts.ExecuteRaw(context.Background(), args)
	if res.Error != "" {
		t.Errorf("should not error: %s", res.Error)
	}
	if !strings.Contains(string(res.Content), "No tools matched") {
		t.Errorf("content: %s", string(res.Content))
	}
}

func TestToolSearch_Definition(t *testing.T) {
	r := mcp.NewRegistry()
	ts := mcp.NewToolSearchToolForTest(r)
	def := ts.Definition()
	if def.Name != "ToolSearch" {
		t.Errorf("name: %s", def.Name)
	}
	if len(def.Parameters) == 0 {
		t.Error("schema empty")
	}
	if !strings.Contains(def.Description, "mcp__") {
		t.Errorf("description should mention mcp__ prefix: %s", def.Description)
	}
	if ts.Name() != "ToolSearch" {
		t.Errorf("Name() = %q", ts.Name())
	}
}
