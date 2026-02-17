package ingest

import (
	"path/filepath"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// Pipeline handles text extraction, chunking, and document/chunk creation.
// Embedding and storage are NOT handled here — the caller is responsible.
type Pipeline struct {
	cfg ChunkerConfig
}

// NewPipeline creates a pipeline. maxTokens/overlapTokens are converted to chars (*4).
func NewPipeline(maxTokens, overlapTokens int) *Pipeline {
	return &Pipeline{
		cfg: ChunkerConfig{
			MaxChars:     maxTokens * 4,
			OverlapChars: overlapTokens * 4,
		},
	}
}

// IngestResult holds the document and its chunks ready for embedding + storage.
type IngestResult struct {
	Document oasis.Document
	Chunks   []oasis.Chunk
}

// IngestText creates a Document + Chunks from plain text.
func (p *Pipeline) IngestText(content, source string, title string) IngestResult {
	now := oasis.NowUnix()
	docID := oasis.NewID()

	doc := oasis.Document{
		ID:        docID,
		Title:     title,
		Source:    source,
		Content:   content,
		CreatedAt: now,
	}

	chunkTexts := ChunkText(content, p.cfg)
	chunks := make([]oasis.Chunk, len(chunkTexts))
	for i, text := range chunkTexts {
		chunks[i] = oasis.Chunk{
			ID:         oasis.NewID(),
			DocumentID: docID,
			Content:    text,
			ChunkIndex: i,
		}
	}

	return IngestResult{Document: doc, Chunks: chunks}
}

// IngestHTML extracts text from HTML, then chunks it.
func (p *Pipeline) IngestHTML(html, sourceURL string) IngestResult {
	text := StripHTML(html)
	title := sourceURL
	if title == "" {
		title = "web page"
	}
	return p.IngestText(text, sourceURL, title)
}

// IngestFile extracts text based on file extension, then chunks it.
func (p *Pipeline) IngestFile(content, filename string) IngestResult {
	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	ct := ContentTypeFromExtension(ext)
	text := ExtractText(content, ct)

	title := filepath.Base(filename)
	return p.IngestText(text, filename, title)
}
