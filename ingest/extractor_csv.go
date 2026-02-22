package ingest

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// Compile-time interface check.
var _ Extractor = (*CSVExtractor)(nil)

// CSVExtractor implements Extractor for CSV documents.
// First row is treated as headers. Each subsequent row becomes a labeled
// paragraph: "Header1: Value1, Header2: Value2".
type CSVExtractor struct{}

// NewCSVExtractor creates a CSV extractor.
func NewCSVExtractor() *CSVExtractor { return &CSVExtractor{} }

// Extract converts CSV content to labeled paragraphs.
func (e *CSVExtractor) Extract(content []byte) (string, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(content)) == 0 {
		return "", nil
	}
	r := csv.NewReader(bytes.NewReader(content))
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	headers, err := r.Read()
	if err != nil {
		if err == io.EOF {
			return "", nil
		}
		return "", fmt.Errorf("read headers: %w", err)
	}
	var paragraphs []string
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read row: %w", err)
		}
		var fields []string
		for i, val := range record {
			if i >= len(headers) {
				break
			}
			val = strings.TrimSpace(val)
			if val == "" {
				continue
			}
			fields = append(fields, fmt.Sprintf("%s: %s", headers[i], val))
		}
		if len(fields) > 0 {
			paragraphs = append(paragraphs, strings.Join(fields, ", "))
		}
	}
	return strings.Join(paragraphs, "\n\n"), nil
}
