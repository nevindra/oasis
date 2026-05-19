// Package data provides structured data transform tools for CSV/JSON processing.
// The LLM composes four atomic tools — parse, filter, aggregate, transform — as
// building blocks for data pipelines without needing Python subprocess overhead.
//
// Each function is its own oasis.Tool[In, Out] implementation; use New() to
// obtain the full set as []oasis.AnyTool ready for registration.
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

// New returns the data toolkit as a slice of atomic AnyTool implementations.
// Tools included: data_parse, data_filter, data_aggregate, data_transform.
func New() []oasis.AnyTool {
	return []oasis.AnyTool{
		oasis.Erase[ParseInput, ParseOutput](&ParseTool{}),
		oasis.Erase[FilterInput, FilterOutput](&FilterTool{}),
		oasis.Erase[AggregateInput, AggregateOutput](&AggregateTool{}),
		oasis.Erase[TransformInput, TransformOutput](&TransformTool{}),
	}
}

// --- data_parse ---

// ParseInput is the input payload for data_parse.
type ParseInput struct {
	Content string `json:"content" describe:"Raw text content to parse (CSV, JSON array, or JSONL)"`
	Format  string `json:"format,omitempty" enum:"csv,json,jsonl" describe:"Data format. Auto-detected if omitted."`
	Limit   int    `json:"limit,omitempty" describe:"Max records to return (default 1000)"`
}

// ParseOutput is the output of data_parse.
type ParseOutput struct {
	Records []map[string]any `json:"records"`
	Columns []string         `json:"columns"`
	Count   int              `json:"count"`
}

// ParseTool implements data_parse.
type ParseTool struct{}

func (t *ParseTool) Definition() oasis.ToolMeta {
	return oasis.ToolMeta{
		Name:        "data_parse",
		Description: "Parse raw CSV, JSON, or JSONL text into structured records. Returns an array of objects with column names as keys. Use this to convert raw file content into a format that data_filter, data_aggregate, and data_transform can process.",
	}
}

