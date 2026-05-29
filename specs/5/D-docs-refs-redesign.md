---
status: draft
depends: []
---

# specs/5/D — docs IA redesign (Divio categories + dbt reference rhythm)

## Why

`/pub/arizuko/` grew organically: each page invented its own chrome
(inline `<style>` blocks, ad-hoc breadcrumbs, mixed TOCs, no consistent
foot). The reference set has no shared page rhythm — a reader can't tell
where they are or what shape a page will take. And `concepts/` and
`reference/` both carry `grants`, `jid`, `tokens`, `topics`: the same
nouns explained twice with no rule for which page owns what.

This spec fixes the _information architecture and content rhythm_ only.
It does not touch the visual identity — see the guardrail below. We
adopt the Divio four-category split (already cited in root `CLAUDE.md`)
and dbt's reference-page rhythm; we adopt none of dbt's look.

## Guardrail (visual identity is fixed)

The hub.css palette, 2px corners, dense typography, and arizuko color
twists do not move. This spec changes _where pages live_ and _what
shape their content takes_, never the look. Any palette / corner /
font-stack variable in `hub.css` that shifts is a bug. Canonical rule:
root `CLAUDE.md` "Updating the web docs"; `template/web/CLAUDE.md`
"Style rules".

## Target IA (Divio + two arizuko extensions)

Divio's four categories map to reader posture: learn, do, look up,
understand. arizuko adds two sections the four-category model doesn't
cover — operator-deployable units (`products/`) and the daemon catalogue
(`components/`). These already exist; this spec keeps them and assigns
each its category role.

```
pub/
  index.html              landing — what arizuko is, quick start
  concepts/               EXPLANATION — mental model, cross-cutting ideas
  howto/                  HOW-TO — task recipes ("do this")
  reference/              REFERENCE — exhaustive CLI/MCP/env/schema surface
  products/<name>/        product intro + setup (operator-deployable units)
  components/<daemon>.html daemon catalogue (one page per daemon)
  security/               SECURITY — threat-model landing
  changelog/index.html
  assets/{hub.css,hub.js}
```

Ownership rule (resolves the concepts↔reference duplication):

