package main

import (
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"simple words", "hello world", []string{"hello", "world"}},
		{"mixed case", "Hello World", []string{"hello", "world"}},
		{"hyphenated", "multi-agent system", []string{"multi-agent", "multi", "agent", "system"}},
		{"punctuation", "foo, bar. baz!", []string{"foo", "bar", "baz"}},
		{"short words filtered", "a I go do it", []string{"go", "do", "it"}},
		{"numbers", "v0 http2 grpc", []string{"v0", "http2", "grpc"}},
		{"markdown heading", "## Network Agent", []string{"network", "agent"}},
		{"leading hyphens trimmed", "--flag --verbose", []string{"flag", "verbose"}},
		{"empty string", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenize(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSearchSingleTerm(t *testing.T) {
	docs := []docEntry{
		{uri: "oasis://concepts/network", name: "Network", content: "# Network\n\nA Network routes tasks to multiple agents."},
		{uri: "oasis://concepts/tool", name: "Tool", content: "# Tool\n\nTools let agents interact with external systems."},
	}

	idx := newSearchIndex(docs)
	results := idx.search("network")

	if len(results) == 0 {
		t.Fatal("expected results for 'network'")
	}
	if results[0].entry.uri != "oasis://concepts/network" {
		t.Errorf("top result = %q, want oasis://concepts/network", results[0].entry.uri)
	}
}

func TestSearchMultiWord(t *testing.T) {
	docs := []docEntry{
		{uri: "oasis://concepts/network", name: "Network", content: "# Network\n\nA Network routes tasks to multiple agents.\nSupports multi-agent routing."},
		{uri: "oasis://concepts/tool", name: "Tool", content: "# Tool\n\nTools let agents interact with external systems."},
		{uri: "oasis://concepts/store", name: "Store", content: "# Store\n\nPersistent storage for conversations."},
	}

	idx := newSearchIndex(docs)
	results := idx.search("network multi-agent routing")

	if len(results) == 0 {
		t.Fatal("expected results for 'network multi-agent routing'")
	}
	// Network doc should rank highest — it contains all query terms.
	if results[0].entry.uri != "oasis://concepts/network" {
		t.Errorf("top result = %q, want oasis://concepts/network", results[0].entry.uri)
	}
}

func TestSearchNoResults(t *testing.T) {
	docs := []docEntry{
		{uri: "oasis://concepts/tool", name: "Tool", content: "# Tool\n\nTools let agents interact with systems."},
	}

	idx := newSearchIndex(docs)
	results := idx.search("nonexistent term xyzzy")

	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	idx := newSearchIndex([]docEntry{{content: "some content"}})
	results := idx.search("")

	if results != nil {
		t.Errorf("expected nil for empty query, got %v", results)
	}
}

func TestSearchHeadingBoost(t *testing.T) {
	docs := []docEntry{
		{uri: "a", name: "A", content: "# Streaming\n\nThis doc is about streaming tokens."},
		{uri: "b", name: "B", content: "# Other\n\nThis doc mentions streaming once in the body."},
	}

	idx := newSearchIndex(docs)
	results := idx.search("streaming")

	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Doc A has "streaming" in the heading, should rank higher.
	if results[0].entry.uri != "a" {
		t.Errorf("expected doc with heading match to rank first, got %q", results[0].entry.uri)
	}
	if results[0].score <= results[1].score {
		t.Errorf("heading doc score (%.2f) should be > body-only score (%.2f)", results[0].score, results[1].score)
	}
}

func TestSearchRankingByTermOverlap(t *testing.T) {
	docs := []docEntry{
		{uri: "full", name: "Full", content: "streaming conversation memory is important for agents"},
		{uri: "partial", name: "Partial", content: "streaming tokens over a channel"},
		{uri: "none", name: "None", content: "this doc is about tools and providers"},
	}

	idx := newSearchIndex(docs)
	results := idx.search("streaming conversation memory")

	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// "full" contains all 3 terms, should rank above "partial" which has only 1.
	if results[0].entry.uri != "full" {
		t.Errorf("top result = %q, want 'full'", results[0].entry.uri)
	}
}

func TestExtractSnippetIncludesHeading(t *testing.T) {
	content := "# My Heading\n\nSome intro text.\n\n## Relevant Section\n\nThis line matches the query terms.\n\nMore content here."
	terms := map[string]bool{"matches": true, "query": true}

	snippet := extractSnippet(content, terms)

	if !strings.Contains(snippet, "Relevant Section") {
		t.Errorf("snippet should include nearest heading, got:\n%s", snippet)
	}
	if !strings.Contains(snippet, "matches the query") {
		t.Errorf("snippet should include matching line, got:\n%s", snippet)
	}
}

func TestFormatResultsEmpty(t *testing.T) {
	out := formatResults("test", nil)
	if !strings.Contains(out, "No results found") {
		t.Errorf("expected 'No results found' message, got: %s", out)
	}
}

func TestFormatResultsWithHits(t *testing.T) {
	results := []searchResult{
		{entry: docEntry{name: "Network", uri: "oasis://concepts/network"}, score: 5.0, snippet: "some snippet"},
	}
	out := formatResults("network", results)

	if !strings.Contains(out, "Found 1 matching") {
		t.Errorf("expected match count, got: %s", out)
	}
	if !strings.Contains(out, "Network") {
		t.Errorf("expected doc name in output, got: %s", out)
	}
}
