# Web Deployment Guide

## File Structure

```
~/public_html/<destination>/
├── index.html          # Overview with day cards
├── day1.html           # Day itinerary
├── day2.html
├── ...
├── sites.html          # Landmarks guide
├── cafes.html          # Cafe database
└── trips.html          # Day trip fragments
```

## Consistency Checks

Run after EVERY change:

**Title = H1**
```bash
cd ~/public_html/<dest>
for f in *.html; do
  title=$(grep -m1 '<title>' "$f" | sed 's/.*<title>\(.*\)<\/title>.*/\1/')
  h1=$(grep -m1 '<h1>' "$f" | sed 's/.*<h1>\(.*\)<\/h1>.*/\1/')
  if [ "$title" != "$h1" ]; then
    echo "❌ $f: title='$title' vs h1='$h1'"
  fi
done
```

**Manual Checks:**
- Index card headers match page titles exactly
- Index card descriptions match day page content
- Map links have coordinates (`&x=&y=&z=` mandatory)
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
