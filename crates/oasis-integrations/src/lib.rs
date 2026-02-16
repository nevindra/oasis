pub mod google;
pub mod http;
pub mod linear;

use async_trait::async_trait;
use oasis_core::error::Result;

/// Abstraction for storing and retrieving OAuth tokens / API keys.
/// Brain implements this via VectorStore's config table.
#[async_trait]
pub trait TokenStore: Send + Sync {
    async fn get(&self, key: &str) -> Result<Option<String>>;
    async fn set(&self, key: &str, value: &str) -> Result<()>;
}
