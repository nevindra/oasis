package runtime

import (
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestResolveToolTransform_ExactWins(t *testing.T) {
	exact := core.ToolTransform{Display: &core.SinkTransform{}}
	c := &Config{
		ToolTransforms: map[string]core.ToolTransform{"db_query": exact},
	}
	c.AddToolTransformMatcher(func(n string) bool { return strings.HasPrefix(n, "db_") },
		core.ToolTransform{Transcript: &core.SinkTransform{}})

	got, ok := c.ResolveToolTransform("db_query")
	if !ok {
		t.Fatal("expected a transform")
	}
	if got.Display == nil || got.Transcript != nil {
		t.Error("exact-name entry must win over matcher")
	}
}

func TestResolveToolTransform_MatcherFallback(t *testing.T) {
	c := &Config{}
	c.AddToolTransformMatcher(func(n string) bool { return strings.HasPrefix(n, "db_") },
		core.ToolTransform{Transcript: &core.SinkTransform{}})

	got, ok := c.ResolveToolTransform("db_write")
	if !ok || got.Transcript == nil {
		t.Error("matcher should apply to db_write")
	}
	if _, ok := c.ResolveToolTransform("greet"); ok {
		t.Error("non-matching tool should resolve to no transform")
	}
}

func TestResolveToolTransform_NilConfig(t *testing.T) {
	var c *Config
	if _, ok := c.ResolveToolTransform("x"); ok {
		t.Error("nil config must return false")
	}
}
