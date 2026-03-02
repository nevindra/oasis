package ingest

import (
	"context"
	"fmt"

	oasis "github.com/nevindra/oasis"
)

// embedImageChunks creates separate image chunks from extracted page metadata
// and embeds them using the multimodal embedding provider.
// Each image becomes its own chunk with ContentType="image" in metadata.
// Returns the new image chunks (with embeddings populated).
func (ing *Ingestor) embedImageChunks(ctx context.Context, docID string, pageMeta []PageMeta) ([]oasis.Chunk, error) {
	if ing.imageEmbedding == nil {
		return nil, nil
	}

	// Collect all images from page metadata.
	type imageEntry struct {
		image oasis.Image
		page  int
	}
	var images []imageEntry
	for _, pm := range pageMeta {
		for _, img := range pm.Images {
			img.Page = pm.PageNumber
			images = append(images, imageEntry{image: img, page: pm.PageNumber})
		}
	}
	if len(images) == 0 {
		return nil, nil
	}

	if ing.logger != nil {
		ing.logger.Info("image embedding started",
			"doc_id", docID,
			"image_count", len(images))
	}

	// Build multimodal inputs.
	inputs := make([]oasis.MultimodalInput, len(images))
	for i, entry := range images {
		att := oasis.Attachment{
			MimeType: entry.image.MimeType,
		}
		if entry.image.Base64 != "" {
			att.Base64 = entry.image.Base64
		}
		inputs[i] = oasis.MultimodalInput{
			Attachments: []oasis.Attachment{att},
		}
	}

	// Embed in batches.
	chunks := make([]oasis.Chunk, len(images))
	for i := 0; i < len(inputs); i += ing.batchSize {
		end := min(i+ing.batchSize, len(inputs))
		batch := inputs[i:end]

		vecs, err := ing.imageEmbedding.EmbedMultimodal(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embed images batch %d-%d: %w", i, end, err)
		}

		for j, vec := range vecs {
			idx := i + j
			entry := images[idx]

			meta := &oasis.ChunkMeta{
				ContentType: "image",
				PageNumber:  entry.page,
			}

			// Store image data: blob store or inline.
			if ing.blobStore != nil {
				chunkID := oasis.NewID()
				ref, err := ing.blobStore.StoreBlob(ctx, chunkID, []byte(entry.image.Base64), entry.image.MimeType)
				if err != nil {
					if ing.logger != nil {
						ing.logger.Warn("blob store failed, falling back to inline",
							"doc_id", docID, "err", err)
					}
					meta.Images = []oasis.Image{entry.image}
				} else {
					meta.BlobRef = ref
				}
				chunks[idx] = oasis.Chunk{
					ID:         chunkID,
					DocumentID: docID,
					Content:    entry.image.AltText,
					ChunkIndex: -(idx + 1),
					Embedding:  vec,
					Metadata:   meta,
				}
			} else {
				meta.Images = []oasis.Image{entry.image}
				chunks[idx] = oasis.Chunk{
					ID:         oasis.NewID(),
					DocumentID: docID,
					Content:    entry.image.AltText,
					ChunkIndex: -(idx + 1),
					Embedding:  vec,
					Metadata:   meta,
				}
			}
		}
	}

	if ing.logger != nil {
		ing.logger.Info("image embedding completed",
			"doc_id", docID,
			"image_chunks", len(chunks))
	}

	return chunks, nil
}
