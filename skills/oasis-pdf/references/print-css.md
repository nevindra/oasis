# Print CSS Reference

Quick reference for CSS print media features used in PDF generation.

## @page Rule

```css
@page {
    size: A4;              /* A4, Letter, Legal, A3 */
    size: A4 landscape;    /* Landscape orientation */
    margin: 1in;           /* All sides */
    margin: 1in 0.75in;   /* Top/bottom, left/right */
}
```

## Page Breaks

```css
.page-break {
    page-break-before: always;   /* Force break before element */
}
.no-break {
    page-break-inside: avoid;    /* Prevent break inside element */
}
.keep-with-next {
    page-break-after: avoid;     /* Keep with following element */
}
```

## Tailwind + Print

Hide elements in print:
```html
<nav class="print:hidden">Navigation</nav>
```

Show elements only in print:
```html
<div class="hidden print:block">Print-only content</div>
```

## Page Sizes

| Name | Width | Height |
|------|-------|--------|
| A4 | 210mm | 297mm |
| Letter | 8.5in | 11in |
| Legal | 8.5in | 14in |
| A3 | 297mm | 420mm |

## Header/Footer Templates

Playwright supports special CSS classes in header/footer templates:

```html
<!-- Page number -->
<span class="pageNumber"></span>

<!-- Total pages -->
<span class="totalPages"></span>

<!-- Date -->
<span class="date"></span>

<!-- Title -->
<span class="title"></span>
```

Example footer:
```html
<div style="font-size:10px; text-align:center; width:100%;">
    Page <span class="pageNumber"></span> of <span class="totalPages"></span>
</div>
```

## Common Patterns

### Avoid Orphan Headings
```css
h1, h2, h3 {
    page-break-after: avoid;
}
```

### Table Header Repeat
```css
thead {
    display: table-header-group;  /* Repeat on every page */
}
```

### Full-Bleed Background
```css
@media print {
    body {
        -webkit-print-color-adjust: exact;
        print-color-adjust: exact;
    }
}
```
Note: Playwright's `printBackground: true` handles this automatically.
