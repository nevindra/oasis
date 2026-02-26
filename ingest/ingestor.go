package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
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

// defaultMaxContentSize is the default maximum content size for extraction (50 MB).
const defaultMaxContentSize = 50 << 20

// Ingestor provides end-to-end ingestion: extract → chunk → embed → store.
type Ingestor struct {
	store          oasis.Store
	embedding      oasis.EmbeddingProvider
	chunker        Chunker
	customChunker  bool // true when chunker was set via WithChunker
	extractors     map[ContentType]Extractor
	strategy       ChunkStrategy
	batchSize      int
	maxContentSize int

	// cached auto-select chunkers (avoid allocation per call)
	mdChunker       *MarkdownChunker
	mdParentChunker *MarkdownChunker

	// parent-child config
	parentChunker Chunker
	childChunker  Chunker

	// graph extraction config
	graphProvider    oasis.Provider
	minEdgeWeight    float32
	maxEdgesPerChunk int
	graphBatchSize    int
	graphBatchOverlap int
	graphWorkers      int
	crossDocEdges     bool
	sequenceEdges    bool

	// contextual enrichment config
	contextProvider    oasis.Provider
	contextWorkers     int
	contextMaxDocBytes int

	// observability
	tracer oasis.Tracer
	logger *slog.Logger

	// lifecycle hooks
	onSuccess func(IngestResult)
	onError   func(source string, err error)
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
			TypeCSV:       NewCSVExtractor(),
			TypeJSON:      NewJSONExtractor(),
			TypeDOCX:      NewDOCXExtractor(),
			TypePDF:       NewPDFExtractor(),
		},
		strategy:        StrategyFlat,
		batchSize:       64,
		maxContentSize:  defaultMaxContentSize,
		mdChunker:       NewMarkdownChunker(),
		mdParentChunker: NewMarkdownChunker(WithMaxTokens(1024)),
		parentChunker:   NewRecursiveChunker(WithMaxTokens(1024)),
		childChunker:    NewRecursiveChunker(WithMaxTokens(256)),
		graphBatchSize:     5,
		graphWorkers:       3,
		contextWorkers:     3,
		contextMaxDocBytes: 100_000, // 100KB ≈ ~25K tokens
	}
	for _, o := range opts {
		o(ing)
	}
	return ing
}

// IngestText ingests plain text content.
func (ing *Ingestor) IngestText(ctx context.Context, text, source, title string) (IngestResult, error) {
	if ing.tracer != nil {
		var span oasis.Span
		ctx, span = ing.tracer.Start(ctx, "ingest.document",
			oasis.StringAttr("source", source),
			oasis.StringAttr("title", title),
			oasis.StringAttr("strategy", strategyName(ing.strategy)),
			oasis.StringAttr("content_type", string(TypePlainText)))
		defer func() { span.End() }()

		result, err := ing.ingestText(ctx, text, source, title)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(
				oasis.StringAttr("doc_id", result.DocumentID),
				oasis.IntAttr("chunk_count", result.ChunkCount))
		}
		return result, err
	}
	return ing.ingestText(ctx, text, source, title)
}

func (ing *Ingestor) ingestText(ctx context.Context, text, source, title string) (IngestResult, error) {
	now := oasis.NowUnix()
	docID := oasis.NewID()

	if ing.logger != nil {
		ing.logger.Info("ingest started",
			"doc_id", docID,
			"source", source,
			"title", title,
			"content_type", string(TypePlainText),
			"strategy", strategyName(ing.strategy),
			"content_bytes", len(text))
	}

	doc := oasis.Document{
		ID:        docID,
		Title:     title,
		Source:    source,
		Content:   text,
		CreatedAt: now,
	}

	chunks, err := ing.chunkAndEmbed(ctx, text, docID, TypePlainText, source, nil)
	if err != nil {
		if ing.logger != nil {
			ing.logger.Error("chunk and embed failed",
				"doc_id", docID, "source", source, "err", err)
		}
		ing.notifyError(source, err)
		return IngestResult{}, err
	}

	if ing.logger != nil {
		ing.logger.Debug("storing document",
			"doc_id", docID, "chunk_count", len(chunks))
	}

	if err := ing.store.StoreDocument(ctx, doc, chunks); err != nil {
		err = fmt.Errorf("store: %w", err)
		if ing.logger != nil {
			ing.logger.Error("store document failed",
				"doc_id", docID, "source", source, "err", err)
		}
		ing.notifyError(source, err)
		return IngestResult{}, err
	}

	if ing.logger != nil {
		ing.logger.Debug("document stored", "doc_id", docID)
	}

	if err := ing.extractAndStoreEdges(ctx, chunks); err != nil {
		err = fmt.Errorf("graph extraction: %w", err)
		if ing.logger != nil {
			ing.logger.Error("graph extraction failed",
				"doc_id", docID, "source", source, "err", err)
		}
		ing.notifyError(source, err)
		return IngestResult{}, err
	}

	result := IngestResult{
		DocumentID: docID,
		Document:   doc,
		ChunkCount: len(chunks),
	}
	if ing.logger != nil {
		ing.logger.Info("ingest completed",
			"doc_id", docID, "source", source, "chunk_count", len(chunks))
	}
	if ing.onSuccess != nil {
		ing.onSuccess(result)
	}
	return result, nil
}

