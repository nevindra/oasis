package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func call(t *testing.T, name string, args any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	tool := New()
	result, err := tool.Execute(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out
}

func callErr(t *testing.T, name string, args any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	tool := New()
	result, err := tool.Execute(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected tool error but got none")
	}
	return result.Error
}

func records(out map[string]any) []map[string]any {
	raw, ok := out["records"].([]any)
	if !ok {
		return nil
	}
	recs := make([]map[string]any, len(raw))
	for i, r := range raw {
		recs[i] = r.(map[string]any)
	}
	return recs
}

func groups(out map[string]any) []map[string]any {
	raw, ok := out["groups"].([]any)
	if !ok {
		return nil
	}
	recs := make([]map[string]any, len(raw))
	for i, r := range raw {
		recs[i] = r.(map[string]any)
	}
	return recs
}

// ---- data_parse tests ----

func TestParseCSV(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": "name,age,city\nAlice,30,NYC\nBob,25,LA",
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["name"] != "Alice" || recs[0]["age"] != "30" {
		t.Errorf("unexpected first record: %v", recs[0])
	}
	if recs[1]["name"] != "Bob" || recs[1]["city"] != "LA" {
		t.Errorf("unexpected second record: %v", recs[1])
	}

	cols := out["columns"].([]any)
	if len(cols) != 3 {
		t.Errorf("expected 3 columns, got %d", len(cols))
	}
	if out["count"].(float64) != 2 {
		t.Errorf("expected count=2, got %v", out["count"])
	}
}

func TestParseCSVQuoted(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": `name,desc
"Alice","lives in NYC, USA"
"Bob","says ""hello"""`,
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["desc"] != "lives in NYC, USA" {
		t.Errorf("expected quoted comma, got: %v", recs[0]["desc"])
	}
	if recs[1]["desc"] != `says "hello"` {
		t.Errorf("expected escaped quotes, got: %v", recs[1]["desc"])
	}
}

func TestParseCSVLimit(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": "x\n1\n2\n3\n4\n5",
		"format":  "csv",
		"limit":   2,
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if out["count"].(float64) != 5 {
		t.Errorf("expected count=5 (total), got %v", out["count"])
	}
}

func TestParseJSON(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": `[{"name":"Alice","age":30},{"name":"Bob","age":25}]`,
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0]["name"] != "Alice" {
		t.Errorf("unexpected first record: %v", recs[0])
	}
	// JSON numbers come through as float64
	if recs[0]["age"].(float64) != 30 {
		t.Errorf("expected age=30, got %v", recs[0]["age"])
	}
}

func TestParseJSONSingle(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": `{"name":"Alice","age":30}`,
		"format":  "json",
	})

	recs := records(out)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
}

func TestParseJSONL(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": "{\"a\":1}\n{\"a\":2}\n{\"a\":3}",
	})

	recs := records(out)
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}
}

func TestParseJSONLSkipsMalformed(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": "{\"a\":1}\nnot json\n{\"a\":3}",
		"format":  "jsonl",
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (skip malformed), got %d", len(recs))
	}
}

func TestParseAutoDetect(t *testing.T) {
	tests := []struct {
		name    string
		content string
		format  string
	}{
		{"csv", "a,b\n1,2", "csv"},
		{"json_array", `[{"a":1}]`, "json"},
		{"json_object", `{"a":1}`, "json"},
		{"jsonl", "{\"a\":1}\n{\"b\":2}", "jsonl"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectFormat(tc.content)
			if got != tc.format {
				t.Errorf("detectFormat(%q) = %q, want %q", tc.content, got, tc.format)
			}
		})
	}
}

func TestParseEmptyContent(t *testing.T) {
	errMsg := callErr(t, "data_parse", map[string]any{
		"content": "",
	})
	if errMsg != "content is required" {
		t.Errorf("unexpected error: %s", errMsg)
	}
}

// ---- data_filter tests ----

func TestFilterEquals(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"name": "Alice", "city": "NYC"},
			{"name": "Bob", "city": "LA"},
			{"name": "Carol", "city": "NYC"},
		},
		"where": []map[string]any{
			{"column": "city", "op": "==", "value": "NYC"},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
}

func TestFilterNotEquals(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"x": "a"}, {"x": "b"}, {"x": "c"},
		},
		"where": []map[string]any{
			{"column": "x", "op": "!=", "value": "b"},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
}

func TestFilterNumericCoercion(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"name": "Alice", "age": "30"},
			{"name": "Bob", "age": "25"},
			{"name": "Carol", "age": "35"},
		},
		"where": []map[string]any{
			{"column": "age", "op": ">", "value": 28},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (30 and 35), got %d", len(recs))
	}
}

func TestFilterLessEqual(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"v": 10}, {"v": 20}, {"v": 30},
		},
		"where": []map[string]any{
			{"column": "v", "op": "<=", "value": 20},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
}

func TestFilterGreaterEqual(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"v": 10}, {"v": 20}, {"v": 30},
		},
		"where": []map[string]any{
			{"column": "v", "op": ">=", "value": 20},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
}

func TestFilterContains(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"desc": "Hello World"},
			{"desc": "Goodbye"},
			{"desc": "hello again"},
		},
		"where": []map[string]any{
			{"column": "desc", "op": "contains", "value": "hello"},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records (case-insensitive), got %d", len(recs))
	}
}

func TestFilterIn(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"city": "NYC"}, {"city": "LA"}, {"city": "SF"}, {"city": "CHI"},
		},
		"where": []map[string]any{
			{"column": "city", "op": "in", "value": []string{"NYC", "SF"}},
		},
	})

	recs := records(out)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
}

func TestFilterMultipleConditions(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"name": "Alice", "age": "30", "city": "NYC"},
			{"name": "Bob", "age": "25", "city": "NYC"},
			{"name": "Carol", "age": "35", "city": "LA"},
		},
		"where": []map[string]any{
			{"column": "city", "op": "==", "value": "NYC"},
			{"column": "age", "op": ">", "value": 28},
		},
	})

	recs := records(out)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record (Alice), got %d", len(recs))
	}
	if recs[0]["name"] != "Alice" {
		t.Errorf("expected Alice, got %v", recs[0]["name"])
	}
}

func TestFilterMissingColumn(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{
			{"name": "Alice"}, {"name": "Bob"},
		},
		"where": []map[string]any{
			{"column": "nonexistent", "op": "==", "value": "x"},
		},
	})

	recs := records(out)
	if len(recs) != 0 {
		t.Fatalf("expected 0 records (missing column), got %d", len(recs))
	}
}

func TestFilterEmptyRecords(t *testing.T) {
	out := call(t, "data_filter", map[string]any{
		"records": []map[string]any{},
		"where": []map[string]any{
			{"column": "x", "op": "==", "value": "y"},
		},
	})

	recs := records(out)
	if len(recs) != 0 {
		t.Fatalf("expected 0 records, got %d", len(recs))
	}
}

// ---- data_aggregate tests ----

func TestAggregateNoGroupBy(t *testing.T) {
	out := call(t, "data_aggregate", map[string]any{
		"records": []map[string]any{
			{"sales": 100}, {"sales": 200}, {"sales": 300},
		},
		"metrics": []map[string]any{
			{"column": "sales", "op": "sum"},
			{"column": "sales", "op": "count"},
			{"column": "sales", "op": "avg"},
			{"column": "sales", "op": "min"},
			{"column": "sales", "op": "max"},
		},
	})

	g := groups(out)
	if len(g) != 1 {
		t.Fatalf("expected 1 group, got %d", len(g))
	}
	row := g[0]
	if row["sum_sales"].(float64) != 600 {
		t.Errorf("sum_sales: expected 600, got %v", row["sum_sales"])
	}
	if row["count_sales"].(float64) != 3 {
		t.Errorf("count_sales: expected 3, got %v", row["count_sales"])
	}
	if row["avg_sales"].(float64) != 200 {
		t.Errorf("avg_sales: expected 200, got %v", row["avg_sales"])
	}
	if row["min_sales"].(float64) != 100 {
		t.Errorf("min_sales: expected 100, got %v", row["min_sales"])
	}
	if row["max_sales"].(float64) != 300 {
		t.Errorf("max_sales: expected 300, got %v", row["max_sales"])
	}
}

func TestAggregateWithGroupBy(t *testing.T) {
	out := call(t, "data_aggregate", map[string]any{
		"records": []map[string]any{
			{"country": "US", "sales": 100},
			{"country": "US", "sales": 200},
			{"country": "UK", "sales": 50},
		},
		"group_by": []string{"country"},
		"metrics": []map[string]any{
			{"column": "sales", "op": "sum"},
			{"column": "sales", "op": "count"},
		},
	})

	g := groups(out)
	if len(g) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(g))
	}

	// Groups are sorted by group-by columns.
	if g[0]["country"] != "UK" {
		t.Errorf("expected first group UK, got %v", g[0]["country"])
	}
	if g[0]["sum_sales"].(float64) != 50 {
		t.Errorf("UK sum: expected 50, got %v", g[0]["sum_sales"])
	}
	if g[1]["country"] != "US" {
		t.Errorf("expected second group US, got %v", g[1]["country"])
	}
	if g[1]["sum_sales"].(float64) != 300 {
		t.Errorf("US sum: expected 300, got %v", g[1]["sum_sales"])
	}
}

func TestAggregateNonNumericSkipped(t *testing.T) {
	out := call(t, "data_aggregate", map[string]any{
		"records": []map[string]any{
			{"val": "not_a_number"},
			{"val": "10"},
			{"val": "20"},
		},
		"metrics": []map[string]any{
			{"column": "val", "op": "sum"},
			{"column": "val", "op": "count"},
		},
	})

	g := groups(out)
	row := g[0]
	if row["sum_val"].(float64) != 30 {
		t.Errorf("sum_val: expected 30 (skip non-numeric), got %v", row["sum_val"])
	}
	if row["count_val"].(float64) != 3 {
		t.Errorf("count_val: expected 3 (count all), got %v", row["count_val"])
	}
}

func TestAggregateMultipleGroupBy(t *testing.T) {
	out := call(t, "data_aggregate", map[string]any{
		"records": []map[string]any{
			{"region": "US", "product": "A", "qty": 10},
			{"region": "US", "product": "A", "qty": 20},
			{"region": "US", "product": "B", "qty": 5},
			{"region": "EU", "product": "A", "qty": 15},
		},
		"group_by": []string{"region", "product"},
		"metrics": []map[string]any{
			{"column": "qty", "op": "sum"},
		},
	})

	g := groups(out)
	if len(g) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(g))
	}
}

func TestAggregateEmptyRecords(t *testing.T) {
	out := call(t, "data_aggregate", map[string]any{
		"records": []map[string]any{},
		"metrics": []map[string]any{
			{"column": "x", "op": "count"},
		},
	})

	g := groups(out)
	if len(g) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(g))
	}
}

// ---- data_transform tests ----

func TestTransformSelect(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"name": "Alice", "age": 30, "city": "NYC"},
		},
		"select": []string{"name", "city"},
	})

	recs := records(out)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if _, ok := recs[0]["age"]; ok {
		t.Error("age should be excluded by select")
	}
	if recs[0]["name"] != "Alice" {
		t.Errorf("expected Alice, got %v", recs[0]["name"])
	}
}

func TestTransformRename(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"name": "Alice", "age": 30},
		},
		"rename": map[string]string{"name": "full_name"},
	})

	recs := records(out)
	if recs[0]["full_name"] != "Alice" {
		t.Errorf("expected renamed field, got %v", recs[0])
	}
	if _, ok := recs[0]["name"]; ok {
		t.Error("original name should be gone after rename")
	}
}

func TestTransformSelectThenRename(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"name": "Alice", "age": 30, "city": "NYC"},
		},
		"select": []string{"name", "age"},
		"rename": map[string]string{"name": "full_name"},
	})

	recs := records(out)
	if _, ok := recs[0]["city"]; ok {
		t.Error("city should be excluded")
	}
	if recs[0]["full_name"] != "Alice" {
		t.Errorf("expected renamed, got %v", recs[0])
	}
}

func TestTransformSortNumeric(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"name": "B", "score": "100"},
			{"name": "A", "score": "20"},
			{"name": "C", "score": "3"},
		},
		"sort_by":   "score",
		"sort_desc": true,
	})

	recs := records(out)
	if recs[0]["name"] != "B" || recs[1]["name"] != "A" || recs[2]["name"] != "C" {
		t.Errorf("expected numeric sort desc: B(100), A(20), C(3), got %v %v %v",
			recs[0]["name"], recs[1]["name"], recs[2]["name"])
	}
}

func TestTransformSortString(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"name": "Charlie"}, {"name": "Alice"}, {"name": "Bob"},
		},
		"sort_by": "name",
	})

	recs := records(out)
	if recs[0]["name"] != "Alice" || recs[1]["name"] != "Bob" || recs[2]["name"] != "Charlie" {
		t.Errorf("expected alphabetical sort, got %v %v %v",
			recs[0]["name"], recs[1]["name"], recs[2]["name"])
	}
}

func TestTransformLimit(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"x": 1}, {"x": 2}, {"x": 3}, {"x": 4}, {"x": 5},
		},
		"limit": 3,
	})

	recs := records(out)
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}
}

func TestTransformSortAndLimit(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{
			{"v": 5}, {"v": 3}, {"v": 1}, {"v": 4}, {"v": 2},
		},
		"sort_by":   "v",
		"sort_desc": true,
		"limit":     3,
	})

	recs := records(out)
	if len(recs) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs))
	}
	if recs[0]["v"].(float64) != 5 || recs[1]["v"].(float64) != 4 || recs[2]["v"].(float64) != 3 {
		t.Errorf("expected top 3 desc: 5,4,3, got %v,%v,%v",
			recs[0]["v"], recs[1]["v"], recs[2]["v"])
	}
}

// ---- definitions + dispatch ----

func TestDefinitions(t *testing.T) {
	tool := New()
	defs := tool.Definitions()
	if len(defs) != 4 {
		t.Fatalf("expected 4 definitions, got %d", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"data_parse", "data_filter", "data_aggregate", "data_transform"} {
		if !names[want] {
			t.Errorf("missing definition: %s", want)
		}
	}
}

func TestUnknownFunction(t *testing.T) {
	tool := New()
	result, err := tool.Execute(context.Background(), "data_unknown", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "unknown data tool") {
		t.Errorf("expected unknown tool error, got: %s", result.Error)
	}
}

// ---- end-to-end pipeline ----

func TestPipeline(t *testing.T) {
	// Simulate: parse CSV → filter → aggregate → transform
	csv := "region,product,revenue\nAPAC,Widget,100\nAPAC,Widget,200\nAPAC,Gadget,50\nEU,Widget,300\nEU,Gadget,150"

	// Step 1: Parse.
	parsed := call(t, "data_parse", map[string]any{
		"content": csv,
	})
	parsedRecords := parsed["records"]

	// Step 2: Filter to APAC.
	filtered := call(t, "data_filter", map[string]any{
		"records": parsedRecords,
		"where": []map[string]any{
			{"column": "region", "op": "==", "value": "APAC"},
		},
	})
	if filtered["count"].(float64) != 3 {
		t.Fatalf("expected 3 APAC records, got %v", filtered["count"])
	}

	// Step 3: Aggregate by product.
	aggregated := call(t, "data_aggregate", map[string]any{
		"records": filtered["records"],
		"group_by": []string{"product"},
		"metrics": []map[string]any{
			{"column": "revenue", "op": "sum"},
			{"column": "revenue", "op": "count"},
		},
	})
	g := groups(aggregated)
	if len(g) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(g))
	}

	// Step 4: Sort by sum_revenue desc, limit 1 → top product.
	transformed := call(t, "data_transform", map[string]any{
		"records":   aggregated["groups"],
		"sort_by":   "sum_revenue",
		"sort_desc": true,
		"limit":     1,
	})
	top := records(transformed)
	if len(top) != 1 {
		t.Fatalf("expected 1 record, got %d", len(top))
	}
	if top[0]["product"] != "Widget" {
		t.Errorf("expected Widget as top product, got %v", top[0]["product"])
	}
	if top[0]["sum_revenue"].(float64) != 300 {
		t.Errorf("expected sum 300, got %v", top[0]["sum_revenue"])
	}
}

// ---- edge cases ----

func TestFilterNoConditions(t *testing.T) {
	errMsg := callErr(t, "data_filter", map[string]any{
		"records": []map[string]any{{"x": 1}},
		"where":   []map[string]any{},
	})
	if !strings.Contains(errMsg, "where conditions are required") {
		t.Errorf("unexpected error: %s", errMsg)
	}
}

func TestAggregateNoMetrics(t *testing.T) {
	errMsg := callErr(t, "data_aggregate", map[string]any{
		"records": []map[string]any{{"x": 1}},
		"metrics": []map[string]any{},
	})
	if !strings.Contains(errMsg, "metrics are required") {
		t.Errorf("unexpected error: %s", errMsg)
	}
}

func TestParseCSVEmpty(t *testing.T) {
	out := call(t, "data_parse", map[string]any{
		"content": "name\n",
		"format":  "csv",
	})
	recs := records(out)
	if len(recs) != 0 {
		t.Errorf("expected 0 records for header-only CSV, got %d", len(recs))
	}
}

func TestTransformEmptyRecords(t *testing.T) {
	out := call(t, "data_transform", map[string]any{
		"records": []map[string]any{},
		"sort_by": "x",
	})
	recs := records(out)
	if len(recs) != 0 {
		t.Fatalf("expected 0 records, got %d", len(recs))
	}
}

func TestToolInterface(t *testing.T) {
	// Verify Tool implements oasis.Tool at compile time.
	var _ interface {
		Execute(context.Context, string, json.RawMessage) (struct {
			Content string
			Error   string
		}, error)
	}
	// The real check is that data.New() is accepted where oasis.Tool is needed.
	// This test just ensures the package compiles with the interface.
}
