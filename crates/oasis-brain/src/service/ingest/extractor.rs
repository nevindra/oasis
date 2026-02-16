use oasis_core::error::Result;

/// Supported content types for text extraction.
pub enum ContentType {
    PlainText,
    Markdown,
    Html,
}

impl ContentType {
    /// Detect content type from a MIME type string.
    pub fn from_mime(mime: &str) -> Self {
        match mime {
            "text/plain" => Self::PlainText,
            "text/markdown" => Self::Markdown,
            "text/html" => Self::Html,
            _ => Self::PlainText,
        }
    }

    /// Detect content type from a file extension.
    pub fn from_extension(ext: &str) -> Self {
        match ext {
            "md" | "markdown" => Self::Markdown,
            "html" | "htm" => Self::Html,
            _ => Self::PlainText,
        }
    }
}

/// Extract plain text from content of a given type.
pub fn extract_text(content: &str, content_type: ContentType) -> Result<String> {
    match content_type {
        ContentType::PlainText => Ok(content.to_string()),
        ContentType::Markdown => Ok(strip_markdown(content)),
        ContentType::Html => Ok(strip_html(content)),
    }
}

/// Remove common Markdown formatting and return plain text.
///
/// Handles: headings (#), emphasis (*/_), bold (**/__), strikethrough (~~),
/// inline code (`), code fences (```), links [text](url), images ![alt](url),
/// blockquotes (>), horizontal rules (---/***), and list markers.
fn strip_markdown(content: &str) -> String {
    let mut result = String::with_capacity(content.len());
    let mut in_code_fence = false;

    for line in content.lines() {
        let trimmed = line.trim();

        // Toggle code fences
        if trimmed.starts_with("```") {
            in_code_fence = !in_code_fence;
            // Include content inside code fences as-is (without the fence markers)
            continue;
        }

        if in_code_fence {
            result.push_str(line);
            result.push('\n');
            continue;
        }

        // Skip horizontal rules: lines that are only ---, ***, or ___
        if trimmed.len() >= 3 {
            let hr_chars: Vec<char> = trimmed.chars().collect();
            let is_hr = (hr_chars.iter().all(|&c| c == '-' || c == ' ')
                && hr_chars.iter().filter(|&&c| c == '-').count() >= 3)
                || (hr_chars.iter().all(|&c| c == '*' || c == ' ')
                    && hr_chars.iter().filter(|&&c| c == '*').count() >= 3)
                || (hr_chars.iter().all(|&c| c == '_' || c == ' ')
                    && hr_chars.iter().filter(|&&c| c == '_').count() >= 3);
            if is_hr {
                result.push('\n');
                continue;
            }
        }

        // Strip heading markers: "## heading" -> "heading"
        let line = strip_heading_markers(trimmed);

        // Strip blockquote markers: "> text" -> "text"
        let line = strip_blockquote_markers(&line);

        // Strip unordered list markers: "- item", "* item", "+ item"
        let line = strip_list_markers(&line);

        // Process inline formatting
        let line = strip_inline_markdown(&line);

        result.push_str(&line);
        result.push('\n');
    }

    collapse_whitespace(&result)
}

/// Remove heading markers (# chars at start of line).
fn strip_heading_markers(line: &str) -> String {
    let trimmed = line.trim_start();
    if trimmed.starts_with('#') {
        let after_hashes = trimmed.trim_start_matches('#');
        after_hashes.trim_start().to_string()
    } else {
        line.to_string()
    }
}

/// Remove blockquote markers (> at start of line).
fn strip_blockquote_markers(line: &str) -> String {
    let trimmed = line.trim_start();
    if let Some(rest) = trimmed.strip_prefix('>') {
        rest.trim_start().to_string()
    } else {
        line.to_string()
    }
}

/// Remove unordered list markers (-, *, +) at start of line.
fn strip_list_markers(line: &str) -> String {
    let trimmed = line.trim_start();

    // Check for unordered list markers: "- ", "* ", "+ "
    if (trimmed.starts_with("- ") || trimmed.starts_with("* ") || trimmed.starts_with("+ "))
        && trimmed.len() > 2
    {
        return trimmed[2..].to_string();
    }

    // Check for ordered list markers: "1. ", "2. ", etc.
    if let Some(dot_pos) = trimmed.find(". ") {
        let prefix = &trimmed[..dot_pos];
        if !prefix.is_empty() && prefix.chars().all(|c| c.is_ascii_digit()) {
            return trimmed[dot_pos + 2..].to_string();
        }
    }

    line.to_string()
}

