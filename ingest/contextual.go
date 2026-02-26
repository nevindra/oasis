package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	oasis "github.com/nevindra/oasis"
)

const contextualEnrichmentPrompt = `<document>
%s
</document>

Here is the chunk we want to situate within the whole document:
<chunk>
%s
</chunk>

Please give a short succinct context to situate this chunk within the overall document for the purposes of improving search retrieval of the chunk. Answer only with the succinct context and nothing else.`

// enrichChunksWithContext sends each chunk to an LLM alongside the document
// text, and prepends the returned context to chunk.Content. Each chunk is
// processed independently via a bounded worker pool. Individual LLM failures
// are logged but do not block — the chunk keeps its original content.
func enrichChunksWithContext(ctx context.Context, provider oasis.Provider, chunks []oasis.Chunk, docText string, workers int, logger *slog.Logger) {
	if len(chunks) == 0 {
		return
	}
	if workers <= 0 {
		workers = 1
	}

	numWorkers := min(workers, len(chunks))
	work := make(chan int, len(chunks))
	done := make(chan struct{})

	if logger != nil {
		logger.Info("contextual enrichment: worker pool started",
			"chunk_count", len(chunks), "workers", numWorkers,
			"doc_text_bytes", len(docText))
	}

	var enriched, failed, skipped atomic.Int32

	for w := 0; w < numWorkers; w++ {
		go func() {
			for i := range work {
				if ctx.Err() != nil {
					skipped.Add(1)
					if logger != nil {
						logger.Warn("contextual enrichment: context cancelled, skipping chunk",
							"chunk_id", chunks[i].ID)
					}
					continue
				}

				prompt := fmt.Sprintf(contextualEnrichmentPrompt, docText, chunks[i].Content)
				resp, err := provider.Chat(ctx, oasis.ChatRequest{
					Messages: []oasis.ChatMessage{
						{Role: "user", Content: prompt},
					},
				})
				if err != nil {
					failed.Add(1)
					if logger != nil {
						logger.Warn("contextual enrichment: LLM call failed",
							"chunk_id", chunks[i].ID, "err", err)
					}
					continue
				}

				prefix := strings.TrimSpace(resp.Content)
				if prefix != "" {
					chunks[i].Content = prefix + "\n\n" + chunks[i].Content
					enriched.Add(1)
				} else if logger != nil {
					logger.Warn("contextual enrichment: empty response from LLM",
						"chunk_id", chunks[i].ID)
				}
			}
			done <- struct{}{}
		}()
	}

	for i := range chunks {
		work <- i
	}
	close(work)

	for w := 0; w < numWorkers; w++ {
		<-done
	}

	if logger != nil {
		e, f, s := enriched.Load(), failed.Load(), skipped.Load()
		if f > 0 || s > 0 {
			logger.Warn("contextual enrichment completed with issues",
				"enriched", e, "failed", f, "skipped", s,
				"total", len(chunks))
		} else {
			logger.Info("contextual enrichment: all chunks enriched",
				"enriched", e, "total", len(chunks))
		}
	}
}

// truncateDocText truncates text to maxBytes at the nearest preceding word
// boundary. Returns the original text if maxBytes is 0 or the text fits.
func truncateDocText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	// If char right after cut is a separator, we're at a word boundary already.
	if text[maxBytes] == ' ' || text[maxBytes] == '\n' {
		return text[:maxBytes]
	}
	// Step back to a space boundary.
	cut := maxBytes
	for cut > 0 && text[cut-1] != ' ' && text[cut-1] != '\n' {
		cut--
	}
	if cut == 0 {
		// No space found — hard cut at maxBytes.
		return text[:maxBytes]
	}
	return strings.TrimSpace(text[:cut])
}
