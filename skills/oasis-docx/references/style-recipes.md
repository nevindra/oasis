# DOCX Style Recipes

Common document patterns for different use cases.

## Corporate Report

```json
{
    "style": "business",
    "page": { "margins": { "top": 1, "bottom": 1, "left": 1.25, "right": 1.25 } },
    "content": [
        { "type": "heading", "level": 1, "text": "Q4 Financial Report" },
        { "type": "paragraph", "text": "Prepared for the Board of Directors", "italic": true },
        { "type": "toc" },
        { "type": "page_break" },
        { "type": "heading", "level": 2, "text": "Executive Summary" },
        { "type": "paragraph", "text": "..." }
    ]
}
```

## Academic Paper (APA-style)

```json
{
    "style": "academic",
    "page": { "margins": { "top": 1, "bottom": 1, "left": 1, "right": 1 } },
    "content": [
        { "type": "heading", "level": 1, "text": "Title of Paper" },
        { "type": "paragraph", "text": "Author Name", "align": "center" },
        { "type": "paragraph", "text": "University Name", "align": "center", "italic": true },
        { "type": "page_break" },
        { "type": "heading", "level": 2, "text": "Abstract" },
        { "type": "paragraph", "text": "..." }
    ]
}
```

## Business Letter

```json
{
    "style": "formal",
    "content": [
        { "type": "paragraph", "text": "March 27, 2026" },
        { "type": "paragraph", "text": "" },
        { "type": "paragraph", "text": "Dear Mr. Smith," },
        { "type": "paragraph", "text": "I am writing to..." },
        { "type": "paragraph", "text": "" },
        { "type": "paragraph", "text": "Sincerely," },
        { "type": "paragraph", "text": "Your Name" }
    ]
}
```

## Meeting Minutes

```json
{
    "style": "minimal",
    "content": [
        { "type": "heading", "level": 1, "text": "Meeting Minutes — March 27, 2026" },
        { "type": "table", "headers": ["Item", "Value"], "rows": [
            ["Date", "March 27, 2026"],
            ["Attendees", "Alice, Bob, Charlie"],
            ["Location", "Conference Room A"]
        ]},
        { "type": "heading", "level": 2, "text": "Agenda Items" },
        { "type": "list", "ordered": true, "items": ["Review Q4 results", "Discuss roadmap", "Assign action items"] },
        { "type": "heading", "level": 2, "text": "Action Items" },
        { "type": "table", "headers": ["Action", "Owner", "Due"], "rows": [
            ["Prepare budget draft", "Alice", "April 3"],
            ["Review vendor proposals", "Bob", "April 5"]
        ]}
    ]
}
```
