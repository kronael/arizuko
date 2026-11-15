# Pages

The page contract every page on the site follows. `NS` is the namespace
in `~/public_html/CLAUDE.md`.

## Skeleton

Copy `~/.claude/skills/knowledge-site/components/page.html`. In order:

1. `<meta charset="utf-8">`, then the `NS:` metas (§ Metadata), then the
   viewport meta.
2. `<title>`: the `<h1>` text, optionally followed by ` — ` and the
   section or site name. Never empty.
3. `<link rel="stylesheet" href="../assets/hub.css">` with one `../` per
   directory level below `~/public_html/` (none on the root page).
4. Right after `<body>`: `<a href="../" class="back-btn">&larr; back</a>`
   pointing at the parent hub (omitted on the root page), then
   `<button class="theme-toggle" aria-label="Toggle theme"></button>`.
5. `<div class="hub-container">` holding, in order: one `<h1>`, a
   `<p class="hub-subtitle">` line, a `.tldr-box` with the answer, a
   `.hub-nav` when the page has three or more sections, the
   `<section id="...">` blocks, and a `<footer class="hub-footer">` with
   the sources and related pages.
6. `<script src="../assets/hub.js"></script>` last, at the same depth as
   the stylesheet.

A page is a directory with an `index.html` (`section/topic/index.html`,
URL `section/topic/`) or one file (`section/topic.html`); stay with the
form the section already uses.

## Metadata

In `<head>`, right after `<meta charset>`:

```html
<meta name="NS:type" content="research">
<meta name="NS:date" content="2026-01-31">
```

- `NS:type` is one of the types listed in `~/public_html/CLAUDE.md`,
  lowercase letters and hyphens.
- `NS:date` is the date the content was written or last changed, as
  `YYYY-MM-DD`. ALWAYS set it by hand when the content changes; NEVER copy
  a file's mtime and NEVER bump it for a markup-only fix.
- `<meta name="NS:authored" content="llm">` marks an LLM-assembled page
  of any other type. `hub.js` inserts one "LLM-authored" banner under
  `.hub-subtitle` on every page typed `research` or marked
  `NS:authored`. A page the user wrote carries neither.

## Components

The stylesheet is the site's `assets/hub.css`. The common components:

| need | class |
| --- | --- |
| page column | `.hub-container` |
| one-line scope under the title | `.hub-subtitle` |
| the answer first | `.tldr-box` with `.tldr-label` and `.tldr-text` |
| in-page navigation | `.hub-nav` > `.hub-nav-inner` > `a.nav-item` with `.nav-label`, `.nav-hint` |
| links to other pages as tiles | `.jump-box` > `.jump-grid` > `a.jump-link` |
| an item in a list of pages | `.card` with `.card-title`, `.card-meta` |
| one key point | `.insight` with `.insight-label`, or `.insights-box` |
| a warning or aside | `.callout`, `.caveat` |
| side-by-side comparison | `.vs-grid` > `.vs-item` with `.vs-title` |
| ordered steps | `.path-box` > `.path-step` |
| dates in order | `.timeline` > `.timeline-item` with `.timeline-date`, `.timeline-text` |
| terms | `.glossary` > `.glossary-grid` > `.glossary-item` |
| wide table | `.table-scroll` around a plain `<table>` |
| source line, check date | `.source-note` |
| section heading on a hub page | `h2.section-label` (`guides/organize.md` § Section labels) |
| page footer | `.hub-footer` |

Plain `<table>`, `<blockquote>`, `<code>` and `<pre>` need no class.
ALWAYS read `assets/hub.css` for a component not listed here before
writing new CSS.

- A page `<style>` block holds layout only: grid, flex, spacing,
  `@media`. NEVER a colour, a `:root` variable or a new component —
  ALWAYS add a component the site needs to `assets/hub.css`, once.
- An interactive page may load Tailwind and Alpine from their CDNs (core
  `web` skill); the linter skips its classes. It still carries the
  metadata, a title and an `<h1>`.

## Links

- Inside the site, ALWAYS link by a path relative to the page
  (`../guides/`, `topic.html#setup`). Into another published tree,
  ALWAYS use the root-relative `/pub/<folder>/...`; NEVER a root path
  without `/pub/<folder>/`, which resolves to nothing.
- Link text in prose sits in square brackets and names what the click
  gives: `<a href="../guides/setup/">[Setup guide]</a>`. The full title
  belongs in the target page.
- A link styled as a component (`.nav-item`, `.jump-link`, `.card`,
  `.back-btn`) keeps its class and its own text.
- `hub.js` opens every external link in a new tab and appends one ↗.
  NEVER type an arrow into link text.
- Link styling lives in `assets/hub.css`. NEVER restyle a link on one
  page — ALWAYS change the one rule in `hub.css`.
- A `#fragment` link needs a matching `id` on the target page; the
  linter checks it.

## Images and files

An image or download lives beside its page or in `assets/` and is linked
relatively. NEVER hotlink a file the page depends on from a host that can
remove it — ALWAYS save a copy the license allows, or link out.
