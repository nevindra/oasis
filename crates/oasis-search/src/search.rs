use chromiumoxide::browser::{Browser, BrowserConfig};
use futures::StreamExt;
use oasis_core::error::{OasisError, Result};
use readability_rust::Readability;
use reqwest::Client;
use serde::Deserialize;
use std::time::Duration;
use tokio::sync::Mutex;

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

/// An interactive element found on a web page.
#[derive(Debug, Clone, Deserialize, Default)]
pub struct PageElement {
    pub tag: String,
    #[serde(rename = "type")]
    pub element_type: String,
    pub label: String,
    pub value: String,
}

/// A snapshot of the current browser page state for LLM consumption.
#[derive(Debug, Clone)]
pub struct PageSnapshot {
    pub url: String,
    pub title: String,
    pub text_content: String,
    pub elements: Vec<PageElement>,
}

impl PageSnapshot {
    /// Format the snapshot as text for LLM consumption.
    pub fn to_llm_text(&self) -> String {
        let mut out = format!("Page: {}\nTitle: {}\n\n", self.url, self.title);

        if !self.text_content.is_empty() {
            out.push_str("--- Content ---\n");
            out.push_str(&self.text_content);
            out.push_str("\n\n");
        }

        if !self.elements.is_empty() {
            out.push_str("--- Interactive Elements ---\n");
            for (i, el) in self.elements.iter().enumerate() {
                let mut desc = format!("[{}] {}", i + 1, el.tag);
                if !el.element_type.is_empty() {
                    desc.push_str(&format!(" ({})", el.element_type));
                }
                desc.push_str(&format!(" \"{}\"", el.label));
                if !el.value.is_empty() && el.element_type != "link" {
                    desc.push_str(&format!(" value=\"{}\"", el.value));
                }
                out.push('\n');
                out.push_str(&desc);
            }
            out.push('\n');
        } else {
            out.push_str("(No interactive elements found)\n");
        }

        out
    }
}

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
/// Time to wait after a browser interaction (click/type) for the page to update.
const BROWSE_ACTION_WAIT: Duration = Duration::from_secs(2);
/// Maximum number of interactive elements to report in a page snapshot.
const MAX_ELEMENTS: usize = 50;
/// Maximum text content length in page snapshots.
const MAX_SNAPSHOT_TEXT: usize = 5000;

