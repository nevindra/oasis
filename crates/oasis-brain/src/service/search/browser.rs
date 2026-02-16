use oasis_core::error::{OasisError, Result};
use std::time::Duration;

use super::types::{PageElement, PageSnapshot};
use super::{
    WebSearch, BROWSE_ACTION_WAIT, BROWSER_PAGE_WAIT, CHROME_USER_AGENT, MAX_ELEMENTS,
    MAX_SNAPSHOT_TEXT,
};

impl WebSearch {
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
            // Navigation happened — wait longer for new page to load
            tokio::time::sleep(BROWSER_PAGE_WAIT).await;
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
