---
status: draft
---

# Authoring Product — Agent template for social/content authoring

A **product** in the sense of `specs/4/products.md`: a curated bundle of
skills, SOUL.md, HELLO.md, and system prompt that turns a stock arizuko
agent into an author. Pure configuration — no new daemon, no schema
changes, no bespoke code paths. Publishing safety comes from the
separate HITL firewall spec (`specs/6/4-hitl-firewall.md`).

## Problem

We want users to spin up an "author" agent for a group that can draft
posts, read prior content, propose publishing, and iterate based on
feedback. Today an agent is a blank container with generic skills —
there's no opinionated starting point for content work. The evangelist
spec (`specs/5/6-evangelist.md`) hinted at this but baked it into a
Reddit-specific flow. We want a generic authoring persona usable across
any outbound-capable platform.

## Design

**A product = a directory of files seeded into the group on creation.**
When a user runs `arizuko group add <name> --product author`, the
create flow copies a product template into the group folder:

```
template/products/author/
├── SOUL.md          — persona, voice, authorial values
├── HELLO.md         — greeting + onboarding prompt for the user
├── SYSTEM.md        — optional, additive system instructions
├── CLAUDE.md        — per-group wisdom layered on top of ant/CLAUDE.md
└── skills/          — authoring-specific skill files
    ├── draft/
    ├── publish/
    ├── research/    — reuses existing if present
    ├── web/         — reuses existing if present
    └── content-audit/
```

No Go code for the product itself. The create command just copies files.
The agent discovers the new skills via the normal skill loader.

### Persona and voice

`SOUL.md` sets the authorial voice — dense, narrative, product-focused,
no marketing fluff (same rules as the `tweet` skill in `~/.claude`). The
SOUL defines:

- Core identity (who is this author, what domain)
- Tone markers (concise, technical, ironic, etc.)
- What the author refuses to write (banned phrases, generic platitudes)
- Preferred formats (thread / essay / short / long)
- Signature patterns (how they open, close, transition)

The user edits `SOUL.md` after creation to tune voice to their brand.

### Skills bundle

**`draft/`** — how to turn a topic into a piece of content. Steps:
research → outline → draft → self-critique → rewrite. Uses existing
`research/` and `web/` skills for sourcing. Outputs drafts to the
group's working area (e.g. `~/drafts/<slug>.md`).

**`publish/`** — how to submit a draft for publication. Calls the
`publish` MCP tool (from the HITL firewall spec, spec 4). Handles
platform selection, account selection, media attachment, scheduling.
The skill knows that publication is gated — it sets expectations with
the agent that every call goes through review, not instant.

**`content-audit/`** — how to review prior content in the group's
`/pub/` area, identify gaps, propose new topics. The agent uses this on
a recurring basis (triggered by a `timed` cron) to generate its own
work queue.

**`research/`**, **`web/`** — reused from the stock skill set. The
author product doesn't reimplement; it just declares them as
requirements.

### System prompt additions

`SYSTEM.md` in the product layers additional instructions onto the
base `ant/CLAUDE.md`:

- "You are an author. Your job is to produce publishable content,
  not to chat."
- "Every publish call goes through human review. Write for the
  reviewer's approval, not for the immediate send."
- "Track your drafts in `~/drafts/`. When a draft is published, move
  it to `~/published/` with the resolved URL."
- "Before proposing a new post, check `/pub/` in the group's web dir
  and the last 10 entries in `~/published/` to avoid repetition."

These layer additively — they don't replace the stock agent wisdom.

### Workflow (happy path)

1. Operator: `arizuko group add REDACTED-author --product author`
2. The create command copies `template/products/author/` into the new
   group folder.
3. Operator edits `SOUL.md` to set the voice for this specific author.
4. Operator routes a JID (e.g. a Telegram DM with themselves, or an
   internal chat) to the group.
5. Operator messages "write a thread about the golang runtime I've been
   researching".
6. Agent invokes `draft` skill: searches prior content, drafts, refines,
   presents for review in the chat.
7. Operator says "ship it to bluesky".
8. Agent invokes `publish` MCP tool with platform=bluesky, body=draft.
9. The HITL firewall (spec 4) holds the call. Operator sees it in
   `/dash/review`.
10. Operator approves (or edits body and approves).
11. The dispatcher executes the publish against the bsky adapter.
12. Agent sees the resolved result (published URL), moves draft to
    `~/published/`, reports back in the chat.

### Per-group content area

