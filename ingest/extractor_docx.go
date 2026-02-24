package ingest

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
)

// Compile-time interface checks.
var _ Extractor = (*DOCXExtractor)(nil)
var _ MetadataExtractor = (*DOCXExtractor)(nil)

// maxZipEntrySize limits decompressed size of individual zip entries
// to prevent zip bomb attacks (100 MB).
const maxZipEntrySize = 100 << 20

// DOCXExtractor implements Extractor and MetadataExtractor for DOCX documents.
// It streams OOXML tokens to extract text, headings, tables, and embedded images
// without loading the full DOM tree into memory.
type DOCXExtractor struct{}

// NewDOCXExtractor creates a DOCX extractor.
func NewDOCXExtractor() *DOCXExtractor { return &DOCXExtractor{} }

// Extract extracts plain text from a DOCX document.
// Unlike ExtractWithMeta, this skips image loading for efficiency.
func (e *DOCXExtractor) Extract(content []byte) (string, error) {
	docData, err := docxReadDocumentXML(content)
	if err != nil {
		return "", err
	}
	result, err := docxParseDocument(docData, nil)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// ExtractWithMeta extracts text and structured metadata (headings, images)
// from a DOCX document. Tables are converted to labeled "Header: Value" format.
// Headings produce PageMeta entries with byte offsets into the returned text.
func (e *DOCXExtractor) ExtractWithMeta(content []byte) (ExtractResult, error) {
	if len(content) == 0 {
		return ExtractResult{}, fmt.Errorf("empty docx content")
	}

	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return ExtractResult{}, fmt.Errorf("open zip: %w", err)
	}

	images := docxLoadImages(zr)

	docData, err := docxFindAndRead(zr)
	if err != nil {
		return ExtractResult{}, err
	}

	return docxParseDocument(docData, images)
}

// docxReadDocumentXML opens a DOCX zip and reads word/document.xml (text-only path).
func docxReadDocumentXML(content []byte) ([]byte, error) {
	if len(content) == 0 {
		return nil, fmt.Errorf("empty docx content")
	}
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	return docxFindAndRead(zr)
}

// docxFindAndRead locates and reads word/document.xml from a zip reader.
func docxFindAndRead(zr *zip.Reader) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			data, err := docxReadZipFile(f)
			if err != nil {
				return nil, fmt.Errorf("read document.xml: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("missing word/document.xml")
}

func docxLoadImages(zr *zip.Reader) map[string]oasis.Image {
	images := make(map[string]oasis.Image)
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "word/media/") {
			continue
		}
		data, err := docxReadZipFile(f)
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

func docxReadZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	lr := io.LimitReader(rc, maxZipEntrySize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(data) > maxZipEntrySize {
		return nil, fmt.Errorf("zip entry %s exceeds %d byte limit", f.Name, maxZipEntrySize)
	}
	return data, nil
}

// docxParseState tracks the streaming XML decoder state.
type docxParseState struct {
	text    strings.Builder
	meta    []PageMeta
	decoder *xml.Decoder

	currentHeading   string
	headingStartByte int

	inParagraph    bool
	inRun          bool
	currentStyle   string
	paragraphTexts []string

	inTable      bool
	inTableRow   bool
	tableHeaders []string
	tableRowIdx  int
	cellTexts    []string
	currentCell  strings.Builder
}

func docxParseDocument(data []byte, images map[string]oasis.Image) (ExtractResult, error) {
	s := &docxParseState{
		decoder: xml.NewDecoder(bytes.NewReader(data)),
	}

	for {
		tok, err := s.decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ExtractResult{}, fmt.Errorf("parse xml: %w", err)
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

	if s.currentHeading != "" {
		s.meta = append(s.meta, PageMeta{
			Heading:   s.currentHeading,
			StartByte: s.headingStartByte,
			EndByte:   s.text.Len(),
		})
	}

	if len(images) > 0 {
		var imgList []oasis.Image
		for _, img := range images {
			imgList = append(imgList, img)
		}
		if len(s.meta) > 0 {
			s.meta[0].Images = imgList
		} else {
			s.meta = append(s.meta, PageMeta{
				StartByte: 0,
				EndByte:   s.text.Len(),
				Images:    imgList,
			})
		}
	}

	return ExtractResult{
		Text: strings.TrimSpace(s.text.String()),
		Meta: s.meta,
	}, nil
}

func (s *docxParseState) handleStart(t xml.StartElement) {
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

func (s *docxParseState) handleEnd(t xml.EndElement) {
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

func (s *docxParseState) handleCharData(data xml.CharData) {
	content := string(data)
	if s.inTable && s.inTableRow {
		s.currentCell.WriteString(content)
		return
	}
	if s.inParagraph && s.inRun {
		s.paragraphTexts = append(s.paragraphTexts, content)
	}
}

func (s *docxParseState) emitTableRow() {
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

func (s *docxParseState) endParagraph() {
	s.inParagraph = false
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

	if isHeading && s.currentHeading != "" {
		s.meta = append(s.meta, PageMeta{
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
