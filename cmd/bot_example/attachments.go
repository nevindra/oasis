package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// RawDoc holds raw bytes of an uploaded document for background ingestion.
type RawDoc struct {
	Data     []byte
	Filename string
	MimeType string
}

// downloadAttachments downloads files and photos from an incoming message and
// classifies them into three buckets:
//   - images: photos, image documents, and PDFs → base64 Attachment for multimodal LLMs.
//   - docs: PDFs and text documents → raw bytes for knowledge-store ingestion.
//   - textContent: other file types → decoded UTF-8 text to prepend to the user message.
//
// For photos, only the largest size (last in the slice) is downloaded.
// MIME type is taken from FileInfo when available; otherwise detected from bytes.
func downloadAttachments(ctx context.Context, fe Frontend, msg IncomingMessage) (images []oasis.Attachment, docs []RawDoc, textContent string, err error) {
	if len(msg.Photos) > 0 {
		largest := msg.Photos[len(msg.Photos)-1]
		data, _, err := fe.DownloadFile(ctx, largest.FileID)
		if err != nil {
			return nil, nil, "", fmt.Errorf("download photo: %w", err)
		}
		mime := largest.MimeType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		images = append(images, oasis.Attachment{
			MimeType: mime,
			Base64:   base64.StdEncoding.EncodeToString(data),
		})
	}

	if msg.Document != nil {
		data, _, err := fe.DownloadFile(ctx, msg.Document.FileID)
		if err != nil {
			return nil, nil, "", fmt.Errorf("download document: %w", err)
		}
		mime := msg.Document.MimeType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		name := msg.Document.FileName
		if name == "" {
			name = "file"
		}

		if strings.HasPrefix(mime, "image/") {
			// Image documents: multimodal only, no text to ingest.
			images = append(images, oasis.Attachment{
				MimeType: mime,
				Base64:   base64.StdEncoding.EncodeToString(data),
			})
		} else if mime == "application/pdf" {
			// PDFs: LLM sees it via multimodal; also queued for text ingestion.
			images = append(images, oasis.Attachment{
				MimeType: mime,
				Base64:   base64.StdEncoding.EncodeToString(data),
			})
			docs = append(docs, RawDoc{Data: data, Filename: name, MimeType: mime})
		} else {
			// Text / other files: prepend to message and queue for ingestion.
			textContent = "[File: " + name + "]\n" + string(data)
			docs = append(docs, RawDoc{Data: data, Filename: name, MimeType: mime})
		}
	}

	return images, docs, textContent, nil
}