// IngestFile ingests file content, detecting the content type from the filename extension.
func (ing *Ingestor) IngestFile(ctx context.Context, content []byte, filename string) (IngestResult, error) {
	ext := strings.TrimPrefix(filepath.Ext(filename), ".")
	ct := ContentTypeFromExtension(ext)

	if ing.tracer != nil {
		var span oasis.Span
		ctx, span = ing.tracer.Start(ctx, "ingest.document",
			oasis.StringAttr("source", filename),
			oasis.StringAttr("title", filepath.Base(filename)),
			oasis.StringAttr("strategy", strategyName(ing.strategy)),
			oasis.StringAttr("content_type", string(ct)))
		defer func() { span.End() }()

		result, err := ing.ingestFile(ctx, content, filename, ct)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(
				oasis.StringAttr("doc_id", result.DocumentID),
				oasis.IntAttr("chunk_count", result.ChunkCount))
		}
		return result, err
	}
	return ing.ingestFile(ctx, content, filename, ct)
}

func (ing *Ingestor) ingestFile(ctx context.Context, content []byte, filename string, ct ContentType) (IngestResult, error) {
	if ing.maxContentSize > 0 && len(content) > ing.maxContentSize {
		err := fmt.Errorf("content size %d exceeds limit %d", len(content), ing.maxContentSize)
		if ing.logger != nil {
			ing.logger.Error("content size exceeds limit",
				"source", filename, "content_bytes", len(content),
				"max_bytes", ing.maxContentSize)
		}
		ing.notifyError(filename, err)
		return IngestResult{}, err
	}

	extractor, ok := ing.extractors[ct]
	if !ok {
		if ing.logger != nil {
			ing.logger.Warn("no extractor registered, falling back to plain text",
				"source", filename, "content_type", string(ct))
		}
		extractor = PlainTextExtractor{}
	}

	docID := oasis.NewID()
	if ing.logger != nil {
		ing.logger.Info("ingest started",
			"doc_id", docID,
			"source", filename,
			"content_type", string(ct),
			"strategy", strategyName(ing.strategy),
			"content_bytes", len(content))
	}

	var text string
	var pageMeta []PageMeta

	// Use MetadataExtractor if available.
	if me, ok := extractor.(MetadataExtractor); ok {
		if ing.logger != nil {
			ing.logger.Debug("extracting with metadata extractor",
				"doc_id", docID, "content_type", string(ct))
		}
		result, err := safeExtractWithMeta(me, content)
		if err != nil {
			err = fmt.Errorf("extract %s: %w", ct, err)
			if ing.logger != nil {
				ing.logger.Error("metadata extraction failed",
					"doc_id", docID, "source", filename, "err", err)
			}
			ing.notifyError(filename, err)
			return IngestResult{}, err
		}
		text = result.Text
		pageMeta = result.Meta
		if ing.logger != nil {
			ing.logger.Debug("extraction completed",
				"doc_id", docID, "text_bytes", len(text),
				"page_meta_count", len(pageMeta))
		}
	} else {
		if ing.logger != nil {
			ing.logger.Debug("extracting with standard extractor",
				"doc_id", docID, "content_type", string(ct))
		}
		var err error
		text, err = safeExtract(extractor, content)
		if err != nil {
			err = fmt.Errorf("extract %s: %w", ct, err)
			if ing.logger != nil {
				ing.logger.Error("extraction failed",
					"doc_id", docID, "source", filename, "err", err)
			}
			ing.notifyError(filename, err)
			return IngestResult{}, err
		}
		if ing.logger != nil {
			ing.logger.Debug("extraction completed",
				"doc_id", docID, "text_bytes", len(text))
		}
	}

	now := oasis.NowUnix()

	doc := oasis.Document{
		ID:        docID,
		Title:     filepath.Base(filename),
		Source:    filename,
		Content:   text,
		CreatedAt: now,
	}

	chunks, err := ing.chunkAndEmbed(ctx, text, docID, ct, filename, pageMeta)
	if err != nil {
		if ing.logger != nil {
			ing.logger.Error("chunk and embed failed",
				"doc_id", docID, "source", filename, "err", err)
		}
		ing.notifyError(filename, err)
		return IngestResult{}, err
	}

	if ing.logger != nil {
		ing.logger.Debug("storing document",
			"doc_id", docID, "chunk_count", len(chunks))
	}

	if err := ing.store.StoreDocument(ctx, doc, chunks); err != nil {
		err = fmt.Errorf("store: %w", err)
		if ing.logger != nil {
			ing.logger.Error("store document failed",
				"doc_id", docID, "source", filename, "err", err)
		}
		ing.notifyError(filename, err)
		return IngestResult{}, err
	}

	if ing.logger != nil {
		ing.logger.Debug("document stored", "doc_id", docID)
	}

	if err := ing.extractAndStoreEdges(ctx, chunks); err != nil {
		err = fmt.Errorf("graph extraction: %w", err)
		if ing.logger != nil {
			ing.logger.Error("graph extraction failed",
				"doc_id", docID, "source", filename, "err", err)
		}
		ing.notifyError(filename, err)
		return IngestResult{}, err
	}

	result := IngestResult{
		DocumentID: docID,
		Document:   doc,
		ChunkCount: len(chunks),
	}
	if ing.logger != nil {
		ing.logger.Info("ingest completed",
			"doc_id", docID, "source", filename, "chunk_count", len(chunks))
	}
	if ing.onSuccess != nil {
		ing.onSuccess(result)
	}
	return result, nil
}

