package todo_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/tools/todo"
)

// memBackend is an in-memory Backend for testing.
type memBackend struct {
	mu   sync.Mutex
	data map[string][]todo.Item
}

func newMemBackend() *memBackend {
	return &memBackend{data: make(map[string][]todo.Item)}
}

func (m *memBackend) Get(_ context.Context, key string) ([]todo.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]todo.Item(nil), m.data[key]...), nil
}

func (m *memBackend) Set(_ context.Context, key string, items []todo.Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]todo.Item(nil), items...)
	return nil
}

func TestBackend_RoundTrip(t *testing.T) {
	b := newMemBackend()
	items := []todo.Item{
		{Content: "do thing", ActiveForm: "doing thing", Status: "pending"},
	}
	if err := b.Set(context.Background(), "k", items); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := b.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 1 || got[0].Content != "do thing" {
		t.Fatalf("round trip failed: %+v", got)
	}
}

// keyFromCtx is a simple key extractor that reads "key" from ctx.Value.
type ctxKey string

const testKey ctxKey = "key"

func keyFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(testKey).(string)
	return v
}

func ctxWithKey(k string) context.Context {
	return context.WithValue(context.Background(), testKey, k)
}

func TestTool_Execute_PersistsItems(t *testing.T) {
	b := newMemBackend()
	tool := todo.New(b, keyFromCtx)
	args := json.RawMessage(`{"todos":[
		{"content":"Build feature","activeForm":"Building feature","status":"in_progress"}
	]}`)
	res, err := tool.Execute(ctxWithKey("conv-1"), "todo_write", args)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute returned tool error: %s", res.Error)
	}
	got, _ := b.Get(context.Background(), "conv-1")
	if len(got) != 1 || got[0].Status != "in_progress" {
		t.Fatalf("did not persist: %+v", got)
	}
}

func TestTool_Execute_AllDone_ClearsList(t *testing.T) {
	b := newMemBackend()
	_ = b.Set(context.Background(), "conv-1", []todo.Item{{Content: "x", ActiveForm: "x", Status: "in_progress"}})
	tool := todo.New(b, keyFromCtx)
	args := json.RawMessage(`{"todos":[
		{"content":"Build feature","activeForm":"Building feature","status":"completed"}
	]}`)
	if _, err := tool.Execute(ctxWithKey("conv-1"), "todo_write", args); err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	got, _ := b.Get(context.Background(), "conv-1")
	if len(got) != 0 {
		t.Fatalf("expected empty list when all completed, got %+v", got)
	}
}

func TestTool_Execute_ValidationErrors(t *testing.T) {
	b := newMemBackend()
	tool := todo.New(b, keyFromCtx)
	cases := []struct {
		name string
		args string
	}{
		{"bad json", `not json`},
		{"empty content", `{"todos":[{"content":"","activeForm":"x","status":"pending"}]}`},
		{"bad status", `{"todos":[{"content":"x","activeForm":"x","status":"bogus"}]}`},
		{"empty active form", `{"todos":[{"content":"x","activeForm":"","status":"pending"}]}`},
		{"too long content", `{"todos":[{"content":"` + strings.Repeat("a", 1001) + `","activeForm":"x","status":"pending"}]}`},
		{"too many items", `{"todos":` + manyItemsJSON(51) + `}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := tool.Execute(ctxWithKey("conv-1"), "todo_write", json.RawMessage(c.args))
			if err != nil {
				t.Fatalf("Execute err: %v", err)
			}
			if res.Error == "" {
				t.Fatalf("expected validation error for %q", c.name)
			}
		})
	}
}

// manyItemsJSON returns a JSON array of n minimal valid items.
func manyItemsJSON(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"content":"x","activeForm":"x","status":"pending"}`
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestTool_Definitions(t *testing.T) {
	tool := todo.New(newMemBackend(), keyFromCtx)
	defs := tool.Definitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	if defs[0].Name != "todo_write" {
		t.Fatalf("expected name todo_write, got %s", defs[0].Name)
	}
	if len(defs[0].Parameters) == 0 {
		t.Fatalf("expected non-empty parameters schema")
	}
}

func TestTool_Description_HasGuidance(t *testing.T) {
	tool := todo.New(newMemBackend(), keyFromCtx)
	desc := tool.Definitions()[0].Description
	// The whole point of the prompt is to make the model use the tool.
	// The full ported prompt is ~180 lines / many KB. Anything materially
	// shorter means we shipped a stub by accident.
	if len(desc) < 4000 {
		t.Fatalf("description too short (%d chars) — port the full prompt", len(desc))
	}
	for _, want := range []string{"pending", "in_progress", "completed", "imperative", "When to Use", "When NOT to Use"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing keyword %q", want)
		}
	}
}
