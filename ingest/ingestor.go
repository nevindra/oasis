package ingest

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// IngestResult holds the outcome of an ingest operation.
type IngestResult struct {
	DocumentID string
	Document   oasis.Document
	ChunkCount int
}

// Ingestor provides end-to-end ingestion: extract → chunk → embed → store.
type Ingestor struct {
	store      oasis.Store
	embedding  oasis.EmbeddingProvider
	chunker    Chunker
	extractors map[ContentType]Extractor
	strategy   ChunkStrategy
	batchSize  int

	// parent-child config
	parentChunker Chunker
	childChunker  Chunker
}

// NewIngestor creates an Ingestor with sensible defaults.
func NewIngestor(store oasis.Store, emb oasis.EmbeddingProvider, opts ...Option) *Ingestor {
	ing := &Ingestor{
		store:     store,
		embedding: emb,
		chunker:   NewRecursiveChunker(),
		extractors: map[ContentType]Extractor{
			TypePlainText: PlainTextExtractor{},
			TypeHTML:      HTMLExtractor{},
			TypeMarkdown:  MarkdownExtractor{},
		},
		strategy:      StrategyFlat,
		batchSize:     64,
		parentChunker: NewRecursiveChunker(WithMaxTokens(1024)),
		childChunker:  NewRecursiveChunker(WithMaxTokens(256)),
	}
	for _, o := range opts {
		o(ing)
	}
	return ing
}

// IngestText ingests plain text content.
func (ing *Ingestor) IngestText(ctx context.Context, text, source, title string) (IngestResult, error) {
	now := oasis.NowUnix()
	docID := oasis.NewID()

	doc := oasis.Document{
		ID:        docID,
		Title:     title,
		Source:    source,
		Content:   text,
		CreatedAt: now,
	}

	chunks, err := ing.chunkAndEmbed(ctx, text, docID, TypePlainText, source, nil)
	if err != nil {
		return IngestResult{}, err
	}

	if err := ing.store.StoreDocument(ctx, doc, chunks); err != nil {
		return IngestResult{}, fmt.Errorf("store: %w", err)
	}

	return IngestResult{
		DocumentID: docID,
		Document:   doc,
		ChunkCount: len(chunks),
	}, nil
}

// IngestFile ingests file content, detecting the content type from the filename extension.
func (ing *Ingestor) IngestFile(ctx context.Context, content []byte, filename string) (IngestResult, error) {
	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	ct := ContentTypeFromExtension(ext)

	extractor, ok := ing.extractors[ct]
	if !ok {
		if isBinaryContentType(ct) {
			return IngestResult{}, fmt.Errorf("no extractor registered for %s; import the corresponding subpackage and use WithExtractor()", ct)
		}
		extractor = PlainTextExtractor{}
	}

	var text string
	var pageMeta []PageMeta

	// Use MetadataExtractor if available.
	if me, ok := extractor.(MetadataExtractor); ok {
		result, err := me.ExtractWithMeta(content)
		if err != nil {
			return IngestResult{}, fmt.Errorf("extract %s: %w", ct, err)
		}
		text = result.Text
		pageMeta = result.Meta
	} else {
		var err error
		text, err = extractor.Extract(content)
		if err != nil {
			return IngestResult{}, fmt.Errorf("extract %s: %w", ct, err)
		}
	}

	now := oasis.NowUnix()
	docID := oasis.NewID()

	doc := oasis.Document{
		ID:        docID,
		Title:     filepath.Base(filename),
		Source:    filename,
		Content:   text,
		CreatedAt: now,
	}

	chunks, err := ing.chunkAndEmbed(ctx, text, docID, ct, filename, pageMeta)
	if err != nil {
		return IngestResult{}, err
	}

	if err := ing.store.StoreDocument(ctx, doc, chunks); err != nil {
		return IngestResult{}, fmt.Errorf("store: %w", err)
	}

	return IngestResult{
		DocumentID: docID,
		Document:   doc,
		ChunkCount: len(chunks),
	}, nil
}

// IngestReader reads all content from r and ingests it, detecting content type from filename.
func (ing *Ingestor) IngestReader(ctx context.Context, r io.Reader, filename string) (IngestResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read: %w", err)
	}
	return ing.IngestFile(ctx, data, filename)
}

// chunkAndEmbed handles chunking (flat or parent-child) and batched embedding.
func (ing *Ingestor) chunkAndEmbed(ctx context.Context, text, docID string, ct ContentType, source string, pageMeta []PageMeta) ([]oasis.Chunk, error) {
	if ing.strategy == StrategyParentChild {
		return ing.chunkParentChild(ctx, text, docID, ct, source, pageMeta)
	}
	return ing.chunkFlat(ctx, text, docID, ct, source, pageMeta)
}

