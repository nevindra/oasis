use oasis_core::error::{OasisError, Result};
use oasis_core::types::*;

use super::chunker::{self, ChunkerConfig};
use super::extractor::{self, ContentType};

/// The ingestion pipeline: extract text, chunk it, and produce Document + Chunk structs.
///
/// Embedding and storage are intentionally NOT handled here — the caller
/// (e.g. oasis-brain) is responsible for calling an EmbeddingProvider and
/// persisting the results.
pub struct IngestPipeline {
    chunker_config: ChunkerConfig,
}

impl IngestPipeline {
    /// Create a new pipeline with the given token-based sizing.
    ///
    /// Tokens are converted to characters by multiplying by 4
    /// (rough approximation: 1 token ~= 4 characters).
    pub fn new(max_tokens: usize, overlap_tokens: usize) -> Self {
        Self {
            chunker_config: ChunkerConfig {
                max_chars: max_tokens * 4,
                overlap_chars: overlap_tokens * 4,
            },
        }
    }

    /// Ingest plain text (or already-extracted text).
    ///
    /// Creates a `Document` and splits the content into `Chunk`s.
    /// Returns the document and a vec of `(Chunk, chunk_index)` pairs.
    ///
    /// Embeddings are NOT computed here — the caller handles that.
    pub fn ingest_text(
        &self,
        content: &str,
        source_type: &str,
        source_ref: Option<&str>,
        title: Option<&str>,
    ) -> Result<(Document, Vec<(Chunk, usize)>)> {
        let now = now_unix();
        let doc_id = new_id();

        let document = Document {
            id: doc_id.clone(),
            source_type: source_type.to_string(),
            source_ref: source_ref.map(|s| s.to_string()),
            title: title.map(|s| s.to_string()),
            raw_content: content.to_string(),
            created_at: now,
            updated_at: now,
        };

        let chunk_texts = chunker::chunk_text(content, &self.chunker_config);

        let chunks: Vec<(Chunk, usize)> = chunk_texts
            .into_iter()
            .enumerate()
            .map(|(idx, text)| {
                let chunk = Chunk {
                    id: new_id(),
                    document_id: doc_id.clone(),
                    content: text,
                    chunk_index: idx as i32,
                    created_at: now,
                };
                (chunk, idx)
            })
            .collect();

        Ok((document, chunks))
    }

    /// Ingest HTML content.
    ///
    /// Extracts plain text from HTML first, then chunks it.
    /// Uses source_type "url".
    pub fn ingest_html(
        &self,
        html: &str,
        source_ref: Option<&str>,
        title: Option<&str>,
    ) -> Result<(Document, Vec<(Chunk, usize)>)> {
        let text = extractor::extract_text(html, ContentType::Html)?;
        self.ingest_text(&text, "url", source_ref, title)
    }

    /// Ingest a file by its content and filename.
    ///
    /// Determines the content type from the file extension, extracts text,
    /// then chunks it. Uses source_type "file" and the filename as source_ref.
    pub fn ingest_file(
        &self,
        content: &str,
        filename: &str,
    ) -> Result<(Document, Vec<(Chunk, usize)>)> {
        let extension = filename
            .rsplit('.')
            .next()
            .unwrap_or("")
            .to_lowercase();

        let content_type = ContentType::from_extension(&extension);
        let text = extractor::extract_text(content, content_type)?;

        // Derive a title from the filename (without extension)
        let title = filename
            .rsplit('/')
            .next()
            .unwrap_or(filename)
            .rsplit('\\')
            .next()
            .unwrap_or(filename);

        self.ingest_text(&text, "file", Some(filename), Some(title))
    }

