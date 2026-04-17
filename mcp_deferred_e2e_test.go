package oasis

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

// T5: parallel ToolSearch calls for the same deferred tool — should not error
// or panic. Strict "exactly one ListTools" assertion would require server-side
// instrumentation; here we assert behavioral correctness (all calls succeed).
func TestE2E_ParallelEnsureSchema_ThunderingHerd(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", Description: "echos", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)
	reg.SetDeferredMode(&deferConfig{enabled: true})
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	ts := newToolSearchTool(reg.toolReg)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args, _ := json.Marshal(map[string]interface{}{"query": "echos"})
			res, err := ts.Execute(context.Background(), toolSearchName, args)
			if err != nil {
				t.Errorf("exec: %v", err)
			}
			if res.Error != "" {
				t.Errorf("res.Error: %s", res.Error)
			}
		}()
	}
	wg.Wait()

	// Schema should now be loaded.
	for _, d := range reg.toolReg.AllDefinitions() {
		if d.Name == "mcp__s__echo" && len(d.Parameters) == 0 {
			t.Error("schema should be loaded after parallel EnsureSchema")
		}
	}
}

// T16: empty query → informative error.
func TestE2E_ToolSearch_EmptyQuery(t *testing.T) {
	reg := newTestRegistry(t)
	ts := newToolSearchTool(reg.toolReg)
	args, _ := json.Marshal(map[string]interface{}{"query": ""})
	res, _ := ts.Execute(context.Background(), toolSearchName, args)
	if res.Error == "" {
		t.Error("expected error")
	}
}

// T17: max_results=0 → defaults to 10.
func TestE2E_ToolSearch_MaxResultsZero(t *testing.T) {
	fake := mcptest.New()
	tools := make([]mcp.ToolDefinition, 15)
	for i := range tools {
		tools[i] = mcp.ToolDefinition{Name: "tool_" + string(rune('a'+i)), Description: "do things"}
	}
	fake.Tools = tools
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)
	reg.SetDeferredMode(&deferConfig{enabled: true})
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	ts := newToolSearchTool(reg.toolReg)
	args, _ := json.Marshal(map[string]interface{}{"query": "things", "max_results": 0})
	res, _ := ts.Execute(context.Background(), toolSearchName, args)

	n := strings.Count(res.Content, `"name":`)
	if n != 10 {
		t.Errorf("expected 10 (default), got %d", n)
	}
}

// T18: DeferExclude → excluded server stays eager, others deferred.
func TestE2E_DeferExclude(t *testing.T) {
	fakeA := mcptest.New()
	fakeA.Tools = []mcp.ToolDefinition{{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	outA, inA := fakeA.Pipes()
	defer fakeA.Stop()

	fakeB := mcptest.New()
	fakeB.Tools = []mcp.ToolDefinition{{Name: "b", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	outB, inB := fakeB.Pipes()
	defer fakeB.Stop()

	reg := newTestRegistry(t)
	reg.SetDeferredMode(&deferConfig{enabled: true, exclude: map[string]bool{"keep": true}})

	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "keep"},
		mcp.NewStdioClientFromPipes(outA, inA)); err != nil {
		t.Fatal(err)
	}
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "defer"},
		mcp.NewStdioClientFromPipes(outB, inB)); err != nil {
		t.Fatal(err)
	}

	for _, d := range reg.toolReg.AllDefinitions() {
		switch d.Name {
		case "mcp__keep__a":
			if len(d.Parameters) == 0 {
				t.Error("excluded server should have eager schema")
			}
		case "mcp__defer__b":
			if len(d.Parameters) != 0 {
				t.Error("non-excluded server should be deferred")
			}
		}
	}
}
