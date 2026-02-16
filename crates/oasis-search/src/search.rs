use chromiumoxide::browser::{Browser, BrowserConfig};
use futures::StreamExt;
use oasis_core::error::{OasisError, Result};
use readability_rust::Readability;
use reqwest::Client;
use serde::Deserialize;
use std::time::Duration;

/// A single search result from Google.
#[derive(Debug, Clone, Deserialize, Default)]
pub struct SearchResult {
    pub title: String,
    pub url: String,
    pub snippet: String,
}

/// A search result with optional fetched page content.
#[derive(Debug, Clone)]
pub struct SearchResultWithContent {
    pub result: SearchResult,
    /// Extracted text from the page, or None if fetch/extraction failed.
    pub content: Option<String>,
}

/// Web search client that uses a headless Chromium browser to scrape Google.
///
/// Uses stealth mode and human-like navigation to avoid bot detection.
/// The browser is also used as a fallback for fetching JS-heavy pages.
pub struct WebSearch {
    browser: Browser,
    client: Client,
}

const CHROME_USER_AGENT: &str =
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
const PAGE_FETCH_TIMEOUT: Duration = Duration::from_secs(10);
/// Maximum characters to keep from a fetched page.
/// Sized large so the caller can chunk and rank by relevance.
const MAX_PAGE_CHARS: usize = 12_000;
/// Time to wait for Google to render search results.
const SEARCH_RENDER_WAIT: Duration = Duration::from_secs(3);
/// Time to wait for a JS-heavy page to render in the browser.
const BROWSER_PAGE_WAIT: Duration = Duration::from_secs(4);
/// Minimum chars for a page to be considered "has content" from reqwest.
/// Below this, we fall back to the headless browser.
const MIN_USEFUL_CHARS: usize = 200;

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

        Ok(Self { browser, client })
    }

    /// Search Google using the headless browser and return parsed results.
    ///
    /// Opens a new tab with stealth mode enabled, navigates to Google homepage
    /// first, then types the query and submits — simulating human behavior to
    /// avoid CAPTCHA detection. Results are deduplicated and cleaned.
    pub async fn search(&self, query: &str, max_results: usize) -> Result<Vec<SearchResult>> {
        let page = self
            .browser
            .new_page("about:blank")
            .await
            .map_err(|e| OasisError::Http {
                status: 0,
                body: format!("new page failed: {e}"),
            })?;

        // Enable stealth mode before any navigation.
        let _ = page
            .enable_stealth_mode_with_agent(CHROME_USER_AGENT)
            .await;

        // Navigate to Google homepage first (like a real user).
        page.goto("https://www.google.com")
            .await
            .map_err(|e| OasisError::Http {
                status: 0,
                body: format!("google navigation failed: {e}"),
            })?;
        tokio::time::sleep(Duration::from_millis(1000)).await;

        // Find the search box and type the query.
        let search_box = page
            .find_element("textarea[name='q'], input[name='q']")
            .await
            .map_err(|e| OasisError::Http {
                status: 0,
                body: format!("search box not found: {e}"),
            })?;

        search_box.click().await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("click failed: {e}"),
        })?;
        tokio::time::sleep(Duration::from_millis(200)).await;

        search_box
            .type_str(query)
            .await
            .map_err(|e| OasisError::Http {
                status: 0,
                body: format!("type failed: {e}"),
            })?;
        tokio::time::sleep(Duration::from_millis(300)).await;

        search_box
            .press_key("Enter")
            .await
            .map_err(|e| OasisError::Http {
                status: 0,
                body: format!("enter failed: {e}"),
            })?;

        // Wait for results to render.
        tokio::time::sleep(SEARCH_RENDER_WAIT).await;

        // Extract results from the rendered DOM.
        // Google's DOM structure changes frequently, so we use a resilient
        // approach: find all h3 elements and walk up to their parent <a> link.
        let js = format!(
            r#"
            (() => {{
                const results = [];
                for (const h3 of document.querySelectorAll('h3')) {{
                    const a = h3.closest('a');
                    if (!a || !a.href) continue;
                    let url = a.href;
                    if (!url.startsWith('http') || url.includes('google.com/')) continue;

                    // Strip URL fragments (Google's #:~:text= links)
                    const hashIdx = url.indexOf('#');
                    if (hashIdx > 0) url = url.substring(0, hashIdx);

                    if (results.some(r => r.url === url)) continue;

                    let snippet = '';
                    const container = h3.closest('[data-sokoban-container]')
                        || h3.closest('[data-hveid]')
                        || a.parentElement?.parentElement;
                    if (container) {{
                        const snipEl = container.querySelector(
                            '[data-sncf], .VwiC3b, [style*="-webkit-line-clamp"], .IsZvec, .lEBKkf, .LEwnzc'
                        );
                        if (snipEl) snippet = snipEl.innerText.trim();
                    }}

                    results.push({{
                        title: h3.innerText.trim(),
                        url: url,
                        snippet: snippet,
                    }});
                    if (results.length >= {max_results}) break;
                }}
                return results;
            }})()
        "#
        );

        let value = page.evaluate(js).await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("JS evaluation failed: {e}"),
        })?;

        let results: Vec<SearchResult> = value.into_value().unwrap_or_default();

        let _ = page.close().await;

        Ok(results)
    }

    /// Fetch pages for search results and extract text content.
    ///
    /// For each result, tries fast reqwest + Readability first. If that yields
    /// too little content (< 200 chars), falls back to the headless browser
    /// to render JS-heavy pages. Failed fetches are returned with
    /// `content: None` rather than causing an error.
    pub async fn fetch_and_extract(
        &self,
        results: Vec<SearchResult>,
    ) -> Vec<SearchResultWithContent> {
        let fetch_client = self.client.clone();

        // Phase 1: try all pages with reqwest + Readability (fast, parallel)
        let mut handles = Vec::new();
        for result in &results {
            let client = fetch_client.clone();
            let url = result.url.clone();
            handles.push(tokio::spawn(async move {
                fetch_page_text(&client, &url).await
            }));
        }

        let mut out: Vec<SearchResultWithContent> = Vec::new();
        let mut browser_retry_indices = Vec::new();

        for (i, handle) in handles.into_iter().enumerate() {
            let content = match handle.await {
                Ok(c) => c,
                Err(_) => None,
            };
            let needs_browser = content
                .as_ref()
                .map(|c| c.len() < MIN_USEFUL_CHARS)
                .unwrap_or(true);

            if needs_browser {
                browser_retry_indices.push(i);
            }
            out.push(SearchResultWithContent {
                result: results[i].clone(),
                content,
            });
        }

        // Phase 2: retry failed/thin pages with headless browser (sequential)
        for i in browser_retry_indices {
            if let Some(content) = self.fetch_page_with_browser(&out[i].result.url).await {
                out[i].content = Some(content);
            }
        }

        out
    }

    /// Fetch a page using the headless browser, wait for JS to render,
    /// then extract the page's text content.
    async fn fetch_page_with_browser(&self, url: &str) -> Option<String> {
        let page = self.browser.new_page("about:blank").await.ok()?;
        let _ = page
            .enable_stealth_mode_with_agent(CHROME_USER_AGENT)
            .await;

        page.goto(url).await.ok()?;
        tokio::time::sleep(BROWSER_PAGE_WAIT).await;

        let js = "document.body?.innerText || ''";
        let value = page.evaluate(js).await.ok()?;
        let text: String = value.into_value().unwrap_or_default();
        let _ = page.close().await;

        let text = text.trim().to_string();
        if text.len() < MIN_USEFUL_CHARS {
            return None;
        }
        if text.len() > MAX_PAGE_CHARS {
            Some(text[..MAX_PAGE_CHARS].to_string())
        } else {
            Some(text)
        }
    }
}