- **concepts/** owns the _narrative_: what a thing is, why it exists,
  how primitives relate. No exhaustive field lists.
- **reference/** owns the _exhaustive surface_: every flag, var, tool,
  column, the grant DSL grammar, the JID grammar.
- A noun documented in both (grants, jid, tokens, topics) keeps its
  concepts page as narrative and its reference page as grammar/field
  list, each linking the other. Not duplicated — split by category.
- **howto/** is task-shaped ("Add a Slack adapter", "Run a migration").
- **components/** and **products/** retain the page contracts in
  `template/web/CLAUDE.md` ("Components", "Products"); this spec only
  applies the shared chrome to them in phase 2.

## Page anatomy (visual elements)

One shell, every category. Concrete three-pane layout at ≥1200px:

```
┌──────────────────────────────────────────────────────────────────┐
│ arizuko                                     [◑ theme]  [ask agent] │  topbar
├──────────────┬───────────────────────────────────┬───────────────┤
│ NAV (left)   │ arizuko › concepts › routing       │ ON THIS PAGE  │
│              │                                    │ (right TOC)   │
│ Concepts     │ # Routing                          │ · Route table │
│ › what is…   │ one-sentence lede.                 │ · Topics      │
│ › ant        │                                    │ · Sticky      │
│ ▸ routing    │ ## Route table                     │               │
│   engagement │ prose + one worked example…        │               │
│   topics     │ ## Topics …                        │               │
│ Reference    │ ─────────────────────────────────  │               │
│ How-to  …    │ ← engagement   ·   topics →         │  pager        │
│              │ edit this page · updated 2026-05    │  footer       │
└──────────────┴───────────────────────────────────┴───────────────┘
```

Below 1200px: single column; the left nav collapses to a drawer toggled
from the topbar; the right TOC drops.

Concrete elements — all in `hub.css` / `hub.js`, no framework, no build:

| Element          | Class / fn             | Behavior                                                                                                    |
| ---------------- | ---------------------- | ----------------------------------------------------------------------------------------------------------- |
| Topbar           | existing hub.css       | brand left; theme toggle; **ask-agent** button (`injectAskAgent`) — conversational discovery, no search box |
| Breadcrumb       | `.docs-crumb`          | `arizuko › <category> › <page>`, directly above H1                                                          |
| Left nav         | `.docs-nav`            | category tree; concepts in **curriculum order**, others alphabetical; `aria-current` via `markCurrentNav()` |
| Content          | `.docs-content`        | breadcrumb → H1 → lede → body; the reference or concepts rhythm lives here                                  |
| Right TOC        | `.docs-toc`            | `buildTOC()` walks `.docs-content h2,h3` into an "On this page" rail                                        |
| Pager            | `.docs-pager`          | `← prev · next →`; keyed to nav order (reference) or curriculum order (concepts)                            |
| Footer           | `.docs-footer`         | edit-this-page link + `<time>` last-updated stamp                                                           |
| Selection helper | `injectSelectionPopup` | select text → "ask the agent about this"                                                                    |

Visual identity is **arizuko's and fixed** (§ guardrail): hub.css palette,
**2px corners** (never rounded), dense typography, arizuko color twists.
The shell borrows the dbt/Stripe three-pane _structure_, never the look.

## Adopt from dbt (IA + content rhythm)

1. Breadcrumb above H1 on every page: `arizuko › reference › CLI commands`.
2. **Reference-page rhythm** (the repeatable template — see next section).
3. One captioned code block per shape; multi-shape commands ship as
   sequential blocks, never a tab widget.
4. Type / default / required folded into Definition prose, not a fielded
   table at the top. Tables earn their place only for cross-item
   comparison (a command's flag set, a table's columns).
5. Low heading density — H2 count 3–6 per page, H3 only where named,
   no H4.
6. Previous/Next pager at page foot, keyed off left-nav order.
7. Inline version-difference statements ("Available since v0.45.4"),
   never version banners.

## Do NOT adopt from dbt

- Docusaurus/React, its fonts, colors, rounded corners (guardrail).
- Version-selector dropdown — one current site; footer version stamp
  (`hub.js:injectFooter`) already answers "which build is this?".
- Tabbed code-block widget — adds JS and a divergence surface.
- Reading-time / last-viewed / feedback-widget chrome.
- Hosted lexical search (Algolia/Meilisearch) or in-page search.
  Discovery is conversational: `hub.js` already ships
  `injectAskAgent()` + `injectSelectionPopup()` pointing at the krons
  agent. No work here; just don't add a search box.

## Reference-page rhythm (the template)

Every reference page follows this skeleton. Voice per
`template/web/CLAUDE.md`.

```
breadcrumb     arizuko › reference › <thing>
H1             bare name for a leaf; "About <thing>" for a catalogue
lede           one definitional sentence
H2 Definition  type/default/required folded into prose
H2 Usage       recommended pattern (or "Recommendation")
H2 Examples    captioned code blocks (caption = filename or language)
H2 Troubleshooting  optional; H3 per named failure (symptom → fix)
pager          ← prev · next →
foot           edit-this-page link · last-updated stamp
```

Two page kinds share the rhythm:

- **Catalogue** (cli, env, mcp, schema, openapi) — many items. Top-level
  H2s group by concern; each item is an H3 under its group. Per-item
  tables (a command's flags, a table's columns, an MCP tier badge) stay
  — those are the cross-item comparisons that earn a table.
- **Leaf** (grants, jid, tokens, topics, stats) — one subject. Follow
  Definition → Usage → Examples → Troubleshooting literally.

Chrome lives in `hub.css` (new classes `.docs-layout`, `.docs-nav`,
`.docs-toc`, `.docs-pager`, `.docs-footer`) and `hub.js` (one
`buildTOC()` walking `.docs-content h2,h3` into the right rail, one
`markCurrentNav()` adding `aria-current` by `location.pathname`).
No framework, no build step. Three-pane at ≥1200px; single column with
a nav drawer below. Per-page inline `<style>` blocks are deleted into
`hub.css`.

The left-nav tree is inlined per page (renders before script). When a
reference page is added, update the tree in sibling files in the same
commit — drift is the price of static nav, acceptable at this page count.

## Concepts walkthrough (Go-Tour rhythm)

`concepts/` is the Explanation category and reads as a **guided tour**,
not a reference set — the Go Tour pattern over the dbt frame. Chrome is
unchanged (three-pane, pager, foot); sequence, size, and tone change.

- **One concept per page, 1–2 min.** A page is a single idea with one
  worked arizuko example (a real route rule, a chat snippet), never an
  exhaustive field list — that's the `reference/` twin's job (§ ownership
  rule). Trim pages that sprawl.
- **Linear curriculum, not alphabetical.** `concepts/index.html` is the
  tour TOC: an ordered "start here → next" path that builds up. Arc:
  1. `index` — what arizuko is, how to take the tour
  2. `ant` — the agent you talk to
  3. `routing` — how an inbound message reaches an agent
  4. `engagement` — staying in the conversation after a mention
  5. `topics` — scoping work into one conversation
  6. `onboarding` + `autoviv` — how groups and agents come to exist
  7. `personas` — giving an agent its voice
  8. `grants` + `scopes` — what an agent or user may do
  9. `auth` + `tokens` — proving the identity behind those grants
  10. `secrets` — folder- and user-scoped credentials
  11. `skills` — extending what an agent can do
  12. `tasks` — scheduled and autonomous work
  13. `web-native-agents` + `webdav` — the web surfaces
  14. `voice`, `slack-pane`, `jid` — surface/addressing specifics (tail)

- **Incremental.** Each page opens by connecting to the prior step ("now
  that routing delivers a message, engagement decides whether to keep
  listening…"); the pager (§ reference rhythm) is keyed to this
  curriculum order, not nav order — the pager **is** the tour's
  next/prev.
- **Mentor tone** (Effective Go): principle first, then the mechanism,
  then a concrete example; second person; explain _why_, not just
  _what_. Voice per `template/web/CLAUDE.md`.

The only new artifact is the ordered `concepts/index.html` TOC; the rest
is the existing pages, re-ordered, sized, and connected. No interactive
playground, no separate `/tour/` tree — it's `concepts/` with a curriculum.

## Migration path

Current `template/web/pub/` has all six target sections populated plus
two legacy top-level dirs to fold. The work is chrome + content + those
two folds + two ownership fixes — no broad tree move.

**Retire / dedup:** for grants, jid, tokens, topics — keep both the
concepts and reference page, but enforce the ownership rule: trim the
concepts page to narrative, trim the reference page to grammar/fields,
cross-link. (Today they overlap.)

**Move (legacy top-level dirs, disposition per `template/web/CLAUDE.md`):**

- `crackbox/` → `components/crackbox.html` (egress-sandbox component;
  redirect the old path). Already the canonical plan in
  `template/web/CLAUDE.md` ("planned move from crackbox/"; "`crackbox/`
  … → redirect to `components/`").
- `examples/chat-sdk.html` → `howto/` as an SDK-embed recipe. Divio has
  no Examples category; the dropped 5th section folds into HOW-TO.
  Redirect the old path.

Otherwise no move — the six Divio + extension sections are already in
place.

**New chrome on existing pages (phase 1, reference/ — 11 pages):**

| Page           | Kind      | Work                                                        |
| -------------- | --------- | ----------------------------------------------------------- |
| `index.html`   | landing   | chrome only; card grid stays                                |
| `cli.html`     | catalogue | chrome + reflow; per-command flag tables stay               |
| `env.html`     | catalogue | chrome + reflow (largest); per-var source link stays inline |
| `mcp.html`     | catalogue | chrome + reflow; tier badge stays as inline tag             |
| `schema.html`  | catalogue | chrome + reflow; per-table column tables stay               |
| `openapi.html` | catalogue | chrome only; generated-doc index                            |
| `grants.html`  | leaf      | chrome + reflow to Definition/Usage/Examples/Troubleshoot   |
| `jid.html`     | leaf      | chrome + unnumber H2s; per-platform table stays             |
| `tokens.html`  | leaf      | chrome + reflow                                             |
| `topics.html`  | leaf      | chrome + reflow                                             |
| `stats.html`   | leaf      | chrome only; data dump, two tables stay                     |

Order: `index` first (sets every page's prev/next), then leaves
(validate the template on small pages), then catalogues (cli, schema,
mcp, env). Two passes per page kind:

1. **Chrome.** Add the CSS classes + `buildTOC()` + `markCurrentNav()`;
   wrap each body in `.docs-layout`; delete inline `<style>`; normalize
   breadcrumb; add pager + edit-this-page + last-updated. No prose
   rewriting. Independently shippable.
2. **Content.** Reflow to the Definition/Usage/Examples/Troubleshooting
   rhythm; fold type/default/required into prose; cull headings to
   density. Per-page, parallelizable.

**Phase 2 (concepts/, howto/, products/, components/, security/):**
inherit the same chrome (breadcrumb, three-pane, pager, foot). Body
rhythm differs by category and follows the page contracts already in
`template/web/CLAUDE.md`. Not started until phase-1 chrome ships.

## Last-updated stamp

A pre-commit hook writes the page's git author-date into a `<time>` in
`.docs-footer` on edit (local Python entry in `.pre-commit-config.yaml`,
no build step). Until the hook lands, hand-stamp from `git log -1`.

## Acceptance (phase 1)

- All 11 reference pages on the new template; three-pane ≥1200px,
  single-column below with working nav drawer.
- No palette / corner / font-stack variable in `hub.css` shifts.
- Inline `<style>` blocks gone; chrome lives in `hub.css`.
- `buildTOC()` + `markCurrentNav()` in `hub.js`; pager + edit-this-page
  - last-updated on every reference page.
- `template/web/CLAUDE.md` "Style rules" updated to require the
  reference-page rhythm for new `reference/` pages, AND carries the
  Maintenance trigger → page map + the same-commit rule (§ Maintenance).
- Deployed to krons via the `template/web/CLAUDE.md` workflow; every
  page returns 200, no console errors.
- Spot-check three pages (one leaf, one catalogue, index) against the
  skeleton.

## Maintenance (standing task for `template/web/CLAUDE.md`)

Docs drift the moment a change ships without its page. The fix is a
standing rule, written into `template/web/CLAUDE.md` (the web-docs guide,
not root CLAUDE.md): **when you change a surface, update its page in the
same commit.** The trigger → page map:

| You changed…                    | Update…                                                                                                                       |
| ------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| a CLI command / flag            | `reference/cli.html`                                                                                                          |
| an env var                      | `reference/env.html`                                                                                                          |
| an MCP tool (name, args, tier)  | `reference/mcp.html`                                                                                                          |
| a DB schema migration           | `reference/schema.html`                                                                                                       |
| a grant scope or the grant DSL  | `reference/grants.html` + `concepts/grants.html` + `concepts/scopes.html`                                                     |
| the JID / token / topic grammar | the matching `reference/*` + its `concepts/*` twin                                                                            |
| a new daemon                    | `components/<daemon>.html` + `components/index.html` + its `reference/openapi.html` row                                       |
| a new channel adapter           | `components/<adapter>.html` (+ a `howto/` recipe if setup is non-trivial)                                                     |
| a **new concept / primitive**   | a `concepts/<x>.html` page **and** insert it into the curriculum order in `concepts/index.html` (fix the neighbouring pagers) |
| a tagged release                | `changelog/index.html`                                                                                                        |

Discipline (also for `template/web/CLAUDE.md`):

- Same-commit rule — docs are part of the change, not a later chore.
- **Concepts curriculum is ordered state**: adding/removing a concept
  means re-stitching `concepts/index.html` order + the prev/next pagers
  of its neighbours (§ Concepts walkthrough). Don't append to the end by
  default — place it where it belongs in the learning arc.
- Static left-nav drift: a new `reference/` page updates the inlined nav
  tree in its sibling pages, same commit.
- Verify-before-announce: every touched `/pub/*` URL returns 200 before
  you call it done; sync to krons per the existing workflow.

This map is the concrete deliverable that lands in `template/web/CLAUDE.md`
when phase 1 ships — see Acceptance.

## Open questions

None blocking. Version-implicit (no per-page version line) and
agent-as-search are decided above.