/// Process inline Markdown formatting within a line.
/// Handles: links, images, bold, italic, strikethrough, inline code.
fn strip_inline_markdown(line: &str) -> String {
    let mut result = String::with_capacity(line.len());
    let chars: Vec<char> = line.chars().collect();
    let len = chars.len();
    let mut i = 0;

    while i < len {
        // Handle images: ![alt](url) -> alt
        if i + 1 < len && chars[i] == '!' && chars[i + 1] == '[' {
            if let Some((alt_text, skip)) = extract_link_text(&chars, i + 1) {
                result.push_str(&alt_text);
                i += 1 + skip; // +1 for the '!'
                continue;
            }
        }

        // Handle links: [text](url) -> text
        if chars[i] == '[' {
            if let Some((link_text, skip)) = extract_link_text(&chars, i) {
                result.push_str(&link_text);
                i += skip;
                continue;
            }
        }

        // Handle inline code: `code` -> code
        if chars[i] == '`' {
            if let Some((code_text, skip)) = extract_backtick_content(&chars, i) {
                result.push_str(&code_text);
                i += skip;
                continue;
            }
        }

        // Handle bold+italic: ***text*** or ___text___
        if i + 2 < len
            && chars[i] == chars[i + 1]
            && chars[i] == chars[i + 2]
            && (chars[i] == '*' || chars[i] == '_')
        {
            let marker = chars[i];
            if let Some(end) = find_closing_marker(&chars, i + 3, marker, 3) {
                for &c in &chars[i + 3..end] {
                    result.push(c);
                }
                i = end + 3;
                continue;
            }
        }

        // Handle bold: **text** or __text__
        if i + 1 < len
            && chars[i] == chars[i + 1]
            && (chars[i] == '*' || chars[i] == '_')
        {
            let marker = chars[i];
            if let Some(end) = find_closing_marker(&chars, i + 2, marker, 2) {
                for &c in &chars[i + 2..end] {
                    result.push(c);
                }
                i = end + 2;
                continue;
            }
        }

        // Handle strikethrough: ~~text~~
        if i + 1 < len && chars[i] == '~' && chars[i + 1] == '~' {
            if let Some(end) = find_closing_marker(&chars, i + 2, '~', 2) {
                for &c in &chars[i + 2..end] {
                    result.push(c);
                }
                i = end + 2;
                continue;
            }
        }

        // Handle italic: *text* or _text_
        if chars[i] == '*' || chars[i] == '_' {
            let marker = chars[i];
            if let Some(end) = find_closing_marker(&chars, i + 1, marker, 1) {
                for &c in &chars[i + 1..end] {
                    result.push(c);
                }
                i = end + 1;
                continue;
            }
        }

        result.push(chars[i]);
        i += 1;
    }

    result
}

/// Extract link text from [text](url) pattern starting at position of '['.
/// Returns (text, chars_consumed) or None.
fn extract_link_text(chars: &[char], start: usize) -> Option<(String, usize)> {
    let len = chars.len();
    if start >= len || chars[start] != '[' {
        return None;
    }

    // Find closing ']'
    let mut depth = 0;
    let mut bracket_end = None;
    for j in start..len {
        if chars[j] == '[' {
            depth += 1;
        } else if chars[j] == ']' {
            depth -= 1;
            if depth == 0 {
                bracket_end = Some(j);
                break;
            }
        }
    }

    let bracket_end = bracket_end?;

    // Check for '(' immediately after ']'
    if bracket_end + 1 < len && chars[bracket_end + 1] == '(' {
        // Find closing ')'
        let mut paren_end = None;
        let mut paren_depth = 0;
        for j in (bracket_end + 1)..len {
            if chars[j] == '(' {
                paren_depth += 1;
            } else if chars[j] == ')' {
                paren_depth -= 1;
                if paren_depth == 0 {
                    paren_end = Some(j);
                    break;
                }
            }
        }

        if let Some(paren_end) = paren_end {
            let text: String = chars[start + 1..bracket_end].iter().collect();
            let consumed = paren_end - start + 1;
            return Some((text, consumed));
        }
    }

    None
}

/// Extract content between backticks. Returns (content, chars_consumed).
fn extract_backtick_content(chars: &[char], start: usize) -> Option<(String, usize)> {
    let len = chars.len();
    if start >= len || chars[start] != '`' {
        return None;
    }

    // Count opening backticks
    let mut tick_count = 0;
    let mut pos = start;
    while pos < len && chars[pos] == '`' {
        tick_count += 1;
        pos += 1;
    }

    // Find matching closing backticks
    let mut j = pos;
    while j < len {
        if chars[j] == '`' {
            let mut closing_count = 0;
            let close_start = j;
            while j < len && chars[j] == '`' {
                closing_count += 1;
                j += 1;
            }
            if closing_count == tick_count {
                let text: String = chars[pos..close_start].iter().collect();
                return Some((text, j - start));
            }
        } else {
            j += 1;
        }
    }

    None
}

