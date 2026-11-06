# Web Deployment Guide

## File Structure

```
~/public_html/<destination>/
├── index.html          # Overview with day cards
├── day1.html           # Day itinerary
├── day2.html
├── ...
└── places.html         # The census, published (census.md)
```

## Consistency Checks

Run `scripts/page_check.py` after EVERY change (`checks.md`). It reads
these classes: `timeline-date` (`HH:MM — <title>`), `food-list`,
`checked-line`, `source-note`, `info-box`, `map-links`; the stylesheet
defines them.

**Manual Checks:**
- Index card headers match page titles exactly
- Index card descriptions match day page content
- Map links follow `mapy-links.md` (no `?q=`)
- Timeline timing realistic (no overlaps)
- Cross-references valid (Plan A ↔ Plan B)
- Transport info consistent across mentions

## Design Rules

**NO vertical accent borders** — use background color instead:
```css
/* Good */
background: rgba(255, 183, 77, 0.08);
padding: 1em;
border-radius: 8px;

/* Bad */
border-left: 4px solid var(--accent);
```

**Styling:**
- ALWAYS use `/pub/<folder>/<site>/assets/hub-lavender.css` + `/pub/<folder>/<site>/assets/hub.js` (rules: the site's `CLAUDE.md` § Web style)
- NEVER inline styles except minimal layout (nav flex, spacing)
- Clean titles (NO unnecessary suffixes)

## HTML Template

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <title>Page Title</title>
  <link rel="stylesheet" href="/pub/<folder>/<site>/assets/hub-lavender.css">
</head>
<body>
  <a href="index.html" class="back-btn">&larr; overview</a>
  <button class="theme-toggle"></button>

  <nav style="max-width: 900px; margin: 1.5em auto 0; padding: 0 1.5em; display: flex; gap: 0.5em; flex-wrap: wrap; font-size: 0.9em;">
    <a href="day1.html" style="color: var(--dim); text-decoration: none;">Day 1</a>
    <span style="color: var(--border);">•</span>
    <a href="day2.html" style="color: var(--dim); text-decoration: none;">Day 2</a>
    <span style="color: var(--border);">•</span>
    <strong style="color: var(--accent);">Day N</strong> <!-- current -->
  </nav>

  <div class="hub-container">
    <h1>Title</h1>
    <!-- Content -->
  </div>

  <script src="/pub/<folder>/<site>/assets/hub.js"></script>
</body>
</html>
```

## Day Navigation

- Minimal inline style: flexbox, gap, color vars only
- Current day: `<strong style="color: var(--accent);">Day N</strong>`
- Other days: links with `color: var(--dim)`
- Separator: `<span style="color: var(--border);">•</span>`
- Keep compact, single line, wraps on mobile
