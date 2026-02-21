// Package data provides structured data transform tools for CSV/JSON processing.
// The LLM composes four functions — parse, filter, aggregate, transform — as
// building blocks for data pipelines without needing Python subprocess overhead.
package data

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	oasis "github.com/nevindra/oasis"
)

const (
	defaultLimit  = 1000
	maxOutputSize = 32 * 1024 // 32KB
)

// Tool provides structured data transform functions.
type Tool struct{}

// New creates a data transform tool.
func New() *Tool { return &Tool{} }

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{
		{
			Name:        "data_parse",
			Description: "Parse raw CSV, JSON, or JSONL text into structured records. Returns an array of objects with column names as keys. Use this to convert raw file content into a format that data_filter, data_aggregate, and data_transform can process.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"content": {
						"type": "string",
						"description": "Raw text content to parse (CSV, JSON array, or JSONL)"
					},
					"format": {
						"type": "string",
						"enum": ["csv", "json", "jsonl"],
						"description": "Data format. Auto-detected if omitted."
					},
					"limit": {
						"type": "integer",
						"description": "Max records to return (default 1000)"
					}
				},
				"required": ["content"]
			}`),
		},
		{
			Name:        "data_filter",
			Description: "Filter records by conditions. All conditions are AND-ed. Operators: ==, !=, >, <, >=, <=, contains (case-insensitive substring), in (value in array). Numeric strings are auto-coerced for comparisons.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"records": {
						"type": "array",
						"description": "Array of record objects to filter"
					},
					"where": {
						"type": "array",
						"description": "Array of conditions: [{column, op, value}, ...]",
						"items": {
							"type": "object",
							"properties": {
								"column": {"type": "string"},
								"op": {"type": "string", "enum": ["==", "!=", ">", "<", ">=", "<=", "contains", "in"]},
								"value": {}
							},
							"required": ["column", "op", "value"]
						}
					}
				},
				"required": ["records", "where"]
			}`),
		},
		{
			Name:        "data_aggregate",
			Description: "Group records and compute aggregate metrics. Operations: sum, count, avg, min, max. Without group_by, aggregates over all records. Non-numeric values are skipped for sum/avg/min/max.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"records": {
						"type": "array",
						"description": "Array of record objects to aggregate"
					},
					"group_by": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Columns to group by (optional — omit to aggregate all records)"
					},
					"metrics": {
						"type": "array",
						"description": "Aggregation metrics: [{column, op}, ...]",
						"items": {
							"type": "object",
							"properties": {
								"column": {"type": "string"},
								"op": {"type": "string", "enum": ["sum", "count", "avg", "min", "max"]}
							},
							"required": ["column", "op"]
						}
					}
				},
				"required": ["records", "metrics"]
			}`),
		},
		{
			Name:        "data_transform",
			Description: "Select, rename, sort, and limit records. Use to pick specific columns, rename them, sort by a column (numeric-aware), and limit output size.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"records": {
						"type": "array",
						"description": "Array of record objects to transform"
					},
					"select": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Columns to keep (omit to keep all)"
					},
					"rename": {
						"type": "object",
						"description": "Column rename map: {old_name: new_name, ...}"
					},
					"sort_by": {
						"type": "string",
						"description": "Column to sort by (numeric-aware)"
					},
					"sort_desc": {
						"type": "boolean",
						"description": "Sort descending (default false)"
					},
					"limit": {
						"type": "integer",
						"description": "Max records to return"
					}
				},
				"required": ["records"]
			}`),
		},
	}
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
	switch name {
	case "data_parse":
		return dataParse(args)
	case "data_filter":
		return dataFilter(args)
	case "data_aggregate":
		return dataAggregate(args)
	case "data_transform":
		return dataTransform(args)
	default:
		return oasis.ToolResult{Error: "unknown data tool: " + name}, nil
	}
}

// --- data_parse ---

type parseArgs struct {
	Content string `json:"content"`
	Format  string `json:"format"`
	Limit   int    `json:"limit"`
}