// chunkFlat performs single-level chunking with batched embedding.
func (ing *Ingestor) chunkFlat(ctx context.Context, text, docID string, ct ContentType, source string, pageMeta []PageMeta) ([]oasis.Chunk, error) {
	chunker := ing.selectChunker(ct)
	chunkTexts := chunker.Chunk(text)
	if len(chunkTexts) == 0 {
		return nil, nil
	}

	chunks := make([]oasis.Chunk, len(chunkTexts))
	offset := 0
	for i, t := range chunkTexts {
		// Find byte offset of this chunk in the original text.
		idx := strings.Index(text[offset:], t)
		startByte := offset
		if idx >= 0 {
			startByte = offset + idx
		}
		endByte := startByte + len(t)
		offset = endByte

		chunks[i] = oasis.Chunk{
			ID:         oasis.NewID(),
			DocumentID: docID,
			Content:    t,
			ChunkIndex: i,
			Metadata:   assignMeta(startByte, endByte, source, pageMeta),
		}
	}

	if err := ing.batchEmbed(ctx, chunks); err != nil {
		return nil, err
	}

	return chunks, nil
}

// chunkParentChild performs two-level hierarchical chunking.
// Parent chunks are stored without embeddings; child chunks get embeddings
// and link back to their parent via ParentID.
func (ing *Ingestor) chunkParentChild(ctx context.Context, text, docID string, ct ContentType, source string, pageMeta []PageMeta) ([]oasis.Chunk, error) {
	parentChunker := ing.parentChunker
	if ct == TypeMarkdown {
		// Use MarkdownChunker for parent level on markdown content.
		parentChunker = NewMarkdownChunker(WithMaxTokens(1024))
	}

	parentTexts := parentChunker.Chunk(text)
	if len(parentTexts) == 0 {
		return nil, nil
	}

	var allChunks []oasis.Chunk
	var childChunks []oasis.Chunk
	chunkIdx := 0
	offset := 0

	for _, pt := range parentTexts {
		parentID := oasis.NewID()

		// Find byte offset of parent chunk.
		idx := strings.Index(text[offset:], pt)
		parentStart := offset
		if idx >= 0 {
			parentStart = offset + idx
		}
		parentEnd := parentStart + len(pt)
		offset = parentEnd

		// Store parent chunk (no embedding).
		parent := oasis.Chunk{
			ID:         parentID,
			DocumentID: docID,
			Content:    pt,
			ChunkIndex: chunkIdx,
			Metadata:   assignMeta(parentStart, parentEnd, source, pageMeta),
		}
		allChunks = append(allChunks, parent)
		chunkIdx++

		// Split parent into children.
		childTexts := ing.childChunker.Chunk(pt)
		childOffset := 0
		for _, childText := range childTexts {
			cidx := strings.Index(pt[childOffset:], childText)
			childStart := parentStart + childOffset
			if cidx >= 0 {
				childStart = parentStart + childOffset + cidx
			}
			childEnd := childStart + len(childText)
			childOffset = childStart - parentStart + len(childText)

			child := oasis.Chunk{
				ID:         oasis.NewID(),
				DocumentID: docID,
				ParentID:   parentID,
				Content:    childText,
				ChunkIndex: chunkIdx,
				Metadata:   assignMeta(childStart, childEnd, source, pageMeta),
			}
			childChunks = append(childChunks, child)
			chunkIdx++
		}
	}

	// Batch embed only child chunks.
	if err := ing.batchEmbed(ctx, childChunks); err != nil {
		return nil, err
	}

	allChunks = append(allChunks, childChunks...)
	return allChunks, nil
}

// assignMeta finds the best-matching PageMeta for a chunk's byte range
// and builds a ChunkMeta.
func assignMeta(startByte, endByte int, source string, pageMeta []PageMeta) *oasis.ChunkMeta {
	meta := &oasis.ChunkMeta{}
	if source != "" {
		meta.SourceURL = source
	}

	if len(pageMeta) == 0 {
		if meta.SourceURL == "" {
			return nil
		}
		return meta
	}

	// Find the PageMeta with the most overlap with this chunk.
	var best *PageMeta
	bestOverlap := 0
	for i := range pageMeta {
		pm := &pageMeta[i]
		overlapStart := max(startByte, pm.StartByte)
		overlapEnd := min(endByte, pm.EndByte)
		overlap := overlapEnd - overlapStart
		if overlap > bestOverlap {
			bestOverlap = overlap
			best = pm
		}
	}

	if best != nil {
		if best.PageNumber > 0 {
			meta.PageNumber = best.PageNumber
		}
		if best.Heading != "" {
			meta.SectionHeading = best.Heading
		}
		if len(best.Images) > 0 {
			meta.Images = best.Images
		}
	}

	return meta
}

// selectChunker returns the appropriate chunker based on content type.
// If an explicit chunker was set via WithChunker, it is always used.
func (ing *Ingestor) selectChunker(ct ContentType) Chunker {
	// Explicit chunker always wins.
	if _, isDefault := ing.chunker.(*RecursiveChunker); !isDefault {
		return ing.chunker
	}
	// Auto-select based on content type.
	if ct == TypeMarkdown {
		return NewMarkdownChunker()
	}
	return ing.chunker
}

// batchEmbed embeds chunks in batches of ing.batchSize.
func (ing *Ingestor) batchEmbed(ctx context.Context, chunks []oasis.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	for i := 0; i < len(chunks); i += ing.batchSize {
		end := min(i+ing.batchSize, len(chunks))

		batch := chunks[i:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Content
		}

		embeddings, err := ing.embedding.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}

		for j := range batch {
			if j < len(embeddings) {
				chunks[i+j].Embedding = embeddings[j]
			}
		}
	}

	return nil
}