/// Find closing marker sequence (e.g., ** or *) in char array.
/// Returns the start index of the closing marker.
fn find_closing_marker(chars: &[char], start: usize, marker: char, count: usize) -> Option<usize> {
    let len = chars.len();
    if start + count > len {
        return None;
    }

    let mut i = start;
    while i + count <= len {
        let mut matches = true;
        for k in 0..count {
            if chars[i + k] != marker {
                matches = false;
                break;
            }
        }
        if matches {
            // Make sure we didn't find an empty span
            if i > start {
                return Some(i);
            }
        }
        i += 1;
    }

    None
}

/// Remove HTML tags and decode common entities to produce plain text.
///
/// Uses a simple state machine: iterate chars, track whether we're inside
/// a `<...>` tag, skip tag content, keep text content.
fn strip_html(content: &str) -> String {
    let mut result = String::with_capacity(content.len());
    let mut in_tag = false;
    let mut in_script = false;
    let mut in_style = false;
    let mut tag_name = String::new();
    let mut collecting_tag_name = false;

    let chars: Vec<char> = content.chars().collect();
    let len = chars.len();
    let mut i = 0;

    while i < len {
        if chars[i] == '<' {
            in_tag = true;
            tag_name.clear();
            collecting_tag_name = true;
            i += 1;
            continue;
        }

        if in_tag {
            if collecting_tag_name {
                if chars[i].is_whitespace() || chars[i] == '>' || (chars[i] == '/' && !tag_name.is_empty()) {
                    collecting_tag_name = false;
                    let lower = tag_name.to_lowercase();
                    if lower == "script" {
                        in_script = true;
                    } else if lower == "/script" {
                        in_script = false;
                    } else if lower == "style" {
                        in_style = true;
                    } else if lower == "/style" {
                        in_style = false;
                    }
                    // Add whitespace for block-level elements
                    if is_block_tag(&lower) {
                        result.push('\n');
                    }
                } else {
                    tag_name.push(chars[i]);
                }
            }

            if chars[i] == '>' {
                in_tag = false;
                // Check if the tag name (still in buffer) needs finalizing
                if collecting_tag_name {
                    collecting_tag_name = false;
                    let lower = tag_name.to_lowercase();
                    if lower == "script" {
                        in_script = true;
                    } else if lower == "/script" {
                        in_script = false;
                    } else if lower == "style" {
                        in_style = true;
                    } else if lower == "/style" {
                        in_style = false;
                    }
                    if is_block_tag(&lower) {
                        result.push('\n');
                    }
                }
            }

            i += 1;
            continue;
        }

        // Skip content inside <script> or <style> tags
        if in_script || in_style {
            i += 1;
            continue;
        }

        // Handle HTML entities
        if chars[i] == '&' {
            if let Some((decoded, skip)) = decode_entity(&chars, i) {
                result.push_str(&decoded);
                i += skip;
                continue;
            }
        }

        result.push(chars[i]);
        i += 1;
    }

    collapse_whitespace(&result)
}

/// Check if a tag name represents a block-level element that should insert whitespace.
fn is_block_tag(tag: &str) -> bool {
    let tag = tag.strip_prefix('/').unwrap_or(tag);
    matches!(
        tag,
        "p" | "div"
            | "br"
            | "hr"
            | "h1"
            | "h2"
            | "h3"
            | "h4"
            | "h5"
            | "h6"
            | "li"
            | "ul"
            | "ol"
            | "table"
            | "tr"
            | "blockquote"
            | "pre"
            | "section"
            | "article"
            | "header"
            | "footer"
            | "nav"
            | "main"
    )
}