/// Fetch a page and extract its main content using Readability.
///
/// Uses Mozilla's Readability algorithm to extract article content (title +
/// body text), stripping navigation, ads, footers, etc. Falls back to simple
/// HTML stripping if Readability can't identify an article.
async fn fetch_page_text(client: &Client, url: &str) -> Option<String> {
    let resp = client.get(url).send().await.ok()?;
    let html = resp.text().await.ok()?;

    // Try Readability first — extracts main article content
    let text = extract_with_readability(&html, url)
        .unwrap_or_else(|| strip_html_simple(&html));

    let text = text.trim().to_string();
    if text.is_empty() {
        return None;
    }
    if text.len() > MAX_PAGE_CHARS {
        Some(text[..MAX_PAGE_CHARS].to_string())
    } else {
        Some(text)
    }
}

/// Extract main content using Mozilla's Readability algorithm.
/// Returns None if Readability can't identify an article.
fn extract_with_readability(html: &str, _url: &str) -> Option<String> {
    let mut parser = Readability::new(html, None).ok()?;
    let article = parser.parse()?;
    let raw_html = article.content.as_deref()?;
    let content = strip_html_simple(raw_html);
    let content = content.trim().to_string();
    if content.is_empty() {
        None
    } else {
        Some(content)
    }
}

