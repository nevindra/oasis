// Package csv provides a CSV text extractor for the ingest pipeline.
// First row is treated as headers. Each subsequent row becomes a labeled
// paragraph: "Header1: Value1, Header2: Value2".
package csv

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/nevindra/oasis/ingest"
)

// TypeCSV is the content type for CSV documents.
const TypeCSV = ingest.TypeCSV

// Extractor implements ingest.Extractor for CSV documents.
type Extractor struct{}

// NewExtractor creates a CSV extractor.
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract converts CSV content to labeled paragraphs.
// First row is treated as headers. Each data row becomes:
// "Header1: Value1, Header2: Value2"
func (e *Extractor) Extract(content []byte) (string, error) {
	// Strip BOM if present.
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