An author product group has a designated `content_target` — a path
under `/srv/data/arizuko_<instance>/web/pub/` where the agent's output
is also mirrored as HTML. Drafts → preview pages; published → permanent
pages. The agent's `web/` skill handles the Markdown → HTML rendering.

This gives each author group its own public archive without extra
infrastructure. The vite dev server already serves `/pub/`.

Whether this is a product-level setting (one target per author) or a
platform-level setting (one target per platform account) is an open
question.

### Relation to existing primitives

- **`specs/4/products.md`** — this spec is one instance of that
  framework. Products are SOUL + skills + HELLO bundles; author is
  just one of the 8 sketched there.
- **`specs/6/4-hitl-firewall.md`** — publishing safety. The author
  product depends on the firewall for the review gate.
- **`specs/2/j-social-actions.md`** — defines the `publish` verb
  semantics. The author product's `publish` skill is the user-facing
  wrapper around that MCP tool.
- **`ant/skills/`** — author skills live alongside stock skills.
  Products layer on top.
- **`template/services/`** — a future product may bundle service
  configs too, but author is content-only and doesn't need any.

## Minimum viable cut

**Scope for v1:**

- Directory `template/products/author/` with SOUL, HELLO, SYSTEM,
  CLAUDE, and three skills (draft, publish, content-audit).
- `arizuko group add ... --product <name>` flag on the create command.
- Copy logic in the create flow, nothing more.
- Depends on `specs/6/4-hitl-firewall.md` being shipped (even a stub
  version that always holds).
- Depends on at least one adapter exposing a publish capability
  (bsky or mastodon first).

**Out of scope for v1:**

- Automatic content→HTML pipeline (manual for now).
- Per-account content targets (one target per group).
- Multi-author groups (one author per group).
- Cross-product composition (a group is one product).

## Open questions (next phase)

1. **Product catalog vs per-instance.** Should `template/products/`
   ship a fixed catalog (author, researcher, developer, ops...) or
   should operators be able to define their own products under a
   per-instance `products/` dir? Probably both — ship some, allow
   override. Naming collision rules TBD.

2. **SOUL.md editing after creation.** If an operator edits SOUL.md
   for a live group, the running agent won't see it until next
   container spawn. Do we need a "reload persona" signal? Or is
   "restart the agent" sufficient?

3. **Platform account binding.** Where does "this author group posts
   as @foo on bluesky" live? On the group row? In a per-group config
   file? In a new `platform_accounts` table? The HITL firewall spec
   doesn't answer this — the authoring product needs it.

4. **Draft storage location.** `~/drafts/` is per-agent, not shared.
   If a human wants to edit a draft in dashd before the agent
   finishes, they need another place. Options: a new `drafts` table,
   a shared `/drafts/` dir on the group volume, or always route
   through `pending_actions` (draft → submit → review). Last option
   is cleanest but means drafts are review-gated too.

5. **Content gap detection.** `content-audit` needs to look at what's
   already in `/pub/`. Does it read the filesystem? Parse
   frontmatter? Hit an index file the vite build emits? Punt until
   the web layer has more structure.

6. **Review loop speed.** If every single publish requires human
   review, the author agent is effectively a draft generator. For
   trusted authors, maybe the firewall's `hold_if` predicate relaxes
   over time (e.g. first 10 posts require review, after that
   auto-publish). That bleeds back into the HITL firewall spec.

7. **Voice drift.** An author agent running for months on a group
   may drift from its SOUL.md voice as conversation history grows.
   Do we need a periodic "recalibrate against SOUL.md" check in the
   draft skill? Deferred.

8. **Cross-agent collaboration.** Can an author delegate to a
   researcher sub-agent for sourcing? Uses the existing delegate
   pattern. Works out of the box if the skills are set up right;
   probably no new spec work needed.

9. **Revenue / analytics loop.** Is there a feedback signal from
   published content (impressions, engagement) back to the author so
   it can learn what works? Entirely out of scope for v1 but worth
   flagging — the schema might want a `published.metrics JSON`
   column from day one so we don't need a migration later.

10. **First product to ship.** Author is the one we're talking about.
    Is it the right first product, or should we do "researcher"
    first (read-only, no publishing, lower risk, still valuable)?
    Tentatively: author, because it exercises the whole HITL stack
    and is the motivator for building the firewall.

## Out of scope

- Multi-tenant authoring (shared drafts across groups).
- Collaborative editing (two humans working on the same draft in
  dashd). The reviewer edit-before-approve from spec 4 is enough.
- Auto-generated media (image/audio). The author writes text; media
  attachment is manual for v1.
- A11y / i18n of the rendered HTML. Whatever the vite scaffold does
  is what we get.
