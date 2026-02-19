package main

import (
	"context"
	"encoding/base64"
	"testing"
)

// stubFrontend is a minimal Frontend stub for testing downloadAttachments.
type stubFrontend struct {
	// files maps fileID → (data, filename)
	files map[string]stubFile
}

type stubFile struct {
	data     []byte
	filename string
}

func (f *stubFrontend) Poll(_ context.Context) (<-chan IncomingMessage, error) { return nil, nil }
func (f *stubFrontend) Send(_ context.Context, _ string, _ string) (string, error)  { return "", nil }
func (f *stubFrontend) Edit(_ context.Context, _, _, _ string) error                { return nil }
func (f *stubFrontend) EditFormatted(_ context.Context, _, _, _ string) error       { return nil }
func (f *stubFrontend) SendTyping(_ context.Context, _ string) error                { return nil }
func (f *stubFrontend) DownloadFile(_ context.Context, fileID string) ([]byte, string, error) {
	if sf, ok := f.files[fileID]; ok {
		return sf.data, sf.filename, nil
	}
	return nil, "", nil
}

func TestDownloadAttachments_Photo(t *testing.T) {
	imgBytes := []byte{0xFF, 0xD8, 0xFF} // fake JPEG header
	fe := &stubFrontend{
		files: map[string]stubFile{
			"photo-small": {data: []byte{0x00}, filename: "small.jpg"},
			"photo-large": {data: imgBytes, filename: "large.jpg"},
		},
	}
	msg := IncomingMessage{
		Photos: []FileInfo{
			{FileID: "photo-small", MimeType: "image/jpeg"},
			{FileID: "photo-large", MimeType: "image/jpeg"},
		},
	}

	images, docs, textContent, err := downloadAttachments(context.Background(), fe, msg)
	if err != nil {
		t.Fatal(err)
	}
	if textContent != "" {
		t.Errorf("textContent = %q, want empty", textContent)
	}
	if len(docs) != 0 {
		t.Errorf("docs count = %d, want 0 for photos", len(docs))
	}
	// Only the largest photo (last in slice) should be downloaded.
	if len(images) != 1 {
		t.Fatalf("images count = %d, want 1", len(images))
	}
	wantBase64 := base64.StdEncoding.EncodeToString(imgBytes)
	if images[0].Base64 != wantBase64 {
		t.Errorf("image base64 mismatch")
	}
	if images[0].MimeType != "image/jpeg" {
		t.Errorf("image MimeType = %q, want image/jpeg", images[0].MimeType)
	}
}

func TestDownloadAttachments_DocumentPDF(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4 fake pdf content")
	fe := &stubFrontend{
		files: map[string]stubFile{
			"doc-pdf": {data: pdfBytes, filename: "report.pdf"},
		},
	}
	msg := IncomingMessage{
		Document: &FileInfo{
			FileID:   "doc-pdf",
			FileName: "report.pdf",
			MimeType: "application/pdf",
		},
	}

	images, docs, textContent, err := downloadAttachments(context.Background(), fe, msg)
	if err != nil {
		t.Fatal(err)
	}
	if textContent != "" {
		t.Errorf("textContent = %q, want empty for PDF", textContent)
	}
	// PDF goes to both images (multimodal) and docs (ingestion).
	if len(images) != 1 {
		t.Fatalf("images count = %d, want 1", len(images))
	}
	if images[0].MimeType != "application/pdf" {
		t.Errorf("MimeType = %q, want application/pdf", images[0].MimeType)
	}
	wantBase64 := base64.StdEncoding.EncodeToString(pdfBytes)
	if images[0].Base64 != wantBase64 {
		t.Errorf("image base64 mismatch")
	}
	if len(docs) != 1 {
		t.Fatalf("docs count = %d, want 1 for PDF", len(docs))
	}
	if docs[0].Filename != "report.pdf" {
		t.Errorf("docs[0].Filename = %q, want report.pdf", docs[0].Filename)
	}
	if docs[0].MimeType != "application/pdf" {
		t.Errorf("docs[0].MimeType = %q, want application/pdf", docs[0].MimeType)
	}
	if string(docs[0].Data) != string(pdfBytes) {
		t.Errorf("docs[0].Data mismatch")
	}
}

func TestDownloadAttachments_DocumentImage(t *testing.T) {
	imgBytes := []byte{0x89, 0x50, 0x4E, 0x47} // PNG header
	fe := &stubFrontend{
		files: map[string]stubFile{
			"doc-img": {data: imgBytes, filename: "photo.png"},
		},
	}
	msg := IncomingMessage{
		Document: &FileInfo{
			FileID:   "doc-img",
			FileName: "photo.png",
			MimeType: "image/png",
		},
	}

	images, docs, textContent, err := downloadAttachments(context.Background(), fe, msg)
	if err != nil {
		t.Fatal(err)
	}
	if textContent != "" {
		t.Errorf("textContent = %q, want empty for image document", textContent)
	}
	if len(docs) != 0 {
		t.Errorf("docs count = %d, want 0 for image document", len(docs))
	}
	if len(images) != 1 {
		t.Fatalf("images count = %d, want 1", len(images))
	}
	if images[0].MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", images[0].MimeType)
	}
}

func TestDownloadAttachments_DocumentTextFile(t *testing.T) {
	src := "package main\n\nfunc main() {}"
	fe := &stubFrontend{
		files: map[string]stubFile{
			"doc-go": {data: []byte(src), filename: "main.go"},
		},
	}
	msg := IncomingMessage{
		Document: &FileInfo{
			FileID:   "doc-go",
			FileName: "main.go",
			MimeType: "text/x-go",
		},
	}

	images, docs, textContent, err := downloadAttachments(context.Background(), fe, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Errorf("images count = %d, want 0 for text file", len(images))
	}
	if textContent == "" {
		t.Fatal("textContent should not be empty for text file")
	}
	if textContent != "[File: main.go]\n"+src {
		t.Errorf("textContent = %q, want file header + content", textContent)
	}
	// Text file also queued for ingestion.
	if len(docs) != 1 {
		t.Fatalf("docs count = %d, want 1 for text file", len(docs))
	}
	if docs[0].Filename != "main.go" {
		t.Errorf("docs[0].Filename = %q, want main.go", docs[0].Filename)
	}
	if string(docs[0].Data) != src {
		t.Errorf("docs[0].Data mismatch")
	}
}

func TestDownloadAttachments_NoAttachments(t *testing.T) {
	fe := &stubFrontend{files: map[string]stubFile{}}
	msg := IncomingMessage{Text: "hello"}

	images, docs, textContent, err := downloadAttachments(context.Background(), fe, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 0 {
		t.Errorf("images count = %d, want 0", len(images))
	}
	if len(docs) != 0 {
		t.Errorf("docs count = %d, want 0", len(docs))
	}
	if textContent != "" {
		t.Errorf("textContent = %q, want empty", textContent)
	}
}