/// Truncate a string to at most `max` bytes, ensuring the cut is on a char boundary.
fn truncate_str(s: &str, max: usize) -> &str {
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
            Some(truncate_str(&text, MAX_PAGE_CHARS).to_string())
        } else {
            Some(text)
        }
    }

    // ─── Interactive browser session ──────────────────────────────────

    /// Navigate to a URL in the interactive browser session and return a page snapshot.
    /// Closes any existing session first.
    pub async fn browse_to(&self, url: &str) -> Result<PageSnapshot> {
        // Close existing session
        let mut session = self.active_page.lock().await;
        if let Some(old_page) = session.take() {
            let _ = old_page.close().await;
        }

        let page = self
            .browser
            .new_page("about:blank")
            .await
            .map_err(|e| OasisError::Http {
                status: 0,
                body: format!("new page failed: {e}"),
            })?;

        let _ = page
            .enable_stealth_mode_with_agent(CHROME_USER_AGENT)
            .await;

        page.goto(url).await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("navigation failed: {e}"),
        })?;
        tokio::time::sleep(BROWSER_PAGE_WAIT).await;

        let snapshot = extract_page_snapshot(&page).await?;
        *session = Some(page);
        Ok(snapshot)
    }

    /// Click an interactive element by its 1-based index number.
    pub async fn page_click(&self, element_idx: usize) -> Result<PageSnapshot> {
        let session = self.active_page.lock().await;
        let page = session.as_ref().ok_or_else(|| OasisError::Http {
            status: 0,
            body: "No active browser session. Use browse_url first.".to_string(),
        })?;

        // Capture current URL before clicking
        let url_before: String = page
            .evaluate("window.location.href")
            .await
            .map(|v| v.into_value().unwrap_or_default())
            .unwrap_or_default();

        let selector = format!("[data-oi=\"{element_idx}\"]");
        let el = page.find_element(&selector).await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("element [{element_idx}] not found: {e}"),
        })?;

        el.click().await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("click failed: {e}"),
        })?;
        tokio::time::sleep(BROWSE_ACTION_WAIT).await;

        // If URL changed (navigation), wait longer for the new page to load
        let url_after: String = page
            .evaluate("window.location.href")
            .await
            .map(|v| v.into_value().unwrap_or_default())
            .unwrap_or_default();
        if url_after != url_before {
            tokio::time::sleep(BROWSER_PAGE_WAIT).await;
            // Scroll incrementally to trigger lazy/virtual scroll loading
            let _ = page.evaluate(
                r#"(async () => {
                    for (let i = 0; i < 5; i++) {
                        window.scrollBy(0, window.innerHeight);
                        await new Promise(r => setTimeout(r, 400));
                    }
                    window.scrollTo(0, 0);
                })()"#
            ).await;
            tokio::time::sleep(Duration::from_secs(3)).await;
        }

        extract_page_snapshot(page).await
    }

    /// Type text into an interactive element by its 1-based index number.
    /// Selects existing value before typing so the new text replaces it.
    pub async fn page_type_into(&self, element_idx: usize, text: &str) -> Result<PageSnapshot> {
        let session = self.active_page.lock().await;
        let page = session.as_ref().ok_or_else(|| OasisError::Http {
            status: 0,
            body: "No active browser session. Use browse_url first.".to_string(),
        })?;

        let selector = format!("[data-oi=\"{element_idx}\"]");
        let el = page.find_element(&selector).await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("element [{element_idx}] not found: {e}"),
        })?;

        // Click to focus — this may open a search modal in SPAs
        el.click().await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("focus failed: {e}"),
        })?;
        tokio::time::sleep(Duration::from_millis(500)).await;

        // Check if the clicked element is a native input/textarea, or if a new
        // focused input appeared (common SPA pattern: click div → modal with input).
        // Tag the target input with data-oi-focus so we can find it with find_element.
        let focus_js = r#"(() => {
            document.querySelectorAll('[data-oi-focus]').forEach(e => e.removeAttribute('data-oi-focus'));
            const active = document.activeElement;
            if (active && (active.tagName === 'INPUT' || active.tagName === 'TEXTAREA')) {
                active.setAttribute('data-oi-focus', '1');
                if (active.select) active.select();
                return 'found';
            }
            // Look for a newly visible input (e.g. search modal)
            const inputs = document.querySelectorAll('input:not([type="hidden"])');
            for (const inp of inputs) {
                const r = inp.getBoundingClientRect();
                if (r.width > 0 && r.height > 0 && getComputedStyle(inp).visibility !== 'hidden') {
                    inp.focus();
                    inp.setAttribute('data-oi-focus', '1');
                    if (inp.select) inp.select();
                    return 'found';
                }
            }
            return 'none';
        })()"#;
        let focus_result: String = page
            .evaluate(focus_js)
            .await
            .map(|v| v.into_value().unwrap_or_default())
            .unwrap_or_default();
        tokio::time::sleep(Duration::from_millis(100)).await;

        // Type into the focused input, or fall back to the originally clicked element
        let type_target = if focus_result == "found" {
            page.find_element("[data-oi-focus]").await.unwrap_or(el)
        } else {
            el
        };
        type_target.type_str(text).await.map_err(|e| OasisError::Http {
            status: 0,
            body: format!("typing failed: {e}"),
        })?;

        // Dispatch React-compatible events so controlled components pick up the value.
        // React overrides the native value setter, so we use the native setter + synthetic events.
        let react_js = format!(
            r#"(() => {{
                const el = document.querySelector('[data-oi-focus]') || document.activeElement;
                if (!el) return;
                const nativeSetter = Object.getOwnPropertyDescriptor(
                    window.HTMLInputElement.prototype, 'value'
                )?.set;
                if (nativeSetter) nativeSetter.call(el, '{text_escaped}');
                el.dispatchEvent(new Event('input', {{ bubbles: true }}));
                el.dispatchEvent(new Event('change', {{ bubbles: true }}));
            }})()"#,
            text_escaped = text.replace('\'', "\\'").replace('\n', "\\n")
        );
        let _ = page.evaluate(react_js).await;

        // Wait longer for autocomplete/suggestions to appear
        tokio::time::sleep(BROWSE_ACTION_WAIT).await;

        extract_page_snapshot(page).await
    }

    /// Read the current page state without any interaction.
    pub async fn read_page(&self) -> Result<PageSnapshot> {
        let session = self.active_page.lock().await;
        let page = session.as_ref().ok_or_else(|| OasisError::Http {
            status: 0,
            body: "No active browser session. Use browse_url first.".to_string(),
        })?;

        extract_page_snapshot(page).await
    }

    /// Close the active browser session, freeing resources.
    pub async fn close_browse_session(&self) {
        let mut session = self.active_page.lock().await;
        if let Some(page) = session.take() {
            let _ = page.close().await;
        }
    }
}

