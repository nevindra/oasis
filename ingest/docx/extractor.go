// Package docx provides a DOCX text extractor for the ingest pipeline.
//
// It parses the ZIP-based OOXML format to extract paragraphs, tables, headings,
// and embedded images. Pure Go, no CGO.
//
// Usage:
//
//	import "github.com/nevindra/oasis/ingest/docx"
//
//	ingestor := ingest.NewIngestor(store, embedding,
//	    ingest.WithExtractor(ingest.TypeDOCX, docx.NewExtractor()),
//	)
package docx

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/ingest"
)

// Compile-time interface checks.
var _ ingest.Extractor = (*Extractor)(nil)
var _ ingest.MetadataExtractor = (*Extractor)(nil)

// TypeDOCX is the content type for DOCX documents.
const TypeDOCX = ingest.TypeDOCX

// Extractor implements ingest.Extractor and ingest.MetadataExtractor for
// DOCX documents. It streams OOXML tokens to extract text, headings, tables,
// and embedded images without loading the full DOM tree into memory.
type Extractor struct{}

// NewExtractor creates a DOCX extractor.
func NewExtractor() *Extractor { return &Extractor{} }

// Extract extracts plain text from a DOCX document.
func (e *Extractor) Extract(content []byte) (string, error) {
	result, err := e.ExtractWithMeta(content)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// ExtractWithMeta extracts text and structured metadata (headings, images)
// from a DOCX document. Tables are converted to labeled "Header: Value" format.
// Headings produce PageMeta entries with byte offsets into the returned text.
func (e *Extractor) ExtractWithMeta(content []byte) (ingest.ExtractResult, error) {
	if len(content) == 0 {
		return ingest.ExtractResult{}, fmt.Errorf("empty docx content")
	}

	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return ingest.ExtractResult{}, fmt.Errorf("open zip: %w", err)
	}

	// Load images from word/media/.
	images := loadImages(zr)

	// Find and parse word/document.xml.
	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return ingest.ExtractResult{}, fmt.Errorf("missing word/document.xml")
	}

	docData, err := readZipFile(docFile)
	if err != nil {
		return ingest.ExtractResult{}, fmt.Errorf("read document.xml: %w", err)
	}

	return parseDocument(docData, images)
}

// loadImages reads all files under word/media/ and returns them keyed by
// filename (without the word/media/ prefix).
func loadImages(zr *zip.Reader) map[string]oasis.Image {
	images := make(map[string]oasis.Image)
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "word/media/") {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			continue
		}
		name := strings.TrimPrefix(f.Name, "word/media/")
		images[name] = oasis.Image{
			MimeType: http.DetectContentType(data),
			Base64:   base64.StdEncoding.EncodeToString(data),
		}
	}
	return images
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// parseState tracks the streaming XML decoder state.
type parseState struct {
	text    strings.Builder
	meta    []ingest.PageMeta
	decoder *xml.Decoder

	// heading tracking
	currentHeading   string
	headingStartByte int

	// paragraph tracking
	inParagraph    bool
	inRun          bool
	currentStyle   string
	paragraphTexts []string

	// table tracking
	inTable       bool
	inTableRow    bool
	tableHeaders  []string
	tableRowIdx   int
	cellTexts     []string
	currentCell   strings.Builder
}

