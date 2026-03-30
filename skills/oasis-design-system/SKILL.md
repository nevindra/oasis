---
name: oasis-design-system
description: Shared design tokens for document generation — color palettes, typography, spacing scale. Use when generating any document (PDF, DOCX, XLSX, PPTX) to ensure visual consistency.
compatibility: No special requirements
tags: [design, document]
---

# Design System

Consistent visual language across all document formats.

## Color Palettes

Use one palette per document. Every document skill references these tokens.

### Corporate
| Token | Hex | Usage |
|-------|-----|-------|
| primary | #1B2A4A | Headings, borders, primary actions |
| secondary | #2D5F8A | Subheadings, secondary elements |
| accent | #E8734A | Highlights, call-to-action, data emphasis |
| light | #F5F5F5 | Backgrounds, alternating rows |
| bg | #FFFFFF | Page background |
| text | #333333 | Body text |
| muted | #6B7280 | Captions, footnotes |

### Minimal
| Token | Hex | Usage |
|-------|-----|-------|
| primary | #111111 | Headings |
| secondary | #444444 | Subheadings |
| accent | #0066CC | Links, highlights |
| light | #FAFAFA | Backgrounds |
| bg | #FFFFFF | Page background |
| text | #222222 | Body text |
| muted | #999999 | Captions |

### Bold
| Token | Hex | Usage |
|-------|-----|-------|
| primary | #FF6B35 | Headings, primary actions |
| secondary | #004E89 | Subheadings, charts |
| accent | #2EC4B6 | Highlights, badges |
| light | #F0F4F8 | Backgrounds |
| bg | #FFFFFF | Page background |
| text | #1A1A2E | Body text |
| muted | #7C8DB0 | Captions |

### Dark
| Token | Hex | Usage |
|-------|-----|-------|
| primary | #E2E8F0 | Headings |
| secondary | #94A3B8 | Subheadings |
| accent | #38BDF8 | Highlights, links |
| light | #1E293B | Card backgrounds |
| bg | #0F172A | Page background |
| text | #CBD5E1 | Body text |
| muted | #64748B | Captions |

## Typography

### Font Stacks

| Name | Stack | Use for |
|------|-------|---------|
| sans | `Inter, system-ui, -apple-system, sans-serif` | Body text, UI elements |
| serif | `Merriweather, Georgia, serif` | Long-form reading, academic |
| mono | `JetBrains Mono, Fira Code, monospace` | Code, data, technical |

### Scale

| Level | Size | Weight | Use for |
|-------|------|--------|---------|
| display | 36px / 2.25rem | 700 | Cover page titles |
| h1 | 28px / 1.75rem | 700 | Document title |
| h2 | 22px / 1.375rem | 600 | Section headings |
| h3 | 18px / 1.125rem | 600 | Subsection headings |
| body | 14px / 0.875rem | 400 | Body text |
| small | 12px / 0.75rem | 400 | Captions, footnotes |
| tiny | 10px / 0.625rem | 400 | Legal text, page numbers |

## Spacing Scale

Use consistent spacing based on a 4px grid:

| Token | Value | Use for |
|-------|-------|---------|
| xs | 4px / 0.25rem | Tight inline spacing |
| sm | 8px / 0.5rem | Between related elements |
| md | 16px / 1rem | Between sections |
| lg | 24px / 1.5rem | Between major sections |
| xl | 32px / 2rem | Page margins, large gaps |
| 2xl | 48px / 3rem | Cover page spacing |
| 3xl | 64px / 4rem | Cover page hero spacing |