func dataParse(args json.RawMessage) (oasis.ToolResult, error) {
	var p parseArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if p.Content == "" {
		return oasis.ToolResult{Error: "content is required"}, nil
	}
	if p.Limit <= 0 {
		p.Limit = defaultLimit
	}

	format := p.Format
	if format == "" {
		format = detectFormat(p.Content)
	}

	var records []map[string]any
	var columns []string
	var totalCount int
	var err error

	switch format {
	case "csv":
		records, columns, totalCount, err = parseCSV(p.Content, p.Limit)
	case "json":
		records, columns, totalCount, err = parseJSON(p.Content, p.Limit)
	case "jsonl":
		records, columns, totalCount, err = parseJSONL(p.Content, p.Limit)
	default:
		return oasis.ToolResult{Error: "unknown format: " + format + " (use csv, json, or jsonl)"}, nil
	}
	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}

	return marshalResult(map[string]any{
		"records": records,
		"columns": columns,
		"count":   totalCount,
	})
}

func detectFormat(content string) string {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		return "csv"
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		// Check if it's JSONL (multiple lines starting with {)
		if trimmed[0] == '{' && strings.Contains(trimmed, "\n") {
			lines := strings.Split(trimmed, "\n")
			if len(lines) > 1 {
				second := strings.TrimSpace(lines[1])
				if len(second) > 0 && second[0] == '{' {
					return "jsonl"
				}
			}
		}
		return "json"
	}
	return "csv"
}

func parseCSV(content string, limit int) ([]map[string]any, []string, int, error) {
	reader := csv.NewReader(strings.NewReader(content))
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	allRows, err := reader.ReadAll()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("csv parse error: %w", err)
	}
	if len(allRows) < 1 {
		return nil, nil, 0, nil
	}

	headers := allRows[0]
	totalCount := len(allRows) - 1
	records := make([]map[string]any, 0, min(totalCount, limit))

	for i := 1; i < len(allRows) && len(records) < limit; i++ {
		row := allRows[i]
		rec := make(map[string]any, len(headers))
		for j, h := range headers {
			if j < len(row) {
				rec[h] = row[j]
			} else {
				rec[h] = ""
			}
		}
		records = append(records, rec)
	}

	return records, headers, totalCount, nil
}

func parseJSON(content string, limit int) ([]map[string]any, []string, int, error) {
	trimmed := strings.TrimSpace(content)

	var rawRecords []map[string]any
	if trimmed[0] == '[' {
		if err := json.Unmarshal([]byte(trimmed), &rawRecords); err != nil {
			return nil, nil, 0, fmt.Errorf("json parse error: %w", err)
		}
	} else {
		var single map[string]any
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, nil, 0, fmt.Errorf("json parse error: %w", err)
		}
		rawRecords = []map[string]any{single}
	}

	totalCount := len(rawRecords)
	if len(rawRecords) > limit {
		rawRecords = rawRecords[:limit]
	}

	columns := extractColumns(rawRecords)
	return rawRecords, columns, totalCount, nil
}

func parseJSONL(content string, limit int) ([]map[string]any, []string, int, error) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	records := make([]map[string]any, 0, min(len(lines), limit))
	totalCount := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		totalCount++
		if len(records) >= limit {
			continue // keep counting total but don't parse
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip malformed lines
		}
		records = append(records, rec)
	}

	columns := extractColumns(records)
	return records, columns, totalCount, nil
}

func extractColumns(records []map[string]any) []string {
	seen := make(map[string]bool)
	var columns []string
	for _, rec := range records {
		for k := range rec {
			if !seen[k] {
				seen[k] = true
				columns = append(columns, k)
			}
		}
	}
	sort.Strings(columns)
	return columns
}

// --- data_filter ---

type filterArgs struct {
	Records []map[string]any `json:"records"`
	Where   []condition      `json:"where"`
}

type condition struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
}