// parseDocument streams through the OOXML tokens in document.xml and builds
// text output with metadata. Tables use "Header: Value" labeled format.
func parseDocument(data []byte, images map[string]oasis.Image) (ingest.ExtractResult, error) {
	s := &parseState{
		decoder: xml.NewDecoder(bytes.NewReader(data)),
	}

	for {
		tok, err := s.decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ingest.ExtractResult{}, fmt.Errorf("parse xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			s.handleStart(t)
		case xml.EndElement:
			s.handleEnd(t)
		case xml.CharData:
			s.handleCharData(t)
		}
	}

	// Close the last heading section if one is open.
	if s.currentHeading != "" {
		s.meta = append(s.meta, ingest.PageMeta{
			Heading:   s.currentHeading,
			StartByte: s.headingStartByte,
			EndByte:   s.text.Len(),
		})
	}

	// Attach images to metadata.
	if len(images) > 0 {
		var imgList []oasis.Image
		for _, img := range images {
			imgList = append(imgList, img)
		}
		if len(s.meta) > 0 {
			s.meta[0].Images = imgList
		} else {
			s.meta = append(s.meta, ingest.PageMeta{
				StartByte: 0,
				EndByte:   s.text.Len(),
				Images:    imgList,
			})
		}
	}

	return ingest.ExtractResult{
		Text: strings.TrimSpace(s.text.String()),
		Meta: s.meta,
	}, nil
}

func (s *parseState) handleStart(t xml.StartElement) {
	switch t.Name.Local {
	case "p":
		s.inParagraph = true
		s.currentStyle = ""
		s.paragraphTexts = nil
	case "pStyle":
		for _, attr := range t.Attr {
			if attr.Name.Local == "val" {
				s.currentStyle = attr.Value
			}
		}
	case "r":
		s.inRun = true
	case "tbl":
		s.inTable = true
		s.tableHeaders = nil
		s.tableRowIdx = 0
	case "tr":
		s.inTableRow = true
		s.cellTexts = nil
	case "tc":
		s.currentCell.Reset()
	}
}

func (s *parseState) handleEnd(t xml.EndElement) {
	switch t.Name.Local {
	case "r":
		s.inRun = false
	case "tc":
		s.cellTexts = append(s.cellTexts, strings.TrimSpace(s.currentCell.String()))
	case "tr":
		s.inTableRow = false
		if !s.inTable {
			return
		}
		if s.tableRowIdx == 0 {
			s.tableHeaders = make([]string, len(s.cellTexts))
			copy(s.tableHeaders, s.cellTexts)
		} else {
			s.emitTableRow()
		}
		s.tableRowIdx++
	case "tbl":
		s.inTable = false
	case "p":
		s.endParagraph()
	}
}

func (s *parseState) handleCharData(data xml.CharData) {
	content := string(data)
	if s.inTable && s.inTableRow {
		s.currentCell.WriteString(content)
		return
	}
	if s.inParagraph && s.inRun {
		s.paragraphTexts = append(s.paragraphTexts, content)
	}
}

// emitTableRow writes a data row in "Header: Value" labeled format.
func (s *parseState) emitTableRow() {
	var fields []string
	for i, val := range s.cellTexts {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		header := ""
		if i < len(s.tableHeaders) {
			header = s.tableHeaders[i]
		}
		if header != "" {
			fields = append(fields, fmt.Sprintf("%s: %s", header, val))
		} else {
			fields = append(fields, val)
		}
	}
	if len(fields) == 0 {
		return
	}
	if s.text.Len() > 0 {
		s.text.WriteString("\n\n")
	}
	s.text.WriteString(strings.Join(fields, ", "))
}

// endParagraph finalizes a paragraph, emitting its text and tracking headings.
func (s *parseState) endParagraph() {
	s.inParagraph = false

	// Table cell paragraphs are handled by the table logic.
	if s.inTable {
		return
	}
	if len(s.paragraphTexts) == 0 {
		return
	}

	paraText := strings.TrimSpace(strings.Join(s.paragraphTexts, ""))
	if paraText == "" {
		return
	}

	isHeading := strings.HasPrefix(s.currentStyle, "Heading")

	// Close previous heading section when a new heading starts.
	if isHeading && s.currentHeading != "" {
		s.meta = append(s.meta, ingest.PageMeta{
			Heading:   s.currentHeading,
			StartByte: s.headingStartByte,
			EndByte:   s.text.Len(),
		})
	}

	if s.text.Len() > 0 {
		s.text.WriteString("\n\n")
	}

	if isHeading {
		s.currentHeading = paraText
		s.headingStartByte = s.text.Len()
	}

	s.text.WriteString(paraText)
}