// IngestReader reads all content from r and ingests it, detecting content type from filename.
func (ing *Ingestor) IngestReader(ctx context.Context, r io.Reader, filename string) (IngestResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read: %w", err)
	}
	return ing.IngestFile(ctx, data, filename)
}

// extractAndStoreEdges runs graph extraction if configured and stores edges.
func (ing *Ingestor) extractAndStoreEdges(ctx context.Context, chunks []oasis.Chunk) error {
	if ing.graphProvider == nil && !ing.sequenceEdges {
		return nil
	}

	gs, ok := ing.store.(oasis.GraphStore)
	if !ok {
		if ing.logger != nil {
			ing.logger.Warn("graph extraction skipped: store does not implement GraphStore")
		}
		return nil
	}

	if ing.logger != nil {
		ing.logger.Info("graph edge extraction started",
			"chunk_count", len(chunks),
			"sequence_edges", ing.sequenceEdges,
			"llm_extraction", ing.graphProvider != nil)
	}

	var edges []oasis.ChunkEdge

	// Sequence edges: deterministic, no LLM needed.
	if ing.sequenceEdges {
		seqEdges := buildSequenceEdges(chunks)
		edges = append(edges, seqEdges...)
		if ing.logger != nil {
			ing.logger.Debug("sequence edges built",
				"edge_count", len(seqEdges))
		}
	}

	// LLM-based extraction.
	if ing.graphProvider != nil {
		if ing.logger != nil {
			ing.logger.Info("LLM graph extraction started",
				"chunk_count", len(chunks),
				"batch_size", ing.graphBatchSize,
				"overlap", ing.graphBatchOverlap,
				"workers", ing.graphWorkers)
		}
		llmEdges, err := extractGraphEdges(ctx, ing.graphProvider, chunks, ing.graphBatchSize, ing.graphBatchOverlap, ing.graphWorkers, ing.logger)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Warn("LLM graph extraction failed", "err", err)
			}
		} else if ing.logger != nil {
			ing.logger.Info("LLM graph extraction completed",
				"edge_count", len(llmEdges))
		}
		edges = append(edges, llmEdges...)
	}

	beforeDedup := len(edges)
	edges = deduplicateEdges(edges)
	if ing.logger != nil && beforeDedup != len(edges) {
		ing.logger.Debug("edges deduplicated",
			"before", beforeDedup, "after", len(edges))
	}

	if ing.minEdgeWeight > 0 || ing.maxEdgesPerChunk > 0 {
		beforePrune := len(edges)
		edges = pruneEdges(edges, ing.minEdgeWeight, ing.maxEdgesPerChunk)
		if ing.logger != nil {
			ing.logger.Debug("edges pruned",
				"before", beforePrune, "after", len(edges),
				"min_weight", ing.minEdgeWeight,
				"max_per_chunk", ing.maxEdgesPerChunk)
		}
	}

	if len(edges) == 0 {
		if ing.logger != nil {
			ing.logger.Debug("no edges to store after processing")
		}
		return nil
	}

	if ing.logger != nil {
		ing.logger.Debug("storing edges", "edge_count", len(edges))
	}

	if err := gs.StoreEdges(ctx, edges); err != nil {
		if ing.logger != nil {
			ing.logger.Error("store edges failed",
				"edge_count", len(edges), "err", err)
		}
		return err
	}

	if ing.logger != nil {
		ing.logger.Info("edges stored successfully", "edge_count", len(edges))
	}

	return nil
}

