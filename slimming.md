# Slimming — void / useless features audit (2026-05-17)

## TL;DR

- **One adapter binary has zero deployments** (`twitd`); five more (`bskyd`,
  `emaid`, `linkd`, `mastd`, `reditd`) are wired in `compose/compose.go` +
  `template/services/*.toml` but absent from krons/sloth/marinade. Move
  them behind a feature-branch or strip; ~5k LOC.
- **`discd/sent_ids.go` is add-never-read** since spec 6/J shipped — the
  ring's `.has()` has zero non-test callers. Confirmed dead. ~70 LOC + test.
- **Two registered MCP tools** (`get_history`, `pane_set_prompts`/`pane_set_title`)
  have zero usage in 7d of krons transcripts; `get_history` is self-documented
  DEPRECATED.
- **One stale web-deploy `.bak`** sitting in `/srv/data/arizuko_krons/web/pub/`.
- **`ttsd` daemon directory exists** but no instance runs it and it has no
  service template — orphaned scaffold.

## By category

### Dead env vars

- `WHAPD_URL` — read at `dashd/channels.go:24`, declared in
  `compose/compose.go:76` — only used by dashd to surface whapd link
  status. KEEP (krons uses whapd).
- `FAKEMCP_KEY` — `ipc/testdata/fakemcp/main.go:67` — test fixture only.
  KEEP.
- `CHANNEL_REGISTER_ALLOW_PUBLIC` — `chanreg/chanreg.go:222` — never set
  in any deployed `.env`; only used to widen public registration.
  Recommendation: KEEP (legit operator escape hatch, cheap).
- `EMAIL_STRICT_AUTH` — `emaid/main.go:68` — emaid not deployed
  anywhere. Falls with emaid removal.
- `TRUSTED_PROXIES` — `proxyd/main.go:63` — declared in
  `template/env.example`, read; KEEP.
- `ARIZUKO_DEV` — `core/config.go:181`, `ipc/ipc.go:768` — dev-only
  gate, read in two sites. KEEP.
- `CRACKBOX_ADMIN_SECRET` — `crackbox/pkg/host/host.go:105,130` —
  crackbox is sandbox dormant per memory. Sibling concern; not in scope
  to strip but verify before any crackbox cleanup.

Net: no truly dead env vars in the core. Most dead env vars hide
inside dead adapter binaries (see below).

### Dead code

- `discd/sent_ids.go` (`sentIDs.has()` at line 41) — `.has()` has **zero**
  non-test callers (grep across `discd/*.go` minus `_test.go` shows only
  `.add()`). Spec 6/J moved bot-message identification to a store column.
  Recommendation: DELETE `sent_ids.go` + `sent_ids_test.go`, drop
  `botMsgs *sentIDs` field on `bot{}`, drop the four `.add()` callsites
  in `discd/bot.go`. Net: ~120 LOC.
- `bskyd/`, `emaid/`, `linkd/`, `mastd/`, `reditd/`, `twitd/` — adapter
  binaries with no deployment (see "Per-instance adapter usage"). KEEP
  if you want the platform-coverage story for the README; DELETE if you
  want to ship lean and add back when a real user appears. My recommend:
  delete `twitd` (legally fraught, scraper-based), defer the rest.
- `ttsd/` — no service template, no compose generation reference, no
  deployment. Recommendation: DELETE unless a spec under `status: spec`
  references it.

### Dead MCP tools

Usage data: 186 transcripts × last 7d on krons. Histogram (top): `send`
(147), `inject_message` (139), `inspect_routing` (56), `refresh_groups`
(45), `inspect_messages` (33), `send_message` (18 — agent's
pre-migration name, still being called), `send_file` (3), the rest
≤2 calls or absent.

- `get_history` — `ipc/ipc.go:1819` — self-marked DEPRECATED alias for
  `inspect_messages`, 0 usage. DELETE.
- `pane_set_prompts` — `ipc/ipc.go:1110` — 0 usage in 7d. KEEP
  conditionally (slack-pane feature spec); verify if marinade slakd uses
  it before deleting.
- `pane_set_title` — `ipc/ipc.go:1137` — same as above; 0 usage.
- `post` — `ipc/ipc.go:927` — 0 usage; only relevant when bskyd/mastd/
  reditd are present (none deployed). DELETE iff those adapters get cut.
- `forward`, `quote`, `repost` — `ipc/ipc.go:1041/1056/1069` — 0 usage,
  guarded behind channel-action registration. Same conditional as `post`.
- `dislike` — `ipc/ipc.go:1082` — 0 usage; per memory
  `[dislike_via_like_emoji]`, on emoji-platforms this routes via
  `like(emoji='👎')`. Only Reddit has true downvote; with reditd dormant
  → currently dead. KEEP if reditd ships; DELETE if reditd is cut.
