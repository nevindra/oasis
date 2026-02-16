use serde::Deserialize;

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