// notifyError fires the onError hook if set.
func (ing *Ingestor) notifyError(source string, err error) {
	if ing.onError != nil {
		ing.onError(source, err)
	}
}

// safeExtract calls e.Extract, recovering any panic into an error.
func safeExtract(e Extractor, content []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("extractor panicked: %v", r)
		}
	}()
	return e.Extract(content)
}

// safeExtractWithMeta calls me.ExtractWithMeta, recovering any panic into an error.
func safeExtractWithMeta(me MetadataExtractor, content []byte) (result ExtractResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("extractor panicked: %v", r)
		}
	}()
	return me.ExtractWithMeta(content)
}

// strategyName returns a human-readable name for a ChunkStrategy.
func strategyName(s ChunkStrategy) string {
	switch s {
	case StrategyFlat:
		return "flat"
	case StrategyParentChild:
		return "parent_child"
	default:
		return "unknown"
	}
}

// chunkWith calls ChunkContext if the chunker implements ContextChunker,
// otherwise falls back to Chunk.
func chunkWith(ctx context.Context, chunker Chunker, text string) ([]string, error) {
	if cc, ok := chunker.(ContextChunker); ok {
		return cc.ChunkContext(ctx, text)
	}
	return chunker.Chunk(text), nil
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

	if ing.logger != nil {
		ing.logger.Info("chunking started",
			"doc_id", docID, "strategy", "flat",
			"content_type", string(ct), "text_bytes", len(text))
	}

	chunkTexts, err := chunkWith(ctx, chunker, text)
	if err != nil {
		return nil, fmt.Errorf("chunk: %w", err)
	}
	if len(chunkTexts) == 0 {
		if ing.logger != nil {
			ing.logger.Warn("chunker produced zero chunks",
				"doc_id", docID, "source", source)
		}
		return nil, nil
	}

	if ing.logger != nil {
		ing.logger.Info("chunking completed",
			"doc_id", docID, "chunk_count", len(chunkTexts))
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
		offset = min(endByte, len(text))

		chunks[i] = oasis.Chunk{
			ID:         oasis.NewID(),
			DocumentID: docID,
			Content:    t,
			ChunkIndex: i,
			Metadata:   assignMeta(startByte, endByte, source, pageMeta),
		}
	}

	if ing.contextProvider != nil {
		if ing.logger != nil {
			ing.logger.Info("contextual enrichment started",
				"doc_id", docID, "chunk_count", len(chunks),
				"workers", ing.contextWorkers)
		}
		docText := truncateDocText(text, ing.contextMaxDocBytes)
		enrichChunksWithContext(ctx, ing.contextProvider, chunks, docText, ing.contextWorkers, ing.logger)
		if ing.logger != nil {
			ing.logger.Info("contextual enrichment completed",
				"doc_id", docID, "chunk_count", len(chunks))
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
		parentChunker = ing.mdParentChunker
	}

	if ing.logger != nil {
		ing.logger.Info("chunking started",
			"doc_id", docID, "strategy", "parent_child",
			"content_type", string(ct), "text_bytes", len(text))
	}

	parentTexts, err := chunkWith(ctx, parentChunker, text)
	if err != nil {
		return nil, fmt.Errorf("chunk parent: %w", err)
	}
	if len(parentTexts) == 0 {
		if ing.logger != nil {
			ing.logger.Warn("parent chunker produced zero chunks",
				"doc_id", docID, "source", source)
		}
		return nil, nil
	}

	if ing.logger != nil {
		ing.logger.Info("parent chunking completed",
			"doc_id", docID, "parent_count", len(parentTexts))
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
		offset = min(parentEnd, len(text))

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
		childTexts, err := chunkWith(ctx, ing.childChunker, pt)
		if err != nil {
			return nil, fmt.Errorf("chunk child: %w", err)
		}
		if ing.logger != nil {
			ing.logger.Debug("parent split into children",
				"doc_id", docID, "parent_id", parentID,
				"parent_bytes", len(pt), "child_count", len(childTexts))
		}
		childOffset := 0
		for _, childText := range childTexts {
			cidx := strings.Index(pt[childOffset:], childText)
			childStart := parentStart + childOffset
			if cidx >= 0 {
				childStart = parentStart + childOffset + cidx
			}
			childEnd := childStart + len(childText)
			childOffset = min(childStart-parentStart+len(childText), len(pt))

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

	if ing.logger != nil {
		ing.logger.Info("child chunking completed",
			"doc_id", docID, "parent_count", len(parentTexts),
			"child_count", len(childChunks))
	}

	if ing.contextProvider != nil {
		if ing.logger != nil {
			ing.logger.Info("contextual enrichment started",
				"doc_id", docID, "chunk_count", len(childChunks),
				"workers", ing.contextWorkers)
		}
		docText := truncateDocText(text, ing.contextMaxDocBytes)
		enrichChunksWithContext(ctx, ing.contextProvider, childChunks, docText, ing.contextWorkers, ing.logger)
		if ing.logger != nil {
			ing.logger.Info("contextual enrichment completed",
				"doc_id", docID, "chunk_count", len(childChunks))
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
	if ing.customChunker {
		if ing.logger != nil {
			ing.logger.Debug("using custom chunker", "content_type", string(ct))
		}
		return ing.chunker
	}
	if ct == TypeMarkdown {
		if ing.logger != nil {
			ing.logger.Debug("using markdown chunker", "content_type", string(ct))
		}
		return ing.mdChunker
	}
	if ing.logger != nil {
		ing.logger.Debug("using default recursive chunker", "content_type", string(ct))
	}
	return ing.chunker
}

// batchEmbed embeds chunks in batches of ing.batchSize.
func (ing *Ingestor) batchEmbed(ctx context.Context, chunks []oasis.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	totalBatches := (len(chunks) + ing.batchSize - 1) / ing.batchSize
	if ing.logger != nil {
		ing.logger.Info("embedding started",
			"chunk_count", len(chunks),
			"batch_size", ing.batchSize,
			"total_batches", totalBatches)
	}

	for i := 0; i < len(chunks); i += ing.batchSize {
		end := min(i+ing.batchSize, len(chunks))
		batchNum := i/ing.batchSize + 1

		batch := chunks[i:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Content
		}

		if ing.logger != nil {
			ing.logger.Debug("embedding batch",
				"batch", batchNum, "total_batches", totalBatches,
				"chunks_in_batch", len(batch))
		}

		embeddings, err := ing.embedding.Embed(ctx, texts)
		if err != nil {
			if ing.logger != nil {
				ing.logger.Error("embedding batch failed",
					"batch", batchNum, "range", fmt.Sprintf("%d-%d", i, end),
					"err", err)
			}
			return fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}

		if ing.logger != nil && len(embeddings) > 0 {
			ing.logger.Debug("embedding batch completed",
				"batch", batchNum, "embeddings_returned", len(embeddings),
				"dimensions", len(embeddings[0]))
		}

		for j := range batch {
			if j < len(embeddings) {
				chunks[i+j].Embedding = embeddings[j]
			}
		}
	}

	if ing.logger != nil {
		ing.logger.Info("embedding completed",
			"chunk_count", len(chunks), "batches_processed", totalBatches)
	}

	return nil
}