- `log_external_cost` (1 call), `invite_create` (0 calls) — both shipped
  recently per migrations 076 and 120; usage is low because adoption is
  early. KEEP — these are designed surface, not legacy.
- `escalate_group` (2 calls), `inspect_session` (1), `inspect_tasks` (1),
  `like` (1) — all low but non-zero. KEEP.

### Dead skills

- `ant/skills/dispatch/SKILL.md` — explicitly `user-invocable: false`,
  but per memory `[skill_discovery_pattern]` it IS invoked via `/dispatch`.
  KEEP — operator surface, just not auto-trigger.
- Skills not checked individually for usage signal (58 skills total) —
  see "Not checked".

### Stale specs

Did not deep-audit; spec directory has 13 numbered buckets. Per the
brief's exclusion, anything `status: spec` or `status: discussion` is
out of scope. Candidates worth a future targeted audit: `specs/11/`
(printing-press, multi-agent-commits) — multiple `status: planned`
entries dating from earlier project epochs.

### Dead web pages

- `/srv/data/arizuko_krons/web/pub/arizuko/index.html.bak-krons` —
  deploy-leftover from a prior rsync. DELETE on host (not in
  `template/web/pub/`).
- `/srv/data/arizuko_krons/web/pub/mayai/negotiation/index.html.bak` —
  same. Belongs to a sibling site, not arizuko, but flagged for the
  operator.
- `template/web/pub/concepts/slack-pane.html` — linked from
  `index.html`; KEEP.
- `template/web/pub/howto/*.html`, `examples/slink-sdk.html`,
  `crackbox/index.html` — not linked from root `index.html` but
  cross-linked from `concepts/` and `reference/`. KEEP.

Did not exhaustively crawl every page's inbound links — recommend a
proper link-check pass (`linkchecker` or similar) as a follow-up.

### Per-instance adapter usage

| Adapter | krons | sloth | marinade | Source-tree present? | Verdict               |
| ------- | ----- | ----- | -------- | -------------------- | --------------------- |
| teled   | yes   | yes   | yes      | yes                  | KEEP                  |
| whapd   | yes   | no    | no       | yes                  | KEEP                  |
| discd   | no    | yes   | no       | yes                  | KEEP                  |
| slakd   | no    | no    | yes      | yes                  | KEEP                  |
| mastd   | no    | no    | no       | yes                  | defer / strip         |
| bskyd   | no    | no    | no       | yes                  | defer / strip         |
| reditd  | no    | no    | no       | yes                  | defer / strip         |
| emaid   | no    | no    | no       | yes                  | defer / strip         |
| linkd   | no    | no    | no       | yes                  | defer / strip         |
| twitd   | no    | no    | no       | yes                  | strip (scraper-based) |
| ttsd    | no    | no    | no       | yes (no template)    | strip                 |

The "extras" daemon also exists on krons: `teled-rhias` — a second
teled instance per-channel-secret. Confirms teled is the load-bearing
adapter.

## Big wins

1. **Strip `discd/sent_ids.go` + callers** — ~120 LOC of confirmed
   add-never-read state. Smallest reversible win, no spec impact.
2. **Delete `get_history` MCP tool** — `ipc/ipc.go:1819-1825`, ~7 LOC.
   Self-documented DEPRECATED, 0 usage, agent migration 073 already
   teaches the new name.
3. **Delete `twitd/` adapter + template + compose entry** — ~600 LOC,
   no deployment, no listed spec for adoption.
4. **Delete `ttsd/` directory** — no template, no compose ref, orphaned
   scaffolding.
5. **Clean `.bak-krons` files** on krons web root — small, immediate.

## What NOT to delete

- **Low-usage MCP tools `invite_create` and `log_external_cost`** —
  they look dead in 7d window but shipped in migrations 076 and 120
  recently; their usage is "lagging adoption", not "dead surface".
- **`bskyd`, `mastd`, `reditd`, `emaid`, `linkd`** — currently unused
  but each implements a real protocol cleanly; deleting forfeits the
  "platform coverage" pitch. Demoting to a sub-directory or build tag
  is the gentler move.
- **Skill `ant/skills/dispatch`** — marked `user-invocable: false` but
  per project memory this is THE skill the operator types `/dispatch`
  to invoke, working around the auto-trigger gap.

## Not checked

- Individual skill usage from operator transcripts (`~/.claude/projects/`
  sessions) — sampled descriptions only, no per-skill call-count.
- Specs `status` field across all 13 buckets — only spot-checked.
- dashd handler/page traffic — dashd has no access-log; would need
  request-id tracing. Likely candidates for orphan pages: `routes_admin`,
  `groups_admin` (both shipped but operator-facing only).
- Inbound link crawl for `template/web/pub/`.
- compose.go: whether all 10 service slots actually generate without
  errors when their env vars are unset.
- whapd recent edits in working tree (`bot.go`, `server.ts`) — could
  contain new dead branches; not in scope for this audit pass.