/// Minimal HTML tag stripping for search result text.
/// Removes tags, decodes basic entities, collapses whitespace.
fn strip_html_simple(html: &str) -> String {
    let mut result = String::with_capacity(html.len());
    let mut in_tag = false;
    let mut in_script = false;
    let mut in_style = false;
    let mut tag_buf = String::new();
    let mut collecting_tag = false;

    for ch in html.chars() {
        if ch == '<' {
            in_tag = true;
            tag_buf.clear();
            collecting_tag = true;
            continue;
        }
        if in_tag {
            if collecting_tag && (ch.is_whitespace() || ch == '>') {
                collecting_tag = false;
                let lower = tag_buf.to_lowercase();
                match lower.as_str() {
                    "script" => in_script = true,
                    "/script" => in_script = false,
                    "style" => in_style = true,
                    "/style" => in_style = false,
                    _ => {}
                }
                if is_block_tag(&lower) {
                    result.push(' ');
                }
            } else if collecting_tag {
                tag_buf.push(ch);
            }
            if ch == '>' {
                in_tag = false;
                if collecting_tag {
                    let lower = tag_buf.to_lowercase();
                    match lower.as_str() {
                        "script" => in_script = true,
                        "/script" => in_script = false,
                        "style" => in_style = true,
                        "/style" => in_style = false,
                        _ => {}
                    }
                    if is_block_tag(&lower) {
                        result.push(' ');
                    }
                    collecting_tag = false;
                }
            }
            continue;
        }
        if in_script || in_style {
            continue;
        }
        result.push(ch);
    }

    let result = result
        .replace("&amp;", "&")
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&#39;", "'")
        .replace("&apos;", "'")
        .replace("&nbsp;", " ");

    let mut collapsed = String::with_capacity(result.len());
    let mut last_was_space = false;
    for ch in result.chars() {
        if ch.is_whitespace() {
            if !last_was_space {
                collapsed.push(' ');
                last_was_space = true;
            }
        } else {
            collapsed.push(ch);
            last_was_space = false;
        }
    }

    collapsed.trim().to_string()
}

fn is_block_tag(tag: &str) -> bool {
    let tag = tag.strip_prefix('/').unwrap_or(tag);
    matches!(
        tag,
        "p" | "div" | "br" | "hr" | "h1" | "h2" | "h3" | "h4" | "h5" | "h6" | "li" | "ul"
            | "ol" | "table" | "tr" | "blockquote" | "pre" | "section" | "article" | "header"
            | "footer" | "nav" | "main"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_strip_html_simple() {
        assert_eq!(
            strip_html_simple("<p>Hello <b>world</b></p>"),
            "Hello world"
        );
    }

    #[test]
    fn test_strip_html_simple_entities() {
        assert_eq!(strip_html_simple("Tom &amp; Jerry"), "Tom & Jerry");
    }

    #[test]
    fn test_strip_html_simple_script() {
        let html = "<p>Hello</p><script>alert('x')</script><p>World</p>";
        let result = strip_html_simple(html);
        assert!(result.contains("Hello"));
        assert!(result.contains("World"));
        assert!(!result.contains("alert"));
    }

    #[test]
    fn test_readability_extracts_article() {
        let html = r#"
            <html><body>
                <nav><a href="/">Home</a> <a href="/about">About</a></nav>
                <article>
                    <h1>Rust Programming</h1>
                    <p>Rust is a systems programming language focused on safety and performance.</p>
                    <p>It prevents memory errors at compile time without garbage collection.</p>
                </article>
                <footer>Copyright 2026</footer>
            </body></html>
        "#;
        let result = extract_with_readability(html, "https://example.com");
        assert!(result.is_some(), "Readability should extract article");
        let text = result.unwrap();
        assert!(text.contains("systems programming"), "should contain article text");
        assert!(!text.contains("Copyright"), "should not contain footer");
    }

    #[test]
    fn test_readability_fallback_on_minimal_html() {
        let html = "<p>Just a paragraph</p>";
        let result = extract_with_readability(html, "https://example.com");
        let _ = result;
    }

    #[test]
    #[ignore] // Requires Chromium installed + network access
    fn test_live_search() {
        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(async {
            let search = WebSearch::new().await.unwrap();
            let results = search.search("rust programming language", 5).await.unwrap();
            assert!(!results.is_empty(), "expected search results");
            for r in &results {
                assert!(!r.title.is_empty());
                assert!(!r.url.is_empty());
                println!("  {} — {}", r.title, r.url);
                if !r.snippet.is_empty() {
                    println!("    {}", r.snippet);
                }
            }
        });
    }
}