    /// Fetch the content of a URL as a string.
    ///
    /// Uses reqwest with rustls. Maps network and HTTP errors to `OasisError::Ingest`.
    pub async fn fetch_url(url: &str) -> Result<String> {
        let response = reqwest::get(url)
            .await
            .map_err(|e| OasisError::Ingest(format!("failed to fetch URL {url}: {e}")))?;

        let status = response.status();
        if !status.is_success() {
            return Err(OasisError::Ingest(format!(
                "HTTP {status} when fetching {url}"
            )));
        }

        response
            .text()
            .await
            .map_err(|e| OasisError::Ingest(format!("failed to read response body from {url}: {e}")))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_new_pipeline() {
        let pipeline = IngestPipeline::new(512, 50);
        assert_eq!(pipeline.chunker_config.max_chars, 2048);
        assert_eq!(pipeline.chunker_config.overlap_chars, 200);
    }

    #[test]
    fn test_ingest_text_basic() {
        let pipeline = IngestPipeline::new(512, 50);
        let (doc, chunks) = pipeline
            .ingest_text("Hello, world!", "text", None, Some("Test"))
            .unwrap();

        assert_eq!(doc.source_type, "text");
        assert_eq!(doc.title, Some("Test".to_string()));
        assert_eq!(doc.raw_content, "Hello, world!");
        assert!(!doc.id.is_empty());
        assert!(doc.created_at > 0);
        assert_eq!(doc.created_at, doc.updated_at);

        assert_eq!(chunks.len(), 1);
        assert_eq!(chunks[0].0.document_id, doc.id);
        assert_eq!(chunks[0].0.chunk_index, 0);
        assert_eq!(chunks[0].1, 0);
        assert_eq!(chunks[0].0.content, "Hello, world!");
    }

    #[test]
    fn test_ingest_text_multiple_chunks() {
        // Use small chunk size to force multiple chunks
        let pipeline = IngestPipeline {
            chunker_config: ChunkerConfig {
                max_chars: 50,
                overlap_chars: 10,
            },
        };

        let text = "First paragraph of content here.\n\nSecond paragraph of content here.\n\nThird paragraph of content here.";
        let (doc, chunks) = pipeline
            .ingest_text(text, "note", Some("test-ref"), Some("Test Doc"))
            .unwrap();

        assert!(chunks.len() > 1);
        assert_eq!(doc.source_ref, Some("test-ref".to_string()));

        // All chunks should reference the same document
        for (chunk, idx) in &chunks {
            assert_eq!(chunk.document_id, doc.id);
            assert_eq!(chunk.chunk_index, *idx as i32);
            assert!(!chunk.content.is_empty());
            assert!(!chunk.id.is_empty());
        }

        // Chunk indices should be sequential
        for (i, (_, idx)) in chunks.iter().enumerate() {
            assert_eq!(*idx, i);
        }
    }

    #[test]
    fn test_ingest_html() {
        let pipeline = IngestPipeline::new(512, 50);
        let html = "<html><body><h1>Title</h1><p>Hello <b>world</b></p></body></html>";
        let (doc, chunks) = pipeline.ingest_html(html, Some("https://example.com"), Some("Example")).unwrap();

        assert_eq!(doc.source_type, "url");
        assert_eq!(doc.source_ref, Some("https://example.com".to_string()));
        assert!(chunks[0].0.content.contains("Hello"));
        assert!(chunks[0].0.content.contains("world"));
        // HTML tags should be stripped
        assert!(!chunks[0].0.content.contains("<b>"));
    }

    #[test]
    fn test_ingest_file_markdown() {
        let pipeline = IngestPipeline::new(512, 50);
        let content = "# Hello\n\nThis is **bold** and *italic*.";
        let (doc, chunks) = pipeline.ingest_file(content, "notes/readme.md").unwrap();

        assert_eq!(doc.source_type, "file");
        assert_eq!(doc.source_ref, Some("notes/readme.md".to_string()));
        assert_eq!(doc.title, Some("readme.md".to_string()));

        // Markdown should be stripped
        let text = &chunks[0].0.content;
        assert!(text.contains("Hello"));
        assert!(text.contains("bold"));
        assert!(!text.contains("**"));
    }

    #[test]
    fn test_ingest_file_plain() {
        let pipeline = IngestPipeline::new(512, 50);
        let content = "Just plain text content.";
        let (doc, chunks) = pipeline.ingest_file(content, "data.txt").unwrap();

        assert_eq!(doc.source_type, "file");
        assert_eq!(doc.source_ref, Some("data.txt".to_string()));
        assert_eq!(doc.title, Some("data.txt".to_string()));
        assert_eq!(chunks[0].0.content, "Just plain text content.");
    }

    #[test]
    fn test_ingest_file_html_extension() {
        let pipeline = IngestPipeline::new(512, 50);
        let content = "<p>Hello from HTML file</p>";
        let (doc, _chunks) = pipeline.ingest_file(content, "page.html").unwrap();

        assert_eq!(doc.source_type, "file");
        assert_eq!(doc.source_ref, Some("page.html".to_string()));
    }

    #[test]
    fn test_ingest_text_source_ref_none() {
        let pipeline = IngestPipeline::new(512, 50);
        let (doc, _) = pipeline.ingest_text("content", "note", None, None).unwrap();
        assert_eq!(doc.source_ref, None);
        assert_eq!(doc.title, None);
    }
}
