package agent

import (
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestToolConfig_ApplyTo_Transforms(t *testing.T) {
	var c Config
	tc := ToolConfig{
		Transforms: map[string]core.ToolTransform{
			"get_customer": {Display: &core.SinkTransform{}},
		},
		TransformMatchers: []TransformMatcher{{
			Match:     func(n string) bool { return strings.HasPrefix(n, "db_") },
			Transform: core.ToolTransform{Transcript: &core.SinkTransform{}},
		}},
	}
	tc.ApplyTo(&c)

	if got, ok := c.ResolveToolTransform("get_customer"); !ok || got.Display == nil {
		t.Error("exact transform not overlaid")
	}
	if got, ok := c.ResolveToolTransform("db_x"); !ok || got.Transcript == nil {
		t.Error("matcher transform not overlaid")
	}
}

func TestToolConfig_ApplyTo_TransformsMerge(t *testing.T) {
	// Two ApplyTo calls merge into the same map (additive, like Policies).
	var c Config
	ToolConfig{Transforms: map[string]core.ToolTransform{
		"a": {Display: &core.SinkTransform{}}}}.ApplyTo(&c)
	ToolConfig{Transforms: map[string]core.ToolTransform{
		"b": {Model: &core.SinkTransform{}}}}.ApplyTo(&c)

	if _, ok := c.ResolveToolTransform("a"); !ok {
		t.Error("first transform lost after second ApplyTo")
	}
	if _, ok := c.ResolveToolTransform("b"); !ok {
		t.Error("second transform missing")
	}
}
