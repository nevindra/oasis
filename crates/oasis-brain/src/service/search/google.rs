use oasis_core::error::OasisError;
use std::time::Duration;

use super::types::SearchResult;
use super::{WebSearch, CHROME_USER_AGENT, SEARCH_RENDER_WAIT};

impl WebSearch {
    /// Search Google using the headless browser and return parsed results.
    ///
    /// Opens a new tab with stealth mode enabled, navigates to Google homepage
    /// first, then types the query and submits — simulating human behavior to
    /// avoid CAPTCHA detection. Results are deduplicated and cleaned.
    pub async fn search(&self, query: &str, max_results: usize) -> Result<Vec<SearchResult>, OasisError> {
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
}
