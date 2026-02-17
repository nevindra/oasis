use std::sync::Arc;

use oasis_core::error::Result;

use super::Brain;

impl Brain {
    /// Handle a file upload from Telegram.
    /// Returns (ingest_response, extracted_text).
    pub(crate) async fn handle_file(
        &self,
        doc: &oasis_telegram::types::TelegramDocument,
    ) -> Result<(String, String)> {
        let file_info = self.bot.get_file(&doc.file_id).await?;
        let file_path = file_info.file_path.ok_or_else(|| {
            oasis_core::error::OasisError::Telegram(
                "file_path not returned by Telegram API".to_string(),
            )
        })?;
        let file_bytes = self.bot.download_file(&file_path).await?;
        let filename = doc.file_name.as_deref().unwrap_or("unknown_file");

        let extension = filename.rsplit('.').next().unwrap_or("").to_lowercase();
        let is_pdf =
            extension == "pdf" || doc.mime_type.as_deref() == Some("application/pdf");

        let content = if is_pdf {
            pdf_extract::extract_text_from_mem(&file_bytes).map_err(|e| {
                oasis_core::error::OasisError::Ingest(format!(
                    "failed to extract text from PDF: {e}"
                ))
            })?
        } else {
            String::from_utf8(file_bytes).map_err(|e| {
                oasis_core::error::OasisError::Ingest(format!(
                    "file is not valid UTF-8 text: {e}"
                ))
            })?
        };

        let response = self.memory_tool.ingest_file(&content, filename).await?;
        Ok((response, content))
    }

    /// Handle a photo upload: download, base64-encode, send to vision LLM.
    pub(crate) async fn handle_photo(
        self: &Arc<Self>,
        chat_id: i64,
        photos: &[oasis_telegram::types::PhotoSize],
        caption: Option<&str>,
        conversation_id: &str,
    ) -> Result<String> {
        use base64::Engine;

        // Pick the largest photo (last in array — Telegram sorts ascending by size)
        let photo = photos.last().unwrap();
        log!(
            " [photo] using {}x{} (file_id={})",
            photo.width,
            photo.height,
            photo.file_id
        );

        let file_info = self.bot.get_file(&photo.file_id).await?;
        let file_path = file_info.file_path.ok_or_else(|| {
            oasis_core::error::OasisError::Telegram(
                "file_path not returned for photo".to_string(),
            )
        })?;
        let photo_bytes = self.bot.download_file(&file_path).await?;

        let mime_type = if file_path.ends_with(".png") {
            "image/png"
        } else {
            "image/jpeg"
        };

        let b64 = base64::engine::general_purpose::STANDARD.encode(&photo_bytes);
        let image_data = oasis_core::types::ImageData {
            mime_type: mime_type.to_string(),
            base64: b64,
        };

        let text = caption.unwrap_or("What's in this image?");

        let recent_messages = self
            .store
            .get_recent_messages(conversation_id, self.config.brain.context_window)
            .await?;

        self.handle_chat_stream(chat_id, text, &recent_messages, "", vec![image_data])
            .await
    }

    /// Handle a URL message.
    pub(crate) async fn handle_url(&self, url: &str, _conversation_id: &str) -> Result<String> {
        let html = crate::service::ingest::pipeline::IngestPipeline::fetch_url(url).await?;
        self.memory_tool.ingest_url(&html, url).await
    }
}