/// Decode an HTML entity starting at position i.
/// Returns (decoded_string, chars_consumed) or None.
fn decode_entity(chars: &[char], start: usize) -> Option<(String, usize)> {
    let len = chars.len();
    if start >= len || chars[start] != '&' {
        return None;
    }

    // Find the semicolon
    let max_entity_len = 10; // Entities are short
    let end_limit = (start + max_entity_len).min(len);

    for j in (start + 1)..end_limit {
        if chars[j] == ';' {
            let entity: String = chars[start..=j].iter().collect();
            let decoded = match entity.as_str() {
                "&amp;" => "&",
                "&lt;" => "<",
                "&gt;" => ">",
                "&quot;" => "\"",
                "&#39;" | "&apos;" => "'",
                "&nbsp;" => " ",
                "&mdash;" => "\u{2014}",
                "&ndash;" => "\u{2013}",
                "&hellip;" => "\u{2026}",
                "&copy;" => "\u{00A9}",
                "&reg;" => "\u{00AE}",
                "&trade;" => "\u{2122}",
                _ => return None,
            };
            return Some((decoded.to_string(), j - start + 1));
        }
        // If we hit a space or another '&', this isn't a valid entity
        if chars[j].is_whitespace() || chars[j] == '&' {
            return None;
        }
    }

    None
}

/// Collapse runs of whitespace (multiple newlines, spaces) into cleaner text.
fn collapse_whitespace(text: &str) -> String {
    let mut result = String::with_capacity(text.len());
    let mut last_was_newline = false;
    let mut newline_count = 0;

    for line in text.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            newline_count += 1;
            if newline_count <= 2 && !result.is_empty() {
                last_was_newline = true;
            }
        } else {
            if last_was_newline {
                result.push('\n');
                if newline_count > 1 {
                    result.push('\n');
                }
            }
            if !result.is_empty() && !last_was_newline {
                result.push('\n');
            }
            result.push_str(trimmed);
            last_was_newline = false;
            newline_count = 0;
        }
    }

    result.trim().to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_content_type_from_mime() {
        assert!(matches!(
            ContentType::from_mime("text/plain"),
            ContentType::PlainText
        ));
        assert!(matches!(
            ContentType::from_mime("text/markdown"),
            ContentType::Markdown
        ));
        assert!(matches!(
            ContentType::from_mime("text/html"),
            ContentType::Html
        ));
        assert!(matches!(
            ContentType::from_mime("application/json"),
            ContentType::PlainText
        ));
    }

    #[test]
    fn test_content_type_from_extension() {
        assert!(matches!(
            ContentType::from_extension("md"),
            ContentType::Markdown
        ));
        assert!(matches!(
            ContentType::from_extension("html"),
            ContentType::Html
        ));
        assert!(matches!(
            ContentType::from_extension("txt"),
            ContentType::PlainText
        ));
    }

    #[test]
    fn test_strip_markdown_headings() {
        let input = "# Title\n## Subtitle\n### Section";
        let result = strip_markdown(input);
        assert!(result.contains("Title"));
        assert!(result.contains("Subtitle"));
        assert!(result.contains("Section"));
        assert!(!result.contains('#'));
    }

    #[test]
    fn test_strip_markdown_emphasis() {
        let input = "This is *italic* and **bold** and ***both***";
        let result = strip_markdown(input);
        assert!(result.contains("italic"));
        assert!(result.contains("bold"));
        assert!(result.contains("both"));
        assert!(!result.contains('*'));
    }

    #[test]
    fn test_strip_markdown_links() {
        let input = "Click [here](https://example.com) for more";
        let result = strip_markdown(input);
        assert!(result.contains("here"));
        assert!(!result.contains("https://example.com"));
        assert!(!result.contains('['));
        assert!(!result.contains(']'));
    }

    #[test]
    fn test_strip_markdown_code() {
        let input = "Use `println!` to print\n```\ncode block\n```";
        let result = strip_markdown(input);
        assert!(result.contains("println!"));
        assert!(result.contains("code block"));
    }

    #[test]
    fn test_strip_html_basic() {
        let input = "<p>Hello <b>world</b></p>";
        let result = strip_html(input);
        assert!(result.contains("Hello"));
        assert!(result.contains("world"));
        assert!(!result.contains('<'));
        assert!(!result.contains('>'));
    }

    #[test]
    fn test_strip_html_entities() {
        let input = "Tom &amp; Jerry &lt;3 &quot;cheese&quot;";
        let result = strip_html(input);
        assert!(result.contains("Tom & Jerry"));
        assert!(result.contains("<3"));
        assert!(result.contains("\"cheese\""));
    }

    #[test]
    fn test_strip_html_script_style() {
        let input = "<p>Hello</p><script>alert('xss')</script><style>.x{color:red}</style><p>World</p>";
        let result = strip_html(input);
        assert!(result.contains("Hello"));
        assert!(result.contains("World"));
        assert!(!result.contains("alert"));
        assert!(!result.contains("color"));
    }

    #[test]
    fn test_extract_text_plaintext() {
        let result = extract_text("hello world", ContentType::PlainText).unwrap();
        assert_eq!(result, "hello world");
    }
}