func (t *ParseTool) Execute(ctx context.Context, in ParseInput) (ParseOutput, error) {
	if in.Content == "" {
		return ParseOutput{}, fmt.Errorf("content is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	format := in.Format
	if format == "" {
		format = detectFormat(in.Content)
	}

	var records []map[string]any
	var columns []string
	var totalCount int
	var err error

	switch format {
	case "csv":
		records, columns, totalCount, err = parseCSV(in.Content, limit)
	case "json":
		records, columns, totalCount, err = parseJSON(in.Content, limit)
	case "jsonl":
		records, columns, totalCount, err = parseJSONL(in.Content, limit)
	default:
		return ParseOutput{}, fmt.Errorf("unknown format: %s (use csv, json, or jsonl)", format)
	}
	if err != nil {
		return ParseOutput{}, err
	}

	out := ParseOutput{Records: records, Columns: columns, Count: totalCount}
	truncateRecordsBySize(&out.Records, &out.Count, func() ([]byte, error) {
		return json.Marshal(out)
	})
	return out, nil
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

// Condition is one row of the filter where-clause.
type Condition struct {
	Column string `json:"column" describe:"column name to test"`
	Op     string `json:"op" enum:"==,!=,>,<,>=,<=,contains,in" describe:"comparison operator"`
	Value  any    `json:"value" describe:"value to compare against (any JSON-marshalable value, or array for 'in')"`
}

// FilterInput is the input payload for data_filter.
type FilterInput struct {
	Records []map[string]any `json:"records" describe:"Array of record objects to filter"`
	Where   []Condition      `json:"where" describe:"Array of AND-ed conditions"`
}

// FilterOutput is the output of data_filter.
type FilterOutput struct {
	Records []map[string]any `json:"records"`
	Count   int              `json:"count"`
}

// FilterTool implements data_filter.
type FilterTool struct{}

func (t *FilterTool) Definition() oasis.ToolMeta {
	return oasis.ToolMeta{
		Name:        "data_filter",
		Description: "Filter records by conditions. All conditions are AND-ed. Operators: ==, !=, >, <, >=, <=, contains (case-insensitive substring), in (value in array). Numeric strings are auto-coerced for comparisons.",
	}
}

func (t *FilterTool) Execute(ctx context.Context, in FilterInput) (FilterOutput, error) {
	if len(in.Where) == 0 {
		return FilterOutput{}, fmt.Errorf("where conditions are required")
	}

	var filtered []map[string]any
	for _, rec := range in.Records {
		if matchesAll(rec, in.Where) {
			filtered = append(filtered, rec)
		}
	}
	if filtered == nil {
		filtered = []map[string]any{}
	}
	out := FilterOutput{Records: filtered, Count: len(filtered)}
	truncateRecordsBySize(&out.Records, &out.Count, func() ([]byte, error) {
		return json.Marshal(out)
	})
	return out, nil
}

func matchesAll(rec map[string]any, conditions []Condition) bool {
	for _, c := range conditions {
		if !matchCondition(rec, c) {
			return false
		}
	}
	return true
}

func matchCondition(rec map[string]any, c Condition) bool {
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

// Metric describes a single aggregation operation.
type Metric struct {
	Column string `json:"column" describe:"column to aggregate"`
	Op     string `json:"op" enum:"sum,count,avg,min,max" describe:"aggregation operator"`
}

// AggregateInput is the input payload for data_aggregate.
type AggregateInput struct {
	Records []map[string]any `json:"records" describe:"Array of record objects to aggregate"`
	GroupBy []string         `json:"group_by,omitempty" describe:"Columns to group by (optional — omit to aggregate all records)"`
	Metrics []Metric         `json:"metrics" describe:"Aggregation metrics"`
}

// AggregateOutput is the output of data_aggregate.
type AggregateOutput struct {
	Groups []map[string]any `json:"groups"`
	Count  int              `json:"count"`
}

// AggregateTool implements data_aggregate.
type AggregateTool struct{}

func (t *AggregateTool) Definition() oasis.ToolMeta {
	return oasis.ToolMeta{
		Name:        "data_aggregate",
		Description: "Group records and compute aggregate metrics. Operations: sum, count, avg, min, max. Without group_by, aggregates over all records. Non-numeric values are skipped for sum/avg/min/max.",
	}
}

func (t *AggregateTool) Execute(ctx context.Context, in AggregateInput) (AggregateOutput, error) {
	if len(in.Metrics) == 0 {
		return AggregateOutput{}, fmt.Errorf("metrics are required")
	}

	// Group records.
	groups := make(map[string][]map[string]any)
	groupKeys := make(map[string]map[string]any) // group key string -> group-by values
	for _, rec := range in.Records {
		key := buildGroupKey(rec, in.GroupBy)
		groups[key] = append(groups[key], rec)
		if _, ok := groupKeys[key]; !ok {
			gk := make(map[string]any)
			for _, col := range in.GroupBy {
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
		for _, m := range in.Metrics {
			name := m.Op + "_" + m.Column
			row[name] = computeMetric(recs, m)
		}
		result = append(result, row)
	}

	// Sort groups for deterministic output.
	if len(in.GroupBy) > 0 {
		sort.Slice(result, func(i, j int) bool {
			for _, col := range in.GroupBy {
				si := fmt.Sprintf("%v", result[i][col])
				sj := fmt.Sprintf("%v", result[j][col])
				if si != sj {
					return si < sj
				}
			}
			return false
		})
	}

	out := AggregateOutput{Groups: result, Count: len(result)}
	truncateRecordsBySize(&out.Groups, &out.Count, func() ([]byte, error) {
		return json.Marshal(out)
	})
	return out, nil
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

func computeMetric(records []map[string]any, m Metric) any {
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

// TransformInput is the input payload for data_transform.
type TransformInput struct {
	Records  []map[string]any  `json:"records" describe:"Array of record objects to transform"`
	Select   []string          `json:"select,omitempty" describe:"Columns to keep (omit to keep all)"`
	Rename   map[string]string `json:"rename,omitempty" describe:"Column rename map: {old_name: new_name}"`
	SortBy   string            `json:"sort_by,omitempty" describe:"Column to sort by (numeric-aware)"`
	SortDesc bool              `json:"sort_desc,omitempty" describe:"Sort descending (default false)"`
	Limit    int               `json:"limit,omitempty" describe:"Max records to return"`
}

// TransformOutput is the output of data_transform.
type TransformOutput struct {
	Records []map[string]any `json:"records"`
	Count   int              `json:"count"`
}

// TransformTool implements data_transform.
type TransformTool struct{}

func (t *TransformTool) Definition() oasis.ToolMeta {
	return oasis.ToolMeta{
		Name:        "data_transform",
		Description: "Select, rename, sort, and limit records. Use to pick specific columns, rename them, sort by a column (numeric-aware), and limit output size.",
	}
}

func (t *TransformTool) Execute(ctx context.Context, in TransformInput) (TransformOutput, error) {
	result := make([]map[string]any, len(in.Records))
	for i, rec := range in.Records {
		result[i] = rec
	}

	// Select columns.
	if len(in.Select) > 0 {
		selectSet := make(map[string]bool, len(in.Select))
		for _, col := range in.Select {
			selectSet[col] = true
		}
		for i, rec := range result {
			filtered := make(map[string]any, len(in.Select))
			for k, v := range rec {
				if selectSet[k] {
					filtered[k] = v
				}
			}
			result[i] = filtered
		}
	}

	// Rename columns.
	if len(in.Rename) > 0 {
		for i, rec := range result {
			renamed := make(map[string]any, len(rec))
			for k, v := range rec {
				if newName, ok := in.Rename[k]; ok {
					renamed[newName] = v
				} else {
					renamed[k] = v
				}
			}
			result[i] = renamed
		}
	}

	// Sort.
	if in.SortBy != "" {
		sort.SliceStable(result, func(i, j int) bool {
			vi := result[i][in.SortBy]
			vj := result[j][in.SortBy]
			cmp := compareValues(vi, vj)
			if in.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}

	// Limit.
	if in.Limit > 0 && len(result) > in.Limit {
		result = result[:in.Limit]
	}

	out := TransformOutput{Records: result, Count: len(result)}
	truncateRecordsBySize(&out.Records, &out.Count, func() ([]byte, error) {
		return json.Marshal(out)
	})
	return out, nil
}

// --- helpers ---

// truncateRecordsBySize ensures the JSON-marshalled representation of an
// output stays within maxOutputSize. If the marshalled size exceeds the
// limit, it halves the records slice until it fits (or only 1 record remains).
// remarshal returns the current JSON encoding of the output (used to test size).
func truncateRecordsBySize(records *[]map[string]any, count *int, remarshal func() ([]byte, error)) {
	data, err := remarshal()
	if err != nil || len(data) <= maxOutputSize {
		return
	}
	recs := *records
	for len(recs) > 1 {
		recs = recs[:len(recs)/2]
		*records = recs
		*count = len(recs)
		data, err = remarshal()
		if err != nil {
			return
		}
		if len(data) <= maxOutputSize {
			return
		}
	}
}

// compile-time checks
var (
	_ oasis.Tool[ParseInput, ParseOutput]         = (*ParseTool)(nil)
	_ oasis.Tool[FilterInput, FilterOutput]       = (*FilterTool)(nil)
	_ oasis.Tool[AggregateInput, AggregateOutput] = (*AggregateTool)(nil)
	_ oasis.Tool[TransformInput, TransformOutput] = (*TransformTool)(nil)
)