func dataFilter(args json.RawMessage) (oasis.ToolResult, error) {
	var f filterArgs
	if err := json.Unmarshal(args, &f); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if len(f.Where) == 0 {
		return oasis.ToolResult{Error: "where conditions are required"}, nil
	}

	var filtered []map[string]any
	for _, rec := range f.Records {
		if matchesAll(rec, f.Where) {
			filtered = append(filtered, rec)
		}
	}
	if filtered == nil {
		filtered = []map[string]any{}
	}

	return marshalResult(map[string]any{
		"records": filtered,
		"count":   len(filtered),
	})
}

func matchesAll(rec map[string]any, conditions []condition) bool {
	for _, c := range conditions {
		if !matchCondition(rec, c) {
			return false
		}
	}
	return true
}

func matchCondition(rec map[string]any, c condition) bool {
	val, ok := rec[c.Column]
	if !ok {
		return false
	}

	switch c.Op {
	case "==":
		return compareValues(val, c.Value) == 0
	case "!=":
		return compareValues(val, c.Value) != 0
	case ">":
		return compareValues(val, c.Value) > 0
	case "<":
		return compareValues(val, c.Value) < 0
	case ">=":
		return compareValues(val, c.Value) >= 0
	case "<=":
		return compareValues(val, c.Value) <= 0
	case "contains":
		return strings.Contains(
			strings.ToLower(fmt.Sprintf("%v", val)),
			strings.ToLower(fmt.Sprintf("%v", c.Value)),
		)
	case "in":
		return valueIn(val, c.Value)
	default:
		return false
	}
}

func compareValues(a, b any) int {
	fa, aOk := toFloat(a)
	fb, bOk := toFloat(b)
	if aOk && bOk {
		if fa < fb {
			return -1
		}
		if fa > fb {
			return 1
		}
		return 0
	}
	sa := fmt.Sprintf("%v", a)
	sb := fmt.Sprintf("%v", b)
	if sa < sb {
		return -1
	}
	if sa > sb {
		return 1
	}
	return 0
}

