---
status: shipped
depends: []
---

# specs/5/D — docs IA redesign (Divio categories + dbt reference rhythm)

## Why

`/pub/arizuko/` grew organically: each page invented its own chrome
(inline `<style>`, ad-hoc breadcrumbs, mixed TOCs), and `concepts/` and
`reference/` both carried `grants`, `jid`, `tokens`, `topics` — the same
nouns explained twice with no rule for which page owns what.

This spec fixes **information architecture and content rhythm only**. We
adopt the Divio four-category split and dbt's reference-page rhythm; we
adopt none of dbt's look.

## Guardrail (visual identity is fixed)

`hub.css` design tokens do not move: palette, the **current** corner radii
(don't invent new ones), dense typography, the circular theme toggle,
arizuko color twists. This spec changes _where pages live_ and _what shape
their content takes_, never the look. Any palette / corner / font-stack
variable that shifts is a bug. `hub.css` is source of truth — root
`CLAUDE.md`'s shorthand px figures are not.

## Target IA (Divio + two arizuko extensions)

Divio's four categories, plus two the model doesn't cover: operator-
deployable units (`products/`) and the daemon catalogue (`components/`).

```
pub/  index.html · concepts/ · howto/ · reference/
      products/<name>/ · components/<daemon>.html · security/ · changelog/
```

**Ownership rule** (this is what resolves the duplication):

- **concepts/** owns the _narrative_ — what a thing is, why it exists, how
  primitives relate. No exhaustive field lists.
- **reference/** owns the _exhaustive surface_ — every flag, var, tool,
  column, the grant DSL grammar, the JID grammar.
- A noun in both keeps its concepts page as narrative and its reference
  page as grammar, each linking the other. Split by category, never
  duplicated.
- **howto/** is task-shaped ("Add a Slack adapter", "Run a migration").

## Two chromes, assigned by content shape (2026-05-30)

Exactly two reused chromes plus a no-nav landing. **Do not invent a
third.**

- **Three-pane** (`.docs-layout`): navigation-heavy _catalogues_ —
  `reference/` and `components/`. Left category nav + content + right
  `.docs-toc` (`buildTOC()`), three-pane at ≥1200px, single column with a
  nav drawer below.
- **Guide** (`.guide-layout`): linear _learning_ sections, Go-Tour rhythm
  — `concepts/`, `howto/`, `products/`. Thin lesson-nav + one readable
  column + a prominent prev/next pager; **no right TOC** (pages are 1–2
  min).
- **One-pager** (no nav): the landing `index.html` and `security/` — a
  pitch, not a doc page.

Chrome lives in `hub.css` + `hub.js` (`injectFooter`, `injectAskAgent`,
`injectSelectionPopup`, `buildTOC`, `markCurrentNav` — see
`template/web/pub/arizuko/assets/hub.js`). No framework, no build step,
no per-page inline `<style>`.

**Two footers, kept distinct**: the global `injectFooter()` carries the
version stamp and links; the per-page `.docs-footer` carries an
edit-this-page link plus a `<time>` last-updated stamp written by a
pre-commit hook from `git log -1 --format=%aI -- <file>`. If git metadata
is unavailable, leave the existing stamp — never blank it.

**Discovery is conversational, not lexical.** No search box, no Algolia:
`injectAskAgent()` / `injectSelectionPopup()` open the krons
`arizuko/support` agent, which has the docs + code in context and links
back into the docs.

## Adopt from dbt (IA + content rhythm)

1. Breadcrumb above H1 on every page: `arizuko › reference › CLI commands`.
2. The reference-page rhythm: `H1 → lede → Definition → Usage → Examples
→ Troubleshooting`, pager, foot. Catalogue pages (cli, env, mcp, schema,
   openapi) group items as H3 under concern H2s; leaf pages (grants, jid,
   tokens, topics, stats) follow the rhythm literally.
3. One captioned code block per shape — never a tab widget.
4. Type / default / required folded into Definition prose, not a fielded
   table. Tables earn their place only for cross-item comparison.
5. Low heading density — 3–6 H2 per page, H3 only where named, no H4.
6. Prev/Next keyed off the section's ordered page list, which **is** the
   left-nav order — one source, two readers.
7. Inline version-difference statements ("Available since v0.45.4"), never
   version banners.

**Do NOT adopt**: Docusaurus/React or dbt's look (guardrail); a
version-selector dropdown (one current site, footer stamp answers it); the
tabbed code-block widget (JS + a divergence surface); reading-time /
feedback-widget chrome; hosted lexical search.

## Concepts is a curriculum, not a set

`concepts/` reads as a **guided tour** (Go Tour over the dbt frame): one
concept per page, 1–2 min, one worked arizuko example, and a **linear
order** — not alphabetical. That order is an explicit ordered list of
pathnames in `concepts/index.html`, and it is the **single source of
truth** for both the concepts nav order AND the pager prev/next. Each page
opens by connecting to the prior step; the tone is mentor (principle →
mechanism → example, second person, explain _why_).

No interactive playground, no separate `/tour/` tree — it is `concepts/`
with a curriculum.

## Maintenance (the standing deliverable)

Docs drift the moment a change ships without its page. The fix is the
same-commit rule plus a trigger → page map. **That map shipped into
`template/web/CLAUDE.md` § Maintenance — it is canonical there, not
here.** Its three non-obvious clauses:

- **The concepts curriculum is ordered state.** Adding or removing a
  concept means re-stitching the order in `concepts/index.html` AND the
  prev/next pagers of its neighbours. Don't append to the end by default —
  place it in the learning arc.
- **Static left-nav drift is accepted, bounded by discipline.** The nav
  tree is inlined per page (it must render before script), so a new
  `reference/` page updates its siblings in the same commit. Acceptable at
  this page count; revisit if the count grows.
- **Verify before announce.** Every touched `/pub/*` URL returns 200
  before it's done.

## Legacy folds

Two top-level dirs folded, each leaving a `<meta http-equiv="refresh">`
stub plus a visible canonical link at the old path (the site is verbatim
static HTML — "redirect" cannot be a server rule):

- `crackbox/` → `components/crackbox.html` (it is a component).
- `examples/chat-sdk.html` → `howto/` (Divio has no Examples category; the
  dropped fifth section folds into HOW-TO).
