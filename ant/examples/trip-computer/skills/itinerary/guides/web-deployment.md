# Web Deployment Guide

Copy `components/hub-lavender.css` and `components/hub.js` from the installed itinerary skill into the trip's `assets/` directory before publishing. A page links every local file relative to its own directory: `assets/hub-lavender.css` from `<trip>/`, `../assets/hub-lavender.css` from `<trip>/<dir>/`. NEVER a root-relative `/pub/...` path — ALWAYS the relative one.

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

Run `trip_lint.py` after EVERY change (`checks.md`). It reads
these classes: `timeline-date` (`HH:MM — <title>`), `food-list`,
`checked-line`, `source-note`, `info-box`, `map-links`; the stylesheet
defines them.

**Manual Checks:**
- Each page's `<title>` matches its `<h1>`; index cards use "Day N — Weekday, Mon D"
- Index card descriptions match day page content
- Map links follow `mapy-links.md` (no `?q=`)
- Timeline timing realistic (no overlaps)
- Cross-references valid (Plan A ↔ Plan B)
- Transport info consistent across mentions

## Design Rules

**Styling:**
- ALWAYS use `assets/hub-lavender.css` + `assets/hub.js` (the site can add local styles)
- ALWAYS use the stylesheet's classes and colour tokens; keep its 2px radii and 760px column.
- NEVER add a page theme or vertical accent borders; use the existing box classes.
- NEVER inline styles except minimal layout (nav flex, spacing).
- Clean titles (NO unnecessary suffixes)

## Links

`hub-lavender.css` appends a "mapy", "google" or "osm" badge to every map
link, so the link text only names the place or route (`mapy-links.md`).
Every link in `.map-links`, `.food-list` and `<nav>`, every `a.alt` chip
and the back link is a 40 px tap target. References inside a Story box
and the `.source-note` stay inline text links; they are the one
exception. NEVER restyle these per page — ALWAYS change the one
stylesheet.

## HTML Template

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Page Title</title>
  <link rel="stylesheet" href="assets/hub-lavender.css">
</head>
<body>
  <a href="index.html" class="back-btn">&larr; overview</a>
  <button class="theme-toggle" aria-label="Toggle theme"></button>

  <nav style="max-width: 760px; margin: 1.5em auto 0; padding: 0 1.5em; display: flex; gap: 0.5em; flex-wrap: wrap; font-size: 0.9em;">
    <a href="day1.html" style="color: var(--dim); text-decoration: none;">Day 1</a>
    <span style="color: var(--border);">•</span>
    <a href="day2.html" style="color: var(--dim); text-decoration: none;">Day 2</a>
    <span style="color: var(--border);">•</span>
    <strong style="color: var(--accent);">Day N</strong> <!-- current -->
  </nav>

  <div class="hub-container">
    <h1>Page Title</h1>
    <!-- Content -->
  </div>

  <script src="assets/hub.js"></script>
</body>
</html>
```

## Day Navigation

- Minimal inline style: flexbox, gap, color vars only
- Current day: `<strong style="color: var(--accent);">Day N</strong>`
- Other days: links with `color: var(--dim)`
- Separator: `<span style="color: var(--border);">•</span>`
- Keep compact, single line, wraps on mobile
