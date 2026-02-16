pub mod types;
pub mod google;
pub mod fetch;
pub mod browser;

pub use types::{SearchResult, SearchResultWithContent, PageElement, PageSnapshot};
pub use browser::parse_element_ref;

use chromiumoxide::browser::{Browser, BrowserConfig};
use futures::StreamExt;
use oasis_core::error::{OasisError, Result};
use reqwest::Client;
use std::time::Duration;
use tokio::sync::Mutex;

/// Web search client that uses a headless Chromium browser to scrape Google.
///
/// Uses stealth mode and human-like navigation to avoid bot detection.
/// The browser is also used as a fallback for fetching JS-heavy pages.
/// Also provides interactive browser session for form filling and page interaction.
pub struct WebSearch {
    browser: Browser,
    client: Client,
    active_page: Mutex<Option<chromiumoxide::Page>>,
}

pub(super) const CHROME_USER_AGENT: &str =
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
pub(super) const PAGE_FETCH_TIMEOUT: Duration = Duration::from_secs(10);
/// Maximum characters to keep from a fetched page.
/// Sized large so the caller can chunk and rank by relevance.
pub(super) const MAX_PAGE_CHARS: usize = 12_000;
/// Time to wait for Google to render search results.
pub(super) const SEARCH_RENDER_WAIT: Duration = Duration::from_secs(3);
/// Time to wait for a JS-heavy page to render in the browser.
pub(super) const BROWSER_PAGE_WAIT: Duration = Duration::from_secs(4);
/// Minimum chars for a page to be considered "has content" from reqwest.
/// Below this, we fall back to the headless browser.
pub(super) const MIN_USEFUL_CHARS: usize = 200;
/// Time to wait after a browser interaction (click/type) for the page to update.
pub(super) const BROWSE_ACTION_WAIT: Duration = Duration::from_secs(2);
/// Maximum number of interactive elements to report in a page snapshot.
pub(super) const MAX_ELEMENTS: usize = 50;
/// Maximum text content length in page snapshots.
pub(super) const MAX_SNAPSHOT_TEXT: usize = 5000;

/// Truncate a string to at most `max` bytes, ensuring the cut is on a char boundary.
pub(super) fn truncate_str(s: &str, max: usize) -> &str {
    if s.len() <= max {
        return s;
    }
    // Walk backwards from max to find a valid char boundary
    let mut end = max;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    &s[..end]
}

impl WebSearch {
    /// Launch a headless Chromium browser and create a new WebSearch instance.
    ///
    /// Requires Chromium or Google Chrome to be installed on the system.
    pub async fn new() -> Result<Self> {
        let config = BrowserConfig::builder()
            .no_sandbox()
            .arg("--disable-gpu")
            .arg("--disable-dev-shm-usage")
            .arg("--disable-blink-features=AutomationControlled")
            .arg("--lang=en-US")
            .window_size(1920, 1080)
            .build()
            .map_err(|e| OasisError::Config(format!("browser config: {e}")))?;

        let (browser, mut handler) = Browser::launch(config)
            .await
            .map_err(|e| OasisError::Config(format!("browser launch failed: {e}")))?;

        // The handler drives the browser's CDP connection — must be polled.
        tokio::spawn(async move {
            while let Some(h) = handler.next().await {
                if h.is_err() {
                    break;
                }
            }
        });

        let client = Client::builder()
            .user_agent(CHROME_USER_AGENT)
            .timeout(PAGE_FETCH_TIMEOUT)
            .redirect(reqwest::redirect::Policy::limited(5))
            .build()
            .unwrap_or_else(|_| Client::new());

        Ok(Self {
            browser,
            client,
            active_page: Mutex::new(None),
        })
    }
}
