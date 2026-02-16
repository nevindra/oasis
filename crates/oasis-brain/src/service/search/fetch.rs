use readability_rust::Readability;
use reqwest::Client;

use super::types::{SearchResult, SearchResultWithContent};
use super::{
    truncate_str, WebSearch, BROWSER_PAGE_WAIT, CHROME_USER_AGENT, MAX_PAGE_CHARS,
    MIN_USEFUL_CHARS,
};

impl WebSearch {
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