func valueIn(val, set any) bool {
	arr, ok := set.([]any)
	if !ok {
		return false
	}
	vs := fmt.Sprintf("%v", val)
	for _, item := range arr {
		if fmt.Sprintf("%v", item) == vs {
			return true
		}
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// --- data_aggregate ---

type aggregateArgs struct {
	Records []map[string]any `json:"records"`
	GroupBy []string         `json:"group_by"`
	Metrics []metric         `json:"metrics"`
}

type metric struct {
	Column string `json:"column"`
	Op     string `json:"op"`
}

func dataAggregate(args json.RawMessage) (oasis.ToolResult, error) {
	var a aggregateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if len(a.Metrics) == 0 {
		return oasis.ToolResult{Error: "metrics are required"}, nil
	}

	// Group records.
	groups := make(map[string][]map[string]any)
	groupKeys := make(map[string]map[string]any) // group key string -> group-by values
	for _, rec := range a.Records {
		key := buildGroupKey(rec, a.GroupBy)
		groups[key] = append(groups[key], rec)
		if _, ok := groupKeys[key]; !ok {
			gk := make(map[string]any)
			for _, col := range a.GroupBy {
				gk[col] = rec[col]
			}
			groupKeys[key] = gk
		}
	}

	// Compute metrics per group.
	var result []map[string]any
	for key, recs := range groups {
		row := make(map[string]any)
		// Add group-by columns.
		for k, v := range groupKeys[key] {
			row[k] = v
		}
		// Compute each metric.
		for _, m := range a.Metrics {
			name := m.Op + "_" + m.Column
			row[name] = computeMetric(recs, m)
		}
		result = append(result, row)
	}

	// Sort groups for deterministic output.
	if len(a.GroupBy) > 0 {
		sort.Slice(result, func(i, j int) bool {
			for _, col := range a.GroupBy {
				si := fmt.Sprintf("%v", result[i][col])
				sj := fmt.Sprintf("%v", result[j][col])
				if si != sj {
					return si < sj
				}
			}
			return false
		})
	}

	return marshalResult(map[string]any{
		"groups": result,
		"count":  len(result),
	})
}

func buildGroupKey(rec map[string]any, groupBy []string) string {
	if len(groupBy) == 0 {
		return "_all"
	}
	parts := make([]string, len(groupBy))
	for i, col := range groupBy {
		parts[i] = fmt.Sprintf("%v", rec[col])
	}
	return strings.Join(parts, "\x00")
}

func computeMetric(records []map[string]any, m metric) any {
	switch m.Op {
	case "count":
		return len(records)
	case "sum":
		sum := 0.0
		for _, rec := range records {
			if f, ok := toFloat(rec[m.Column]); ok {
				sum += f
			}
		}
		return sum
	case "avg":
		sum := 0.0
		count := 0
		for _, rec := range records {
			if f, ok := toFloat(rec[m.Column]); ok {
				sum += f
				count++
			}
		}
		if count == 0 {
			return nil
		}
		return sum / float64(count)
	case "min":
		minVal := math.MaxFloat64
		found := false
		for _, rec := range records {
			if f, ok := toFloat(rec[m.Column]); ok {
				if f < minVal {
					minVal = f
				}
				found = true
			}
		}
		if !found {
			return nil
		}
		return minVal
	case "max":
		maxVal := -math.MaxFloat64
		found := false
		for _, rec := range records {
			if f, ok := toFloat(rec[m.Column]); ok {
				if f > maxVal {
					maxVal = f
				}
				found = true
			}
		}
		if !found {
			return nil
		}
		return maxVal
	default:
		return nil
	}
}

// --- data_transform ---

type transformArgs struct {
	Records  []map[string]any  `json:"records"`
	Select   []string          `json:"select"`
	Rename   map[string]string `json:"rename"`
	SortBy   string            `json:"sort_by"`
	SortDesc bool              `json:"sort_desc"`
	Limit    int               `json:"limit"`
}

func dataTransform(args json.RawMessage) (oasis.ToolResult, error) {
	var t transformArgs
	if err := json.Unmarshal(args, &t); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	result := make([]map[string]any, len(t.Records))
	for i, rec := range t.Records {
		result[i] = rec
	}

	// Select columns.
	if len(t.Select) > 0 {
		selectSet := make(map[string]bool, len(t.Select))
		for _, col := range t.Select {
			selectSet[col] = true
		}
		for i, rec := range result {
			filtered := make(map[string]any, len(t.Select))
			for k, v := range rec {
				if selectSet[k] {
					filtered[k] = v
				}
			}
			result[i] = filtered
		}
	}

	// Rename columns.
	if len(t.Rename) > 0 {
		for i, rec := range result {
			renamed := make(map[string]any, len(rec))
			for k, v := range rec {
				if newName, ok := t.Rename[k]; ok {
					renamed[newName] = v
				} else {
					renamed[k] = v
				}
			}
			result[i] = renamed
		}
	}

	// Sort.
	if t.SortBy != "" {
		sort.SliceStable(result, func(i, j int) bool {
			vi := result[i][t.SortBy]
			vj := result[j][t.SortBy]
			cmp := compareValues(vi, vj)
			if t.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}

	// Limit.
	if t.Limit > 0 && len(result) > t.Limit {
		result = result[:t.Limit]
	}

	return marshalResult(map[string]any{
		"records": result,
		"count":   len(result),
	})
}

// --- helpers ---

func marshalResult(v map[string]any) (oasis.ToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return oasis.ToolResult{Error: "marshal error: " + err.Error()}, nil
	}
	content := string(data)
	if len(content) > maxOutputSize {
		// Truncate records and re-marshal.
		if records, ok := v["records"].([]map[string]any); ok {
			for len(records) > 1 {
				records = records[:len(records)/2]
				v["records"] = records
				v["count"] = len(records)
				data, _ = json.Marshal(v)
				if len(data) <= maxOutputSize {
					break
				}
			}
			content = string(data)
		}
	}
	return oasis.ToolResult{Content: content}, nil
}
