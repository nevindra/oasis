# Chart.js Patterns for PDF

Charts render inside Chromium before PDF capture. Use inline `<canvas>` with Chart.js CDN.

## Setup

Add Chart.js CDN to the HTML `<head>`:

```html
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
```

## Bar Chart

```html
<div class="no-break" style="max-width: 600px; margin: 0 auto;">
    <canvas id="barChart"></canvas>
</div>
<script>
new Chart(document.getElementById('barChart'), {
    type: 'bar',
    data: {
        labels: ['Q1', 'Q2', 'Q3', 'Q4'],
        datasets: [{
            label: 'Revenue ($M)',
            data: [1.2, 1.5, 1.8, 2.1],
            backgroundColor: '#2D5F8A'
        }]
    },
    options: {
        responsive: true,
        animation: false,
        plugins: { legend: { display: true } }
    }
});
</script>
```

## Line Chart

```html
<canvas id="lineChart"></canvas>
<script>
new Chart(document.getElementById('lineChart'), {
    type: 'line',
    data: {
        labels: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun'],
        datasets: [{
            label: 'Users',
            data: [100, 150, 200, 280, 350, 420],
            borderColor: '#1B2A4A',
            backgroundColor: 'rgba(27, 42, 74, 0.1)',
            fill: true,
            tension: 0.3
        }]
    },
    options: { responsive: true, animation: false }
});
</script>
```

## Pie / Doughnut Chart

```html
<div style="max-width: 400px; margin: 0 auto;">
    <canvas id="pieChart"></canvas>
</div>
<script>
new Chart(document.getElementById('pieChart'), {
    type: 'doughnut',
    data: {
        labels: ['Product', 'Services', 'Support'],
        datasets: [{
            data: [60, 25, 15],
            backgroundColor: ['#1B2A4A', '#2D5F8A', '#E8734A']
        }]
    },
    options: { responsive: true, animation: false }
});
</script>
```

## Multi-Dataset Bar (Grouped)

```html
<canvas id="groupedBar"></canvas>
<script>
new Chart(document.getElementById('groupedBar'), {
    type: 'bar',
    data: {
        labels: ['Q1', 'Q2', 'Q3', 'Q4'],
        datasets: [
            { label: '2025', data: [1.2, 1.4, 1.5, 1.8], backgroundColor: '#2D5F8A' },
            { label: '2026', data: [1.9, 2.1, 2.3, 2.6], backgroundColor: '#E8734A' }
        ]
    },
    options: { responsive: true, animation: false }
});
</script>
```

## Important Rules

1. **Always set `animation: false`** — Playwright captures the page after load. Animations may result in incomplete charts.
2. **Wrap charts in `class="no-break"`** to prevent page breaks mid-chart.
3. **Use design system colors** from oasis-design-system for consistency.
4. **Set explicit dimensions** on the container `<div>` — don't rely on responsive sizing for print.
5. **Unique IDs** — each `<canvas>` needs a unique `id`.