/// Extract a snapshot of the current page state: URL, title, text content,
/// and a numbered list of interactive elements tagged with `data-oi` attributes.
async fn extract_page_snapshot(page: &chromiumoxide::Page) -> Result<PageSnapshot> {
    let url: String = page
        .evaluate("window.location.href")
        .await
        .map_err(|e| OasisError::Http {
            status: 0,
            body: format!("url eval failed: {e}"),
        })?
        .into_value()
        .unwrap_or_default();

    let title: String = page
        .evaluate("document.title || ''")
        .await
        .map_err(|e| OasisError::Http {
            status: 0,
            body: format!("title eval failed: {e}"),
        })?
        .into_value()
        .unwrap_or_default();

    let text_js = format!(
        r#"(() => {{
            const c = document.body.cloneNode(true);
            c.querySelectorAll('script,style,noscript,svg,iframe').forEach(e => e.remove());
            return (c.innerText || '').substring(0, {MAX_SNAPSHOT_TEXT});
        }})()"#
    );
    let text_content: String = page
        .evaluate(text_js)
        .await
        .map_err(|e| OasisError::Http {
            status: 0,
            body: format!("text eval failed: {e}"),
        })?
        .into_value()
        .unwrap_or_default();

    let elements_js = format!(
        r#"(() => {{
            document.querySelectorAll('[data-oi]').forEach(e => e.removeAttribute('data-oi'));
            let idx = 0;
            const out = [];
            const tag = (el) => {{
                if (idx >= {MAX_ELEMENTS} || el.getAttribute('data-oi')) return;
                const r = el.getBoundingClientRect();
                if (r.width === 0 || r.height === 0) return;
                idx++;
                el.setAttribute('data-oi', idx);
                const t = el.tagName.toLowerCase();
                const tp = el.type || el.getAttribute('role') || '';
                const lb = el.getAttribute('aria-label') || el.placeholder || el.getAttribute('title')
                    || (t==='a'||t==='button' ? (el.textContent||'').trim().substring(0,60) : '')
                    || el.name || el.id || '';
                out.push({{ tag: t, type: tp, label: lb, value: el.value || el.textContent?.trim()?.substring(0,60) || '' }});
            }};

            // --- Phase 1: detect if there's an overlay/modal on top ---
            // Find the topmost overlay (position fixed/absolute with high z-index)
            let overlay = null;
            document.querySelectorAll('div,section,dialog').forEach(el => {{
                const s = getComputedStyle(el);
                if ((s.position === 'fixed' || s.position === 'absolute') &&
                    el.getBoundingClientRect().width > 200 &&
                    el.getBoundingClientRect().height > 100) {{
                    const z = parseInt(s.zIndex) || 0;
                    if (z > 0 && (!overlay || z > (parseInt(getComputedStyle(overlay).zIndex) || 0))) {{
                        overlay = el;
                    }}
                }}
            }});

            // If overlay exists, prioritize its interactive elements first
            if (overlay) {{
                overlay.querySelectorAll('input:not([type="hidden"]),textarea,select').forEach(tag);
                overlay.querySelectorAll('[role="option"],[role="menuitem"],[role="combobox"],[role="searchbox"]').forEach(tag);
                // Clickable items inside overlay (cursor:pointer with text = autocomplete items)
                overlay.querySelectorAll('*').forEach(el => {{
                    const text = (el.textContent||'').trim();
                    if (!text || text.length > 80 || text.length < 2) return;
                    // Skip containers — only leaf-ish elements
                    if (el.children.length > 3) return;
                    const cs = getComputedStyle(el);
                    if (cs.cursor === 'pointer' && cs.visibility !== 'hidden') {{
                        tag(el);
                    }}
                }});
                overlay.querySelectorAll('button,[role="button"]').forEach(tag);
            }}

            // --- Phase 2: rest of the page ---
            document.querySelectorAll('input:not([type="hidden"]),textarea,select').forEach(tag);
            document.querySelectorAll('[role="combobox"],[role="searchbox"],[role="textbox"]').forEach(tag);
            document.querySelectorAll('[contenteditable="true"]').forEach(tag);
            document.querySelectorAll('[role="option"],[role="menuitem"],[role="tab"]').forEach(tag);
            document.querySelectorAll('button,[role="button"]').forEach(tag);
            document.querySelectorAll('a[href]').forEach(el => {{
                if (!(el.textContent||'').trim()) return;
                tag(el);
            }});
            return out;
        }})()"#
    );
    let elements: Vec<PageElement> = page
        .evaluate(elements_js)
        .await
        .map_err(|e| OasisError::Http {
            status: 0,
            body: format!("elements eval failed: {e}"),
        })?
        .into_value()
        .unwrap_or_default();

    Ok(PageSnapshot {
        url,
        title,
        text_content,
        elements,
    })
}

/// Parse an element reference like "1", "[1]", "#1" to a 1-based index.
pub fn parse_element_ref(s: &str) -> usize {
    let s = s
        .trim()
        .trim_start_matches('[')
        .trim_end_matches(']')
        .trim_start_matches('#');
    s.parse().unwrap_or(0)
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
        Some(truncate_str(&text, MAX_PAGE_CHARS).to_string())
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
