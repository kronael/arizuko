# Organize the site

How the pages hang together: hub pages, section labels, moves, retiring a
page and the archive. Page markup is `guides/pages.md`.

## Layout

```text
~/public_html/
├── CLAUDE.md            # NS, types, site title, section rules
├── index.html           # root hub: hand-picked cards per section + archive link
├── assets/hub.css, assets/hub.js
├── archive/index.html   # written by catalogue.py, never by hand
└── <section>/
    ├── index.html       # section hub: every page of the section
    └── <topic>/index.html
```

One directory per section. A section with a `CLAUDE.md` of its own adds
rules for its pages (a research method, a page template); it never
loosens the site's rules.

## Hub pages

- The root hub shows a hand-picked set of cards per section and links
  every section hub and the archive. ALWAYS ask the user before adding a
  card to the root hub or removing one.
- A section hub lists every live page of its section as `.card` or
  `.jump-link` entries, newest or most useful first, with a one-line
  `.card-meta` each.
- A hub page, root or section, carries `NS:type` `index` and the date of
  its last change. `index` is the last type in the site's list, so the
  archive lists the hubs after the content. `catalogue.py` stamps the
  archive page itself `index` and never lists it.
- Every new page is linked from its section hub in the same change.
  NEVER publish a page that no hub links — ALWAYS add the hub entry
  first.

## Section labels

A category heading on a hub page uses the one rule in `hub.css`:

- on the root hub:
  `<div class="section-header"><div class="section-nav"><a href="guides/">Guides</a></div></div>`;
- on any other hub page: `<h2 class="section-label">Guides</h2>`.

NEVER restyle a section label on one page — ALWAYS change `hub.css`.

## Move a page

A reader may hold the old URL, so the old path always answers.

1. Move the file or directory to the new path.
2. Fix the moved page's depth-relative paths: `assets/` links, the back
   button, links to its neighbours.
3. Write a redirect stub at the old path:

   ```html
   <!doctype html>
   <meta charset="utf-8">
   <title>Moved</title>
   <meta http-equiv="refresh" content="0; url=../new/topic/">
   <link rel="canonical" href="../new/topic/">
   <p>Moved to <a href="../new/topic/">../new/topic/</a>.</p>
   ```

   The refresh target, the canonical link and the body link are the same
   path, relative to the stub. A stub carries no `NS:` metas; the
   linter and the catalogue skip it.
4. Update every link to the old path:
   `grep -rl 'old/topic' ~/public_html --include='*.html'`, then edit
   each hit to the new path. The stub catches only readers from outside.
5. Regenerate the archive and run `site_lint.py` to exit 0; the
   `redirect-stub` rule checks that the target exists.

NEVER point a stub at another stub — ALWAYS update the older stub to
the final target when a page moves twice.

## Retire a page

- Unlink it from its section hub and the root hub. The page stays where
  it is with its metadata, and the archive keeps listing it.
- When the content is replaced, add a `.callout` at the top of the old
  page linking the replacement, and update `NS:date`.
- NEVER delete a page on your own judgment — ALWAYS ask the user. When
  the user asks for a deletion, remove every link to the page in the
  same change and run the linter.

## Archive

`~/.claude/skills/knowledge-site/scripts/catalogue.py` writes
`archive/index.html`: every page with an `NS:type`, grouped in the
`--types` order from the site's `CLAUDE.md`, newest `NS:date` first,
titles from `<title>`. Regenerate it after a page is added, moved,
retyped or redated. NEVER edit `archive/index.html` by hand — ALWAYS
fix the page's metadata and rerun the command (the skill's `SKILL.md` § Archive).
