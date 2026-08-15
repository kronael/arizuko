# BUGS.md — open issues queue

> **Standing policy — every fix here adheres to WISDOM** (`~/.claude/CLAUDE.md`):
> minimality (smallest change at the root cause), orthogonality (one concern per
> fix, no parallel second path — amend the original), fail loud to the user on
> user-facing paths, retry only transient errors, fix causes not symptoms.
> Redesigns (new contract, changed cross-daemon control flow, auth-model or
> schema changes) stay recorded as proposals and ship only after user sign-off.

## Error-hygiene sweep: swallowed / mis-levelled errors in the hot path (2026-07-11, 2 FIXED, rest OPEN)

Sweep for the same class as the no-reply bug below (an error swallowed as
Warn/`_ =` while execution continues → silent degradation). Two shapes recur:
`if err == nil { use }` with the else branch missing, and `_ = err` on
bookkeeping writes that matter next turn.

**FIXED this pass** (both were missing error logs, no control-flow change):
- `routd/loop.go` `resolve()` — a transient `l.db.Routes()` read failure was
  indistinguishable from a clean route-miss, so the poll caller advanced the
  cursor and **dropped the inbound message** with zero log. Kept the
  fail-forward (retry risks a poison-message loop) but made the drop a loud
  `slog.Error`.
- `routd/turns.go:337` `handleDocument` — `s.deliver.Document` error had no
  `else`, so `send_file` failures returned HTTP 200 (row left pending) and were
  never logged, unlike the sibling `handleSend` (`turns.go:308` logs
  "deliver send failed"). Added the matching `slog.Error`.

**VERIFIED, OPEN** (real, left for prioritisation):
- `authd/main.go:108-112` — `core.LoadConfig()` failure → OAuth `/auth/*` not
  mounted, logged only `Warn`, server starts green (JWKS/health OK). Human login
  404s while everything reports healthy. **FIXED 2026-07-11** — elevated to
  `slog.Error` with the "login 404s while health green" symptom in the message.

**NOT A BUG** (verified benign, recorded so it isn't re-flagged):
- `container/runner.go:173` `ipcDir, _ := folders.IpcPath(in.Folder)` — the same
  folder is already resolved+validated via `buildMounts` (`runner.go:170`, which
  handles the error at `:560`) before line 173; `IpcPath` won't diverge for a
  folder that reached dispatch. Discard is a cosmetic asymmetry, not a live gap.
- `routd/prompt.go:29-39` (scout #2) — the store reads there don't surface an
  error at the call site (methods return zero-value/empty context); the one
  explicit `_ = UpdateObservedCursor` is **documented-benign** (comment L24-27:
  re-seeing observed messages is harmless, at-least-once by design).

**REPORTED by sweep, NOT yet independently verified** (read before fixing):
- `ipc/ipc.go:378-386` `writeJSON` — swallows `json.Marshal` error with bare
  `return`, no error frame → tool result vanishes, caller times out. (HIGH if
  confirmed.) **Verified + partially fixed 2026-07-13** — marshal failure now
  `slog.Error` (same log-add shape as the two FIXED items above; no
  control-flow change). Error-frame delivery stays open: writeJSON takes an
  opaque `v`, so a proper JSON-RPC error frame needs the request id threaded
  in — design, not a log-add.
- `ipc/ipc.go:348-361` — peer-cred/UID mismatch on MCP accept only `Warn`; gates
  every tool call for the group. (MED)
- `crackbox/pkg/admin/registry.go:84-107` `flushLocked` — `Warn`+return on
  disk-persist failure inside a void fn; handler still ACKs 200 → stale egress
  allowlist can reappear after restart. (MED)
- `routd/turns.go:670-676` `recordTurnResult` — discards `PutMessage`/`PutSession`
  errors (this path historically broke all replies post-split — verify first). (MED)
- `runed/manager.go:212-214`, `runed/docker.go:66` (steer silently no-ops),
  `ipc/ipc.go:340-345` (conn-limit reject, no error frame),
  `routd/dispatch.go:263,305,320` (terminal SetTurnState unlogged),
  `crackbox/pkg/host/host.go:130-132`, `routd/budget.go:32-43` (spend caps fail
  open on a DB hiccup). (MED/LOW)

Adapters (teled/discd/slakd/mastd/bskyd/reditd/emaid/chanlib) swept clean for
this class.

## Root-owned group home → agent runs config-less, silently never replies ("typing but no reply") (2026-07-11, ROOT CAUSE FIXED on krons, HARDENING OPEN)

Symptom (user-reported): the bot shows a typing indicator then never replies,
intermittently. Reproduced on krons folder `krons/content`: 4 turns (5729–5732)
dispatched + spawned, each ran ~33s, logged `[ant] Query done. Messages: 0,
results: 0` then `[ant] Input empty, exiting`, `code:0` — **no `turn_results`
row, no bot message**. rhias/nemo (healthy) replies normally.

**Root cause.** `groups/krons/content` was hand-created as **root:root** (Jul 7,
manual `mkdir`/host-CLI — the "never mkdir groups manually, use onbod/SetupGroup"
rule, `group_creation` memory). The container runs `user: 1000:1000`
(`compose/compose.go:694`); it can't `mkdir .claude` under the root-owned home, so
`cpDirImpl` (`container/runner.go:1146`) and `writeSettings`
(`container/runner.go:865`) both fail — **settings.json (which carries the
`mcpServers.arizuko` socket + outputStyle) is never written** → the SDK query
runs with no MCP tools and returns 0 messages → no reply. Note `krons/content` is
also a **ghost**: routed-to but NOT in `groups` (registered krons groups:
adshaus, happy, krons, krons/public/marble, mayai, rhias, rhias/content,
rhias/nemo). Fixed live: `chown -R 1000:1000 groups/krons/content`; uid-1000 can
now write `.claude`. Pending end-to-end confirm on next inbound.

**Hardening (OPEN).** Both write-failure sites swallow the error as `WARN` and
continue, producing a silently dead agent — violates CLAUDE.md "strict, not
magical / no silent fallbacks." Fix: when the group home is not writable (mkdir
`.claude` = permission denied, or settings.json write fails), fail LOUD — log
`ERROR` with the folder AND abort the spawn / deliver an error turn, rather than
running a config-less container. Also consider refusing to dispatch a folder with
no `groups` row (ghost-group guard) or auto-`chown` a fresh home to the run uid.

## F5 — `store.UserScopes` decides folder visibility without action or deny (2026-08-05, PARTIALLY FIXED 2026-08-05)

`specs/5/32` makes `auth.Authorize` the one question the ACL answers.
`store.UserScopes` (`store/acl.go:200-228`) is a second reader:
`SELECT DISTINCT scope FROM acl WHERE effect='allow' AND principal IN (…)`.
It ignores the `action` column entirely, has no deny-wins precedence against a
scope that also carries an allow row on a different action, and is blind to
wildcard-principal rows that `auth.Authorize` honours.

Its callers split, and only one half is a defect:

- **Inert** — `dashd/authz.go` feeds the result into `auth.Caller.Extra` as
  principal aliases, and a full `auth.Authorize` downstream re-checks action,
  deny and wildcards. The blindness cannot escape here.
- **Live** — `dashd/authz.go` `visible()` / `requireVisible()` /
  `requireOperator()` and `onbod/dash.go:20-29` treat the derived set as the
  terminal 200/403 decision with no downstream `Authorize`. A deny row that
  `auth.Authorize` would honour does not hide a folder from the dashboard.

So the violation is real but confined to the read/visibility surface, not the
mutation surface. Routing visibility through `auth.Authorize` changes which
folders appear for existing users — a live authorization-behaviour change.

- **Severity:** medium (deny rows do not suppress dashboard visibility)
- **Scope:** store.UserScopes vs auth.Authorize
- **Affected:** all instances — dashd + onbod folder lists
- **Source:** store/acl.go:200-228; dashd/authz.go visible/requireVisible/requireOperator; onbod/dash.go:20-29
- **Status:** PARTIALLY FIXED. The second READER is gone: `UserScopes` moved to
  `auth.UserScopes` and now projects the same rows `Authorize` loads (expanded
  principals + wildcard-principal rows) instead of its own
  `SELECT DISTINCT scope … principal IN (…)`. That closes the wildcard-principal
  blindness outright — a `google:*` grant authorized fine yet produced an EMPTY
  `X-User-Groups`. Documented and tested as a LISTING that can never be a
  verdict, with the deny/action gaps pinned as tests rather than left implicit.
  Live impact nil: no instance has a wildcard-principal row or a deny row (all
  43 rows fleet-wide are `allow`).

  **STILL OPEN, still needs sign-off:** dashd's read gate remains a scope-list
  match rather than an `auth.Authorize` call, so a deny row still cannot
  suppress dashboard visibility. That last step is a policy decision, not a
  refactor — it must pick WHICH action dashboard read requires (`interact` is
  the natural floor), and a `[]string` header is structurally incapable of
  carrying deny, so the gate has to move off the header entirely. Not shipped
  inline. Note the original entry overstated onbod: `onbod/dash.go`
  `requireOperator` only tests for `**`, it does no per-folder visibility.
  **Re-verified 2026-08-15 at `bcb5bb83`; still exactly as described, still a
  redesign.** `store.UserScopes` is gone — `store/acl.go:196` is now only a
  pointer to `auth.UserScopes` — so the second READER really did close. The
  live half is unchanged: `dashd.callerScope` (`dashd/authz.go:44`) reads the
  `X-User-Groups` header, falls back to `auth.UserScopes(sub)`, and treats
  `slices.Contains(allowed, "**")` as operator; `visible()` then decides
  200/403 by `auth.MatchGroups` over that list. There is no `auth.Authorize`
  anywhere in dashd's read path. Not fixed here: closing it picks which action
  dashboard read requires and moves the gate off a `[]string` header that
  cannot represent deny — an auth-model change, which is precisely the class
  that ships only after sign-off.
- **Found:** specs/5 frontmatter audit; `5/32` demoted to `partial`, now shipped.

## F77 — skill-guard's per-line scan is evadable by splitting a payload (2026-08-08, PARTIALLY FIXED 2026-08-08)

The demonstrated case is fixed: shell line-continuations are joined before
scanning, so `curl \` + newline + payload no longer scans `safe` while the
one-line form scans `dangerous`. Reproduced by codex and independently before
the fix; regression test in `ant/src/skillguard.test.ts`.

The CLASS remains open. A payload assembled from string concatenation, built in
a variable, or split across a here-doc still passes a regex table that reads one
line at a time. Closing it means AST or dataflow analysis, which
`specs/5/23` explicitly puts out of scope.

Not a containment boundary — Docker, the crackbox egress allowlist and the gated
MCP socket are, and `SECURITY.md` states this. Recorded so the residual is not
mistaken for coverage.

**Re-verified 2026-08-15 at `bcb5bb83`: accurate as written, no action.** The
demonstrated fix is present and is the one described — `logicalLines`
(`ant/src/skillguard.ts:46`) joins a line ending in `\` with the next before
scanning. The residual class is genuinely out of scope: closing it needs AST or
dataflow analysis, which `specs/5/23` excludes by decision, so there is no
contained fix to make and nothing to close. Left open deliberately as a
recorded residual, which is what this entry is for.

## L31 — a product never really updates: four of six payload kinds are seed-once (2026-08-15, proposal — needs sign-off)

`arizuko packages upgrade` / `sync` re-apply exactly two payload kinds: skills
(and `blendSkills` REFUSES any skill whose hash moved, so a locally edited one
is skipped) and `settings.json` keys not already present. `PERSONA.md`, the
`CLAUDE.md` region, `facts/` and `tasks.toml` go through `blendSeedOnce`, which
writes only when nothing is there yet and never rewrites
(`container/blend.go` `seedOnce`).

Consequence: a product author cannot fix anything they shipped. A typo in
`PERSONA.md`, a wrong fact, a bad default task — every group that already
installed the product keeps it forever, and the author has no channel to
correct it. `5/28` calls itself a package manager; on four of six payload kinds
it is a one-time copy.

- **Severity:** medium — no failure, but the headline capability does not work
- **Scope:** `5/28` composition, the blend table's "On upstream update" column
- **Affected:** every installed product; nothing on the fleet has been upgraded
  in place because there is no meaningful upgrade
- **Source:** `container/blend.go` `blendSeedOnce` vs `blendSkills`;
  `specs/5/28-packages.md` §Composition table
- **Status:** proposal, operator-stated 2026-08-15. **A product is overwritten
  on upgrade, like any software package; customization lives in the local
  layer, not in the package's files.**

  The local layer already exists and is already managed: the agent's memory and
  `facts/`, the operator's `~/CLAUDE.md` overlay, `.disabled` to drop a skill.
  The contract becomes two clean surfaces — product files are the product's,
  local state is the group's — instead of one file with two owners and a
  dirty-hash arbitration between them.

  `CLAUDE.md` is the precedent, not an exception: marked regions already give a
  product ownership of its own region while text outside it is never rewritten
  (`<!-- arizuko:package:<name> BEGIN/END -->`). The proposal extends that
  contract to the other payloads rather than inventing one.

  **What it dissolves.** Per-product `migrations/` (`L?`, deferred then refused
  2026-08-15) exists to transform state you cannot overwrite. If files are
  replaced, the only state left to migrate is the agent's own memory, which the
  agent curates. The deferred runner stops being owed.

  **What it costs, and the one question to settle.** Everything edited in place
  today needs a home in the local layer. `PERSONA.md` is the sharp case:
  operators do tune a persona by editing the file, and under this rule that edit
  is overwritten. Either a persona tweak becomes an overlay, or `PERSONA.md`
  keeps a carve-out — that choice is the whole decision, and it should be made
  before the table is rewritten. `tasks.toml` needs the same answer (an
  operator-added task must not vanish on upgrade).

  On sign-off this is a `5/28` spec edit first — the blend table's two right-hand
  columns — then `container/blend.go` moves payloads from `blendSeedOnce` to the
  managed path, keeping dirty-detection only where a carve-out survives.
- **Fix:**

## L30 — the SSE hub key is an unwritten string convention both ends re-derive (2026-08-15, open)

`webd/turn.go` was fixed on 2026-08-15 to subscribe on the real chat JID rather
than the route token's `target_folder` (spec `5/J`). The fix is correct and
tested, but it treats one symptom of a shared cause: **nothing owns the hub
key.** Each end reconstructs it with string surgery, and the two must agree by
convention alone.

Four remaining sites, all verified at `8ef52f47`:

- `routd/turns.go:716` — publishes on `strings.TrimPrefix(tc.ChatJID, "web:")`.
  For a `hook:` chat JID the prefix does not match, so the key stays
  `hook:a/b` while every webd subscriber builds a `web:`-derived key.
- `webd/channel.go:59` — `handleRoundDone` rebuilds `jid := "web:" + req.Folder`
  and looks the topic up by it. A `hook:` chat gets `"web:hook:a/b"`, so
  `TopicByMessageID` returns `""` and the event publishes under an empty topic.
- `webd/route_token.go:195,382` — `handleChatTokenPost` / `injectRouteMessage`
  subscribe and publish on `row.Folder()` (the token's target), while the
  assistant frame arrives through `handleSend` keyed on the chat JID. A
  suffixed token's `wait=` POST never sees its own reply.
- `webd/route_token.go:419,435` — `handleRouteTokenStream` builds
  `"web:" + folder` from the CLIENT-SUPPLIED `group` param and subscribes to
  it, so a suffixed chat is unreachable and the param decides the key.

- **Severity:** medium — each site is a hang, not a leak, but the last one lets
  a request parameter choose a subscription key.
- **Scope:** webd SSE delivery
- **Affected:** every route token minted with a `jid_suffix`, and every `hook:`
  chat
- **Source:** `routd/turns.go:716`; `webd/channel.go:59`;
  `webd/route_token.go:195,382,419,435`; contrast the fixed `webd/turn.go:125-140`
- **Status:** open, and the honest fix is a REDESIGN needing sign-off: give the
  key ONE renderer — a `store` helper that turns a chat JID into a hub key —
  and make both daemons call it instead of each spelling the convention. Patching
  the four sites individually re-creates exactly the drift that produced them.
  Same class as [`L4`](#l4) and the `5/J` fix; see the identity-derived-not-read
  rule in `CLAUDE.md`.
- **Fix:**

## L27 — five channel platforms ship no output-style file, so their agents format blind (2026-08-15, open)

`specs/5/Y` says WhatsApp, Reddit, Bluesky, Twitter and LinkedIn "keep their
single platform file". None of those files exists. `ant/output-styles/` ships
`slack{,-dm,-channel,-thread,-pane}`, `telegram{,-dm,-group}`,
`discord{,-dm,-channel}`, `email.md`, `web.md` and `README.md` — nothing else.
(Mastodon is correctly absent: `mastd` was dropped 2026-08-15.)

The picker does no host-side existence check by design (`5/Y` §"Name selection
and file loading are separate concerns"), so it names `whatsapp` for a whapd
turn, Claude Code's `readOutputStyle` finds no such file, and the agent answers
with Claude Code's stock formatting — markdown a platform may not render, no
length envelope, no per-surface shape.

- **Severity:** medium — every reply on five live adapters is unstyled; whapd
  and reditd both run today.
- **Scope:** agent output formatting
- **Affected:** whapd, reditd, bskyd, linkd, and the Twitter surface
- **Source:** `ant/output-styles/` (14 files, none of the five);
  `specs/5/Y-output-styles-per-surface.md:33-36`
- **Status:** open. Two halves, and only the first is mechanical: the spec
  sentence is false and must say those platforms ship no file today. Whether to
  AUTHOR the five files is a content decision — each one is a real
  length/markup envelope per platform, not a template — so it wants the user's
  call before anyone writes prose into the agent's mouth.
- **Fix:**

## L25 — slakd acks Slack before routd accepts, so a deploy silently drops messages (2026-08-15, proposed)

`slakd.handleEvents` answers `200 OK` to Slack BEFORE calling routd
(`slakd/bot.go:405-406`, comment: "ack first; Slack retries on non-2xx, which we
don't want for handled events"). If the routd call then fails, `dispatch` logs
`deliver failed` and returns (`slakd/bot.go:546-548`). Slack has its 2xx, so it
never redelivers. The message is gone, with one ERROR line as the only trace.

`slakd.yml:23` declares `depends_on: [routd]` with no `condition:
service_healthy`, so the documented deploy — `systemctl restart
arizuko_<instance>` — restarts both and opens exactly this window on every
release.

`teled` does the opposite and is correct: `teled/bot.go:212-224` advances its
getUpdates offset ONLY when `delivered` is true, so Telegram redelivers on the
next poll. The two adapters disagree on whether an inbound is durable.

- **Severity:** high — silent user-visible data loss on the documented deploy
  path, on the adapter krons and marinade both run.
- **Scope:** slakd inbound durability
- **Affected:** every Slack message arriving while routd is restarting or down
- **Source:** `slakd/bot.go:405-406,546-548`; `template/services/slakd.yml:23`;
  contrast `teled/bot.go:212-224`
- **Status:** proposed — the fix is a REDESIGN and needs sign-off.

  Deferring the ack until after routd accepts is the obvious change and is
  wrong: Slack requires the ack inside 3 seconds and retries on non-2xx, so a
  slow routd would turn one message into several. The durable answer matches
  what we established for outbound on 2026-08-15: slakd now owns `slakd.db`,
  so an inbound that routd rejects should land in a local `inbound_pending`
  table and be re-delivered by a sweep, exactly as `teled` re-polls. That is a
  new mechanism and a new table, hence a proposal, not an inline fix.

  Sibling question for the same decision: every other adapter that acks a
  platform webhook before calling routd has this shape. `whapd` already has a
  filed instance (`L20` — it queues undelivered messages in memory and
  200-acks). One durable-inbound mechanism in `chanlib`, one table per adapter
  daemon, would answer both rather than two bespoke fixes.
- **Fix:**

## L26 — `make test-race`, the documented pre-tag gate, cannot finish (2026-08-15, open)

`routd` and `store` both hit `panic: test timed out after 10m0s` under `-race`,
so the target never completes and never gates anything. CI does not run it
either. 0 races were reported in the packages that did finish, so this is a
timeout, not a detected race — but a gate that cannot run is not a gate, and
the release procedure names it.

- **Severity:** medium — no race coverage on the two most concurrent packages
- **Scope:** release verification
- **Affected:** every tag; `routd` and `store` in particular
- **Source:** `make test-race`; reproduced 2026-08-15 during the CTO audit
- **Status:** open — needs either a longer `-timeout` for those two packages, a
  split target that runs them separately, or the gate removed from the release
  procedure and replaced with something that runs.
- **Fix:**

## L24 — a fresh instance ships with egress isolation OFF, while the docs describe it as the model (2026-08-15, proposed)

`CRACKBOX_ADMIN_API` is the master switch for egress isolation
(`core/config.go:265` → `Config.EgressAPI`; `compose/compose.go:493,621` gate
the crackbox service and the per-folder agent networks on it). Nothing seeds it:
`template/env.example` is 20 lines and does not mention it, and no code under
`cmd/arizuko/` writes it. So `arizuko create` produces an instance whose agent
containers reach the internet unfiltered, silently.

`SECURITY.md:249` and `ARCHITECTURE.md:23,88,761` describe egress isolation as
the deployment model without saying it is opt-in and off by default. A reader
who follows `INSTALL.md` gets the opposite of what the security page describes,
with no warning at any step.

**Correcting the finding that surfaced this.** The 2026-08-15 CEO memo
(`.ship/critique-ceo-20260815.md`) reads `container/runner.go:184-191` — the
`egress` grant appending `"*"`, which `crackbox/pkg/match/match.go:49` honours
as allow-all — as egress default-deny being "negated by its own grant". That
half is NOT a defect: it is the documented behaviour of the `egress` grant per
spec `5/33`, and the comment at the call site says so. The grant is how an
operator deliberately opens a folder. The real gap is the one above — the
control is off unless an operator knows to switch it on.

- **Severity:** high — a security control the docs present as the model does not
  run on a default install, and nothing says so.
- **Scope:** instance seeding + the security docs
- **Affected:** every instance created from `arizuko create`; readers of
  SECURITY.md and ARCHITECTURE.md
- **Source:** `core/config.go:265`; `compose/compose.go:493,621`;
  `template/env.example` (no entry); `SECURITY.md:249`
- **Status:** proposed — needs a decision, two parts:
  1. **Docs (no sign-off needed).** State plainly in `SECURITY.md`,
     `ARCHITECTURE.md` and `INSTALL.md` that egress isolation is opt-in, name
     the variable, and say what an instance without it allows.
  2. **Default (needs sign-off).** Whether `arizuko create` should seed
     `CRACKBOX_ADMIN_API` so a fresh instance is filtered by default. That
     changes what a new install runs and requires the crackbox image to be
     present, so it is a deployment-behaviour change, not an inline fix.
- **Fix:**

## L13 — `secret_use_log` is write-only: no read surface on any face (2026-08-11, proposed 2026-08-15 — needs sign-off)

The table (routd/migrations/0008-secrets.sql) is written on every resolved
secret (`routd/mcp.go:503`, `store/secret_use_log.go:38`) and read by nothing:
no resreg resource, no REST/MCP face, no dashd page, no CLI verb — only the
side `audit_log` row each write also emits is reachable. This repeats the exact
class BUGS `F29`/`F35` closed for the four `audit_log` owners ("audit rows
reachable only with sqlite3"), and it outlived `F68` (2026-08-09), which fixed
this table's writer fields without giving it a reader. Cost: the per-key
forensic detail (key, tool, scope_kind) the writer was just fixed to record
answers "who used which secret" only to an operator with sqlite3 on the host.

- **Severity:** low
- **Scope:** secret-use forensic log read surface
- **Affected:** routd, store
- **Source:** `store/secret_use_log.go:38`, `routd/migrations/0008-secrets.sql`
- **Status:** PROPOSED, not built — the premise is CONFIRMED at 28d20228
  (readers are four test files and nothing else; no `Table:` declaration, no
  REST handler, no dashd page, no CLI verb), but the fix is a new wire contract,
  which this pass does not ship unreviewed.
  Why it is not a mechanical registration: in this codebase a resource's name IS
  its wire identity, and `5/17` derives the MCP tool from the same declaration
  that serves REST. Registering `secret_use_log` therefore publishes a new
  `/v1/secret_use_log` path AND a new agent-visible tool in one move, and the
  question "may an AGENT read which secrets were used, by whom, with which
  tool?" is a security decision the operator owns, not a detail of the wiring.
  Recommended shape, for approval:
  - Owner routd (it owns the table, routd migration `0008`), read-only: `list`
    with folder containment, no create/update/delete — the writer stays
    `store/secret_use_log.go`, the single one.
  - RowType mirroring the columns already written (`key`, `scope`,
    `scope_kind`, `tool`, `caller_sub`, `spawn_id`, `ts`), so the doc and the
    handler read one struct.
  - Gate: operator scope on REST. On the agent MCP face, either omit it
    entirely (operator-only forensics, the safer default and my
    recommendation) or bind it to the caller's own folder — never a plain
    `mcp:` grant with the default target resolver.
  - `F35`'s onbod `audit_log` resource is the precedent to copy for the
    read-only shape.
- **Evidence:** table writer/reader sweep over all migrations
  (`INSERT INTO|UPDATE` vs `FROM <table>` vs `Table:` resreg decls) →
  `secret_use_log writers=1 readers=0 resreg=0`. `grep -rn secret_use_log`
  → writer paths + migrate-split copy + docs only.

## L15 — dead-code sweep 2026-08-11: the hits deliberately REJECTED (record, not a bug)

Not a defect. This is the audit's negative result, kept so the next sweep does not
re-report the same ~135 hits. `deadcode ./...` gave 149 hits, 56 under `-test`;
nine survived verification and are filed above as `L6`-`L14`.

- **Status:** record only — nothing to fix

Production run: 149 hits. With `-test`: 56. Buckets and verdicts:

- **crackbox/** (~67 prod hits; 48 survive `-test`: `pkg/host` + `pkg/host/internal`,
  `pkg/admin.NewAPI`, `pkg/dns Server.LocalAddr`, `pkg/match.LooksLikeDomain`) —
  REJECTED: crackbox is a shipped standalone component with external users
  (`specs/6/8`; brief's false-positive #2). Reachability from arizuko's mains is
  the wrong root set for a public library. Not adjudicated here: whether some
  `pkg/host/internal` helpers are dead even relative to crackbox's public API —
  out of scope for an arizuko audit, flag to a crackbox-scoped pass if wanted.
- **tests/testutils** (28 hits: FakeChannel/FakePlatform/NewInstance/etc.) —
  REJECTED: shared test library; the 8 that survive `-test`
  (Name/Connect/Disconnect/Forward/Quote/Repost/Dislike/Edit) are interface-
  satisfaction stubs, live through the Channel interface contract.
- **resreg/resregtest** (8 hits) + `resreg/engine.go:1311 reset` ("Test-only." by
  its own comment) — REJECTED: test harness by design.
- **routd/db.go:87, runed/db.go:61, store/store.go:178 `OpenMem`** — REJECTED:
  conventional in-memory constructors for tests, colocated with the real Open.
- **runed/runtimes.go FakeRuntime.Run/Kill** — REJECTED: fake in a prod file so
  other packages' tests can import it; standard pattern.
- **queue SetSignalContainerForTest / SetActiveForTest** — REJECTED: named test
  seams (the *unnamed* dead queue surface is filed above).
- **proxyd/resource.go:114 installManualWeb** — REJECTED: self-labeled
  "test-only entry point" in its own doc comment.
- **chanlib isStubVerb + CapImplReport** — REJECTED: consumed by nine adapter
  test suites (`emaid/bskyd/slakd/discd/mastd/reditd/linkd/teled/caps_test.go`,
  `chanlib/extra_test.go`) — the drift-guard is doing exactly its intended job
  at test time.
- **chanlib NoFileSender.SendFile** — REJECTED (with note): unused mixin, but
  its siblings `NoVoiceSender` (5 adapters) and `NoPinSupport` (2) are embedded;
  it is the deliberate mixin family's spare slot for the next file-less adapter.
  4 lines; deleting buys nothing.
- **core/types.go RouteTarget.String** — REJECTED: fmt.Stringer convention;
  absence of any `%v` path over a RouteTarget value is not provable by grep, and
  a wrong deletion breaks log formatting silently.
- **groupfolder/folder.go:34 IsTopLevel** — REJECTED: documented shape predicate
  of the leaf library (deliberately renamed from IsRoot with a semantics note),
  test-covered; 1 line.
- **auth/surrogate/engine.go:83 NewEngineWith** — REJECTED: own comment says
  "tests point a…" — a named test seam.
- **auth/jwks.go:50 NewKeySet** — REJECTED as dead code (test convenience), but
  its stale "used in-process by authd" comment is filed in the wrapper-atrophy
  entry above.
- **chanlib/handler.go:252 NoFileSender / core RouteTarget / audit loadCursor+cursor.get/set/save** —
  cursor family NOT rejected: filed inside the audit-messages entry (same
  deleted chain).
- **auth/jwt.go:29 mintJWT** — NOT rejected: filed as the HS256 entry.
- **routd/tokens.go (5), routd/db.go (4), routd/sibling_db.go (2),
  routd/pending_actions.go (2), routd/loop.go StopQueue, queue
  RegisterProcess/StopProcess/SetHasPendingFn, runed/manager.go intersect,
  obs (3), audit emitMessage** — NOT rejected: filed above.

# Not determined (UNVERIFIED)

- ~~Whether any live instance sets `AUDIT_WEBHOOK_URL_MESSAGES`~~ — MOOT
  2026-08-15: the knob and its producer-less stream are deleted (`L11`). No
  setting of it ever produced an event.
- ~~`authd/http.go:40-43`'s claim that service:runed's grants entry gates a
  per-turn downscope~~ — RESOLVED 2026-08-15 while fixing `L8`: the claim was
  FALSE. runed calls `auth.ServiceToken` nowhere and mints nothing, so the
  grant gated no call. Grant, comment and dead plumbing are deleted.
- Whether the 2s poll fully substitutes for the queue's unwired
  same-group-restart (`hasPending`) in production load — behavior inference
  from code, not observed live.

## L2 — a delegated child is never dispatched: its only message is a bot row (2026-08-08, CAUSE ISOLATED 2026-08-15, fix needs sign-off)

Same live run, `child-delegate`. Unlike L1 this one is NOT root-caused, and it
is recorded that way on purpose.

What is verified from the logs: the parent registered the child
(`"group registered" jid=web:eval/etjgjnv-child-delegate folder=eval/etjgjnv-child-delegate`)
and delegated the task (`"send" folder=eval jid=web:eval/etjgjnv-child-delegate
text="Task: run this exact command and report the result back here"`). So
registration and delegation both work. The child never fired the callback.

**CAUSE ISOLATED 2026-08-15 by the call path.** No probe was needed and no
capability is involved. Agent-to-agent delegation over `send` cannot wake the
target folder, by construction:

1. The parent's `send` reaches `ipc.internalSend` → `gated.SendMessage` →
   `routd.mcpDeliver`, which delivers, and `ipc.putOutboundRow`, which persists
   the row. That writer stamps EVERY agent-authored row `FromMe: true,
   BotMsg: true` (`ipc/ipc.go` putOutboundRow) — there is no separate shape for
   "addressed to another folder".
2. The poll DOES see it: `DB.NewMessages` filters on timestamp only, so the
   child chat is picked up and `resolve` succeeds (the child folder was
   registered), and it is enqueued.
3. `processGroupMessages` then drops it (`routd/dispatch.go:52-62`):

       // Strip bot rows from the trigger batch (don't feed the agent its own
       // output) but keep them in the rendered context.
       for _, m := range msgs { if !m.BotMsg { trigger = append(trigger, m) } }
       if len(trigger) == 0 { l.advance(chatJID, last); return false, nil }

   The child's chat holds nothing BUT bot rows, so `trigger` is empty. The
   cursor advances and the function returns `(false, nil)` — the "silent no-op"
   that is deliberately NOT a failure, so it does not even trip the breaker.
   No container is ever spawned for the child.

Both recorded candidates are answered:
- The `egress` ACL vs `network_rules` question is MOOT. No container starts, so
  no egress decision is ever reached. The two mechanisms never got to disagree.
- "the child may never have been dispatched at all" is CORRECT, and now
  explained: it was enqueued and then dropped at the bot-row strip.

- **Severity:** medium — agent-to-agent delegation silently does nothing.
- **Scope:** message model — what may trigger a turn
- **Affected:** routd (dispatch), ipc (outbound writer)
- **Status:** NOT FIXED — the fix is a redesign and needs sign-off.

**PROPOSAL, for the user, not built.** The bot-row strip is correct and must
stay: it is what stops an agent re-triggering itself on its own output. The
missing distinction is that a row authored by folder A and addressed to folder
B is B's INBOUND, not B's own output. A fix has to say so explicitly, and that
is a new contract on the message model plus a loop-safety rule:

- Make the trigger test `m.BotMsg && m.Sender != <this folder>` rather than
  `m.BotMsg` alone, so a row a DIFFERENT folder wrote can trigger. `Sender` is
  already the author folder at this writer, so no new column is needed.
- Bound it, or two agents ping-pong forever: a hop budget carried on the row, a
  depth cap on the delegation chain, or a rule that only a registered CHILD
  folder may be woken by its parent. This is the part that decides the shape,
  and it is a product call, not a code call.
- Decide whether the woken turn is credentialed as the child (socket folder) —
  it is, since the socket is per-folder — and whether the parent needs an ACL
  action to wake another folder at all.

Until then, delegation should either be refused loudly at the `send` gate when
the target folder is not the caller's own chat, or documented as not supported.
Silently storing a row nobody will ever act on is the worst of the three.

## J16 — `ant/CLAUDE.md` tells the agent connector tools are deferred; they are eager (2026-08-09, proposed)

"Third-party connector tools (Slack, GitHub, …) do NOT [load eagerly] — they're
deferred behind the **Tool Search Tool**." Connectors are `ExtTools` on the
arizuko server, and that server is `alwaysLoad: true`, so they ride the eager
prefix. `ant/src/mcp-servers.ts` said as much in its own comment before `J1`
touched it. The agent is told to search for tools already in its list, which
costs turns and invites "capability missing" conclusions when a search misses.

Pre-existing, not caused by `J1` — but `J1` removed the last server that COULD
be deferred, so no deferred MCP server exists at all now.

- **Severity:** low
- **Scope:** agent instructions vs MCP assembly
- **Affected:** ant
- **Source:** ant/CLAUDE.md:104
- **Status:** proposed
- **Fix:** either drop the paragraph, or split the routd-side MCP server so
  connector tools really are deferred (`5/17` names the split as the cost).
  Editing `ant/CLAUDE.md` fires the release path — migration file,
  `MIGRATION_VERSION` bump, agent image rebuild — so it rides a release, not a
  doc commit.

## F75 — three skill trees are triplicated and one pair has already drifted (2026-08-08, open — verified 2026-08-15, still a redesign)

`ant/skills/p5js/` also exists under `ant/skills/create/art/.../p5js/` (same for
excalidraw and manim-video). A `diff` shows the top-level
`p5js/scripts/export-frames.js` carries a chromium `executablePath` fix the
`create/`-tree copy lacks — so the duplicate is not merely redundant, it is
already stale. Deciding which is canonical for skill dispatch is the fix.

**Verified 2026-08-15, premise still holds.** All three pairs are real and
each nested copy carries a `README.md` where the top-level carries `SKILL.md`
(dispatchable skill vs. reference resource read by the `create` router):
`ant/skills/p5js` ↔ `ant/skills/create/art/p5js`,
`ant/skills/excalidraw` ↔ `ant/skills/create/diagram/excalidraw`,
`ant/skills/manim-video` ↔ `ant/skills/create/video/manim`. The `p5js` pair's
`scripts/export-frames.js` still differs — the top-level tree has the
chromium `executablePath` fix, the `create/`-tree copy does not.
NOT fixed here: syncing the stale script would patch the symptom (this
instance of drift) while leaving the duplication mechanism that produced it
in place — the same class root `CLAUDE.md` calls out under "No duplication —
amend the original." The actual fix is the canonicalization decision the
entry already names (which tree is the single source, and whether the other
becomes a symlink/reference rather than a second copy), which is a
cross-cutting structural change to skill dispatch and wants sign-off.

## F53 — live krons teled has no state mount, so its offset resets every recreate (2026-08-07, open)

Deployed `/srv/data/arizuko_krons/services/teled.yml` carries neither a state
mount nor `DATA_DIR`; the repo's `template/services/teled.yml` has both. teled's
default dataDir is `/srv/app/home` (`teled/main.go:69`), unmounted for teled, so
`teled-offset-telegram` lives in the container's ephemeral layer and is lost on
every recreate.

The deployed fragment is stale against the repo. Worth a fleet-wide fragment
diff, not just a teled fix — `arizuko generate`'s relink only replaces fragments
that are still identical catalog copies.

- **Source:** deployed `services/teled.yml` vs `template/services/teled.yml`; `teled/main.go:69,78`
- **Status:** open

## F41 — the re-greet cooldown is a rate, not a cap (2026-08-06, open question — NOT a spec defect)

Shipped with the `5/31` fold and named in the spec rather than hidden. A chat
that (a) keeps messaging, (b) still routes nowhere, and (c) never redeems its
pairing link is offered a fresh one every `store.PairingTTL` (10 min) — 144/day
in the worst case. Before the fold it was greeted exactly once, ever, which was
the permanent lockout `P1b` blocker 2 existed to remove; the trade is a live
link the user can always get for a message the user cannot switch off.

Two bounds already hold and are what make this a rate rather than a flood: the
re-arm sits in `store.InsertOnboarding`, which only routd's route-MISS branch
calls, so a since-routed chat and a silent chat are BOTH never greeted again
(this is what stopped the timer-only design from re-greeting three live krons
group chats every ten minutes — see `5/31`).

Open question, not a defect with an obvious fix: should the Nth greeting back
off? A `prompt_count` column and a widening interval is the obvious shape, and
it is a new column plus a policy constant for a case nobody has hit yet. Left
as a rate until an instance reports it.

**Reclassified 2026-08-11 — this never made `5/31` defected.** A spec is
`defected` when an open bug CONTRADICTS its guarantee. `5/31` names the rate in
its own text and shipped it deliberately, so the spec is accurate and the code
matches it; what is open is a future policy choice, which is a backlog item.
`5/31` returns to `shipped` and keeps this entry as the recorded question.

## F46 — `runed`, `routd` and `authd` all serve `GET /v1/sessions`, for different tables (2026-08-06, open — CORRECTED 2026-08-07: three daemons, not two)

**Correction (2026-08-07, during `F33`).** This is THREE daemons, not two.
`routd/server.go:286` also mounts `GET /v1/sessions` (hand-rolled,
`s.handleSessionGet`, reads `core.SessionRecord`). The original entry missed it.
The count matters: it was the reason `F33`'s signed-off "derive the advertised
set by probing paths" approach was rejected and rebuilt as derivation by mount
IDENTITY. Probing would have made BOTH routd's and runed's `/openapi.json`
publish authd's `SessionsRow` schema.

Found while picking a foreign-resource anchor for `runed`'s new doc-vs-mux
guard: `sessions` was unusable as one, because `runed` really does serve that
path. `runed/server.go:44` mounts `GET /v1/sessions` (hand-rolled, reads
`session_log` — agent spawn sessions); `routd/server.go:286` mounts it over
`core.SessionRecord`; `authd/http.go` mounts the resreg `sessions` resource at
the same path (refresh-token families, `SessionsRow`). Three daemons, one wire
path, three unrelated tables.

Since `F33` the doc no longer lies about this — only authd's RegisterREST mount
is documented, and `resreg/openapi_mount_test.go` pins that a hand-rolled mount
at a resource's path documents nothing. The wire-identity collision itself is
untouched and still wants the rename below.

Not currently reachable as a bug: the daemons listen on separate containers, and
`runed`'s is hand-rolled rather than a resreg registration, so
`TestResourceEndpoints_SingleSource` sees nothing. It is the exact shape the
`Name` rule exists to prevent — "a resource's name IS its wire identity,
globally unique" (root `CLAUDE.md`), the rule `proxyd_routes` violated in
2026-07 — and it bites the moment either surface is federated behind one host,
or `runed`'s spawn sessions become a resreg resource (which `5/16`'s adoption
program would do).

- **Severity:** low today, medium on federation (an aggregator or generated
  client keying on `/v1/sessions` gets whichever daemon it was pointed at)
- **Scope:** runed/server.go:44-45, routd/server.go:286, authd sessions resource
- **Source:** runed/server.go:44 `mux.HandleFunc("GET /v1/sessions", …)`;
  routd/server.go:286 `mux.HandleFunc("GET /v1/sessions", s.handleSessionGet)`;
  authd/http.go `s.mountSessions(m)` → `/v1/sessions`
- **Fix:** rename `runed`'s to its actual subject before it becomes a resource —
  `/v1/spawn_sessions` (and `/v1/spawn_sessions/recent`), matching `runed`'s
  `spawns`/`session_log` vocabulary. `authd` keeps `sessions`: it is the
  registered resreg resource and the one with a REST twin, so it holds the wire
  identity. Renaming `runed`'s touches its one federated caller (routd's recent-
  sessions read, spec 5/P) — a cross-daemon path change, so it wants sign-off
  rather than an inline fix.

## F37 — three dispatch tests fail under whole-suite load, so the suite cannot gate a release (2026-08-06, open)

Found while verifying `F36`. Two packages failed on two separate whole-suite
runs and passed every isolated re-run:

- `tests/TestFeature_MessageRouting/inbound-to-dispatch` — `bot rows = 0, want
1`, with `ERROR silent turn: clean run delivered nothing` in the log. It
  polls `dispatchedFolder` on a 5s deadline and then asserts the bot row
  immediately; the dispatch was seen, the reply row was not yet written.
  Passed 6 of 6 isolated, and `go test ./tests/ -count=3` clean.
- `slakd` — one whole-suite failure, no `--- FAIL` line captured at all (only
  the package-level `FAIL`), then 20 of 20 subtests PASS isolated and
  `-count=3` clean.

None touches `authd`, refresh rotation, signing keys or sessions; the same
tree ran `53 ok / 0 fail` on three other whole-suite runs, including one
immediately before and one immediately after these.

**Third instance, same package, new test name (2026-08-06, base `9c944875`).**
`tests/TestSplitFederation_InboundToTurnRoundTrip` failed one whole-suite run
(`52 ok / 1 fail`), then passed isolated (0.098s), at `-count=3` on the package,
and on a whole-suite re-run that came back `53 ok / 0 fail`. So the flake is not
specific to `inbound-to-dispatch`: it is the `tests/` package's poll-then-assert
shape, and the fix below should be applied to every such site in the package,
not just the one named test. Disk was 89% throughout, not 100%, which weakens
the disk-pressure confound for THIS instance without eliminating it.

**A second, unrelated failure mode seen in the same session — do not confuse it
with this bug.** One whole-suite run returned `0 ok / 61 FAIL`, every package
`[setup failed]` with `package net is not in std
(/home/onvos/sdk/go1.27rc2/src/net)`. The directories all exist; the next run
was clean. That is a transient toolchain/cache race under concurrent agents, not
a dispatch flake and not `no space left on device` — it presents as total,
uniform failure across every package, whereas this bug is one or two packages.

**A confound that must not be dropped from the record.** The host was at 100%
disk during the first failure — a later run in the same session aborted
outright with `no space left on device` across ~40 packages — and three agents
were running suites concurrently. So "flaky under load" is supported, but
"flaky under *load alone*" is not: disk pressure is an equally good explanation
and was demonstrably present. Do not close this by re-running on an idle box;
that reproduces the pass, not the failure.

This is the same shape as `F36`'s own filing — a test whose outcome depends on
scheduling is reporting something, and the honest first step is to make the
failure reproducible rather than to re-run until green. The difference is that
`F36`'s invariant was over committed rows and could be forced with a barrier;
these two are waiting on asynchronous work with no barrier to hold.

- **Severity:** medium (a suite that flakes under load cannot gate a release,
  and the two flakes were on different packages on different runs)
- **Scope:** the `tests/` package's poll-then-assert sites (at least
  `feat_routing_test.go` and `TestSplitFederation_InboundToTurnRoundTrip`),
  `slakd` package tests
- **Affected:** CI and any developer running the full suite on a busy machine
- **Source:** tests/feat_routing_test.go:66-75 (poll-then-assert); TestSplitFederation_InboundToTurnRoundTrip; slakd whole-package FAIL with no subtest line
- **Status:** open
- **Fix:** give `inbound-to-dispatch` the same deadline treatment for the bot
  row that it already gives the dispatch — poll `countBotRows` rather than
  reading it once the instant dispatch is observed. `slakd` needs a captured
  failure first: run it under `-count=N` with the log kept, because a
  package-level `FAIL` with no `--- FAIL` line is usually a panic or timeout in
  a goroutine, not an assertion.

## F34 — "who ended this engagement" has two answers in two tables (2026-08-06, PROPOSED — needs sign-off, DISPROVED)

Found while shipping `5/G`'s disengage control, which fixed the REST half only.

A `(jid, topic)` window can be ended early by two callers, and they now record it
in two different places:

- **REST** — `POST /v1/engagement` writes an `audit_log` row inside the write's
  own transaction (`routd DB.SetEngagementAudited`, `engagement.clear`). This is
  what `/dash/engagement/`'s button uses.
- **MCP** — the agent's `disengage` tool writes an `ipc_audit` row plus an
  `audit-system.jl` line, through the generic wrapper every `granted` tool passes
  (`ipc/ipc.go:909`). No `audit_log` row, and not in the write's transaction —
  `db.SetEngagement` is a bare Exec.

So an operator asking "who stopped the bot talking in this chat" must query
`audit_log` AND `ipc_audit` and merge two schemas, and only one of the two
answers is transactional. `/dash/audit/` reads `audit_log`, so the agent's own
disengages are invisible there.

This is the same shape as root `CLAUDE.md`'s "one renderer, many sinks": two
paths into one question drift. Note the older claim that both writers were
already audited in routd's transaction — repeated in `F31`, `5/G`, the
`serviceGrants` comment and `dashd/README.md` — was true of neither before today
and is true of one now; those four sites are corrected.

The fix is not local. `ipc_audit` predates `audit_log` and ~45 hand-rolled ipc
tools feed it (`store/ipc_audit.go` already says new callers should prefer
`audit.EmitInTx`), so moving `disengage` alone would make engagement the one tool
that reports differently from its neighbours — trading a split between SURFACES
for a split between TOOLS. The real question is whether the `granted` wrapper
should emit `audit_log` for every state-changing tool, which is a cross-cutting
contract change over every ipc tool and therefore a sign-off, not an inline fix.

- **Severity:** low (both writes ARE recorded; the cost is that answering one
  question takes two queries, and `/dash/audit/` shows only half)
- **Scope:** `ipc` audit contract vs `audit_log` (specs 5/I, 5/G, 5/16)
- **Affected:** all instances
- **Source:** ipc/ipc.go:873-925,1571-1578; routd/db.go SetEngagement /
  SetEngagementAudited; store/ipc_audit.go:16; dashd audit page reads audit_log
- **Status:** PROPOSED — the narrow fix (disengage only) is worse than the gap;
  the cause fix is the `granted` wrapper, which needs sign-off
- **Blocks:** nothing. `5/G` is shipped — its DoD asks that the dashboard
  mutation be audited, and it is.

- **DISPROVED 2026-08-06.** `store.LogIPCAudit` already writes `audit_log`
  via `audit.EmitDB` (`surface=mcp`, `action=mcp.tool.invoke`); only the
  function NAME is legacy. Live krons: 50 `mcp/set_web_route` + 7
  `mcp/delegate_group` rows, and the `ipc_audit` table no longer exists there.
  The premise came from a stale field comment at `ipc/ipc.go:251` saying
  "persists one ipc_audit row" — a claim, not evidence. Comment corrected.
  No code move needed; `disengage` is audited like its ~45 neighbours.

## F38 — `task_run_logs` holds orphan rows on all three live instances (2026-08-06, open)

Found while proving migration `0031` caused no FK damage: `PRAGMA
foreign_key_check` on a `.backup` copy of each live `routd.db` reports violations
on `task_run_logs` and only `task_run_logs` — krons 10, sloth 32, marinade 66.
Identical counts before and after `0031`, so this is pre-existing and unrelated
to the re-key; recorded here rather than fixed (record-don't-fix).

The rows point at a parent that no longer exists (the FK was added by a later
migration than the data, or a task delete did not cascade). Not currently
harmful: routd opens `routd.db` with `_pragma=foreign_keys(on)`, and SQLite does
not re-validate existing rows — only new writes are checked. It becomes harmful
the moment any migration rebuilds `task_run_logs` or its parent the way `0031`
rebuilt `installed_packages`: a rebuild under FK enforcement can fail outright or
silently drop the orphans.

The count growing with instance age (10 / 32 / 66) suggests an ongoing producer,
not a one-off backfill.

- **Severity:** low today, latent trap for the next `task_run_logs` migration
- **Scope:** routd.db `task_run_logs`
- **Affected:** krons (10), sloth (32), marinade (66)
- **Source:** `PRAGMA foreign_key_check` on read-only `.backup` copies, 2026-08-06
- **Status:** open — needs the producer identified before deciding delete-orphans
  vs relax-the-constraint. Do NOT rebuild `task_run_logs` until then.

## C9 — queue replay opens circuit breakers before runed is ready (2026-07-30, proposed)

On every full-stack restart, routd begins replaying pending group work before
runed listens on `:8080`. The three immediate `connection refused` attempts
consume each group's breaker budget, so healthy queued work remains blocked
after runed becomes ready. Live krons reproduced this fleet-wide at
07:49:05–07:49:08; restarting only routd after runed was healthy cleared it.

- **Severity:** high
- **Scope:** routd queue replay readiness / runed client retry
- **Affected:** every instance with pending work during restart
- **Source:** krons journal 2026-07-30 07:49:05–07:49:08
- **Status:** proposed (dispatch-readiness redesign, needs sign-off)
- **Fix:** gate queue replay on runed readiness, or keep transient connection
  failures outside the breaker budget until the execution plane is ready

## T1 — PROPOSED, needs sign-off (verified still open 2026-08-15) — stalled typing indicator on timed-out/errored turns (2026-07-26)

Symptom (krons, telegram:user/1112184352, 2026-07-26): the agent shows "typing…"
for ~20 min then goes silent with no reply. Recurring; the user recognises it as
"the typing indication again".

Root cause (config + missed clear, one concern — the turn-end typing-clear is
unreliable and the backstop can't cover for it):
- Container RunTTL = 20 min; in-container query timeout = RunTTL−30s = 19.5 min
  (`runed/docker.go:147` `queryTimeoutMs`).
- Typing backstop `chanlib/typing.go:14` `DefaultTypingMaxTTL = 20*time.Minute`
  — **equal to RunTTL**, so it never fires before the run is already SIGKILLed.
- Event-driven clear is skipped on the failing path: `dispatch.go:270` clears
  typing only for `!out.Steered`. Follow-up messages that arrive mid-run get
  **steered** into the live container and hand typing-clear to `submit_turn` —
  which never fires when the turn hits the query-timeout wall and its
  graceful-summary fallback itself errors (observed: `Query timeout reached` →
  `Not logged in · Please run /login`, no `submit_turn`, no content). Typing
  refreshes every 5s for the full 20 min, then stops only at the backstop ==
  the container's own death.

Evidence: 4 query-timeouts + 3 `container exited with error` in 12h; last real
bot reply 10:03 while user msgs at 16:11/16:12/16:13 went unanswered under a
live-but-doomed 19.5-min browser-automation turn.

Re-verified 2026-08-15, unchanged: `DefaultTypingMaxTTL` (`chanlib/typing.go`)
is still `20*time.Minute`, `RUNED_RUN_TIMEOUT` (`runed/cmd/runed/main.go`)
still defaults to the same `20*time.Minute`, and `dispatch.go`'s typing-clear
still gates on `!out.Steered` with no run-end reconcile for an orphaned
steered turn (`turns.go`'s `appendAndDeliver` is still the only other clear
site). Not contained — routd turn-lifecycle change across dispatch.go +
turns.go + the chanlib backstop, not one call path — so left unbuilt per the
redesign sign-off gate.

Fix (redesign — needs sign-off; routd turn-lifecycle + chanlib backstop):
1. **Clear typing on every terminal turn outcome, including steered.** The
   steered branch (`dispatch.go` `out.Steered`) marks the turn done but leaves
   typing owned by a `submit_turn` that may never arrive. When a steered turn's
   parent run ends without delivering the steered turn's reply, that turn's
   typing must be cleared by the run-end reconcile, not orphaned.
2. **Make the backstop a real safety net: strictly below RunTTL** (e.g. derive
   it as a fraction of RunTTL, or plumb RunTTL into the refresher), so a stalled
   turn stops "typing" well before the 20-min wall regardless of which clear
   path was missed. A 20-min backstop equal to the 20-min run is not a backstop.
3. **Secondary — fail loud:** the query-timeout graceful-summary path erroring
   with "Not logged in" means the timeout fallback delivers nothing. That resume
   call losing auth is its own bug; the user-facing timeout must still surface a
   notice (dispatch.go:339 `runFailureNotice` covers the non-steered parent, but
   confirm it fires for the steered-follow-up case).

## X2 — the aws-devops product's credential model died with X1 (2026-08-09, proposed)

`specs/17/product-aws-devops.md` sells one headline: each engineer's `AWS_*`
keys reach `aws`/`boto3` **inside the container**, so CloudTrail attributes the
call to that person. `X1` removed the mechanism — `AWS_ACCESS_KEY_ID` is a
capability credential, so it is now broker-only and container env never carries
it. Six claims in that spec and the whole flow diagram assert the removed
behavior.

This is not dead paper. `arizuko create --product aws-devops` is a shipped flag
(`cmd/arizuko/main.go:221`) that seeds `ant/examples/aws-devops/`, whose
`CLAUDE.md:9-12` tells the agent its env holds the triggering user's AWS keys.
After `X1` those vars are always empty, so the product fails silently rather
than loudly — worse than a crash.

- **Severity:** medium (no live instance runs it; blast radius is the next
  operator who passes `--product aws-devops`)
- **Source:** specs/17/product-aws-devops.md:30,34,55,110,144,175;
  ant/examples/aws-devops/CLAUDE.md:9-12; ant/examples/aws-devops/PRODUCT.md:8
- **Status:** proposed — spec marked `defected`, code untouched, awaiting the
  design decision. NOT worked around.
- **Options (sign-off):** (a) host-side SigV4 connector — the AWS call becomes a
  `5/13` shape 1/2 tool, the credential stays on the host, per-operator
  attribution survives, and `registerWithSecrets` finally gets its first
  consumer; (b) an explicit, narrow, signed-off exception letting one declared
  key prefix into spawn env, which reopens exactly the hole `X1` closed and
  should be refused unless (a) is proven impossible; (c) retire the product.
  Until one is chosen, `ant/examples/aws-devops/` must not ship.
- **Proposal note (2026-08-11):** `specs/6/17-egress-credential-proxy.md`
  (rewritten) is the concrete mechanism for (a): iron-proxy's `aws_auth`
  transform re-signs SigV4 host-side from the same credential store —
  configuration, not new crypto code, and `boto3` works unmodified in the
  container. Decision between (a)/(b)/(c) remains the user's.

## X3 — runed gets `SECRETS_KEY` and never reads it (2026-08-09, open)

`compose/compose.go:178` ships `SECRETS_KEY` to runed. Nothing in `runed/` or
`container/` reads it: routd decrypts and sends plaintext in
`RunRequest.Secrets`, and runed has no store. So the decryption key for every
secret in the fleet sits in the env of a daemon that has no use for it, next to
the daemon that spawns agent containers. Found while closing `X1`.

- **Severity:** low (runed is a trusted daemon on the compose network; this
  widens the blast radius of a runed compromise, it does not create one)
- **Source:** compose/compose.go:178; no reader in runed/ or container/
- **Status:** open — record only. Dropping the key is a one-line compose change,
  but it regenerates every instance's `env/runed.env`, and that regeneration is
  exactly what caused the fleet-wide `/login` outage after v0.62. Wants a
  deliberate deploy, not a drive-by.

## M1 — `mcpc` socat-connect form 502s since mcpc 0.3.0 (2026-07-16, open)

The documented ad-hoc MCP-call form for agents — `mcpc connect "socat
UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s` (`ant/skills/self/mcp.md:128`,
`ant/skills/mcp/SKILL.md:24`, `ant/skills/migrate/SKILL.md:277`) — fails on the
in-container `mcpc` 0.3.0 with `Failed to connect to MCP server: Proxy response
(502) !== 200 when HTTP Tunneling`. The socket is healthy (routd logs `"mcp
server listening"`, `srw-rw---- /run/ipc/gated.sock`); it's mcpc's HTTP-tunnel
wrapper regressing on the socat form, NOT routd. Known since a June diary note.

Impact: an agent that must reach a NON-native MCP tool via mcpc gets a hard 502
and (marinade atlas, 2026-07-16) narrated it to a user as a routd outage +
falsely tied it to an unrelated transient Slack-send 502. For the route-token
class specifically mcpc is now never the answer: minting is root-only by grant
(tier-0 `*` set), a genuine `/root` turn gets every socket tool natively
(worker-side steering + elevated row-ACL gate, 2026-07-16), and list/revoke
ride the tier-1/2 defaults — a non-elevated agent that can't see a mint tool
lacks the grant, and mcpc wouldn't change that. Any genuinely-non-native tool
still hits this.

Fix (deferred, operator): pin/patch in-container `mcpc` to a version whose
`connect "socat …"` form works, OR switch the documented form + the three
skills to a working direct-`UNIX-CONNECT` form — verify against a live socket
before touching the skill docs.

**Premise re-verified 2026-08-15 at `bcb5bb83`: still live, still deferred, and
the deferral now has a second reason.** All three documented sites still carry
the failing form verbatim (`ant/skills/self/mcp.md:134`,
`ant/skills/mcp/SKILL.md:24`, `ant/skills/migrate/SKILL.md:277`), plus a fourth
in the frozen migration record `ant/skills/self/migrations/052-mcpc-cli.md:7`,
which is history and must not be rewritten. `ant/Dockerfile:171` installs
`@apify/mcpc` UNPINNED, so the version an image gets is whatever npm serves at
build time — the regression can move under the fleet without a code change, and
pinning is itself the first half of the fix.

Not fixed here, for the reason the entry already gives and one more: the fix is
unverifiable without a live socket (this worktree has none, and a form that
merely LOOKS right is what put the failing one in three skills), and
`ant/skills/` is release machinery — a change there needs a migration file, a
`MIGRATION_VERSION` bump and an ant image rebuild to reach any agent. Shipping
an unverified form into all three skills would replace a known-broken
instruction with an unknown one.

## ✅ FIXED 2026-08-15 M2 — elevated (/root) turns bound by static-tier STRUCTURAL gates — CLOSED-STALE, premise gone

The 2026-07-16 elevation fix widened both halves of the per-tool grant
(`ServeTurnMCP` rules → `*`, `turnAuthorize` → allow-all), but the resource
gates' STRUCTURAL checks still keyed on `auth.Resolve(folder)` — the folder's
static tier, blind to `turnMCP.elevated`. So `/root network_allow(...)` /
`add_acl` / `register_group` from a tier-2 folder still 403'd "tier N cannot ..."
(the rhias operator hit exactly this on `network_allow('rhias/content', …)`).

**Fixed 2026-07-20 (commit d452d6ef, user sign-off):** `turnIdentity(folder,
elevated)` (tier 0 under /root, else the folder's tier) is computed once in
`ServeTurnMCP` and threaded into the five postBuild structural gates —
`network_rules`, `acl`, `groups`, `routes`, `scheduled_tasks`. `network_rules`
was doubly broken (the only postBuild that never took the elevated `authorize`
either) — now routed through the shared `toolGrant`. Operator-gated: cmdRoot
elevates only for `IsOperator(sender)`. Test:
`TestNetworkRulesMCP_RootElevationManagesEgress`.

**Both residuals below are now moot — the whole tier system this entry
describes is gone.** `auth.Identity` (`auth/identity.go`) carries only
`Folder`/`IsRoot`, no tier field; `AuthorizeStructural` no longer exists
(only stale comments reference it). The 5/33 redesign replaced every
static-tier check with `auth.Authorize`/acl grant rows.

- `route_tokens`: `routeTokensHandler` (`routd/route_tokens_resource.go:107`)
  has no `authorizeRouteTokenMint` call left. Containment now runs through
  `routeTokensPostBuild`'s injected Gate (`turnAuthorize`/`turnIdentity`,
  `routd/agent_gate.go`), which IS elevation-aware — `turnIdentity` sets
  `IsRoot` under `/root`, and `turnAuthorize(elevated)` returns an allow-all
  fn when elevated. Mint containment against the target folder rides the
  same uniform evaluator as every other resource, own- or cross-subtree.
- `ipc/ipc.go` `buildMCPServer` (line 957) now takes `isRoot bool` directly
  (`identity.IsRoot = identity.IsRoot || isRoot`), and `authorizeCall`/
  `visible` route through `db.Authorize`/`db.Visible` — the same evaluator,
  not a tier check. The caller (`ipc.go:374`) threads `isRoot` through.
  `fork_topic`, `inspect_messages`, `set_work`, `get_web_presence` are
  elevation-aware via this shared path; no per-tool residual remains.

**Fix:** none needed — closed by the unrelated 5/33 tiers-removal redesign,
verified against `auth/identity.go`, `routd/agent_gate.go`,
`routd/route_tokens_resource.go`, `ipc/ipc.go` at HEAD `0d376586`.

## M4 — `vited` runs the Vite DEV server in production (2026-07-16, partly fixed 2026-07-18)

**Crash class fixed (2026-07-18):** the dev server's dep-optimization cache
defaulted into the bind-mounted `/web` root, where host uid ownership drifted to
root and `vite` (USER node) hit `EACCES: unlink …/.vite/deps/_metadata.json` on a
config change → crash-loop → `/pub` 502 (marinade + sloth, twice). Fixed by moving
`cacheDir` out of the mount to a container-local path (`ant/vite.config.js`) — the
cache is now container-owned + ephemeral, so it can't recur. Immediate incident
patched by chowning the stale `/web/**/.vite` caches back to the container uid.

**Residual (still open):** vited still runs `vite dev` (poll-watch + transform)
rather than serving the static `/pub`+`/priv` tree — a steady ~1-core cost even
with the scoped watcher. The clean end-state is a static serve (`vite preview` or
a plain file server) keeping the pub-fallback/trailing-slash middleware; deferred,
needs sign-off.

## R2 — Reserving `#announce` silently steals an existing route topic (2026-07-15, proposed)

Before v0.59, every fragment other than `#observe` was a valid pinned topic, so
`folder#announce` meant topic `announce`. The new parser silently reinterprets
that same persisted value as announcement metadata and clears `Topic`; inbound
messages now run in the root topic. No migration detects or rewrites existing
collisions. Parser-produced values otherwise round-trip correctly, and the
`Mode > Announce > Topic` `String` precedence is safe because parsing never sets
more than one field.

- **Severity:** medium
- **Scope:** route-target backward compatibility
- **Affected:** any pre-v0.59 route whose pinned topic is literally `announce`
- **Source:** core/types.go:94-135; routd/loop.go:630-646
- **Status:** proposed
- **Fix:**

## Release announcement is over-delivered: every group blasts all its routes, once per server-per-group — no opt-in, no main-channel, no mute (2026-07-14, OPEN, proposal)

The `/migrate` skill step (e) (`ant/skills/migrate/SKILL.md:224-289`) is run
per-group by each behind group's own agent. It sends the release line to EVERY
non-web route of that group (skips only web/slink/wildcard), deduped only per
telegram-JID / slack-team / discord-guild, guarded by a per-group
`~/.announced-version`. Two symptoms:

1. **Per-group dedup, shared destination.** N groups routing into the same
   Discord server / Slack workspace / Telegram chat each announce once → the
   server sees N copies, one per group, each in that group's first-`seq` route.
2. **No opt-in / no main-channel / no mute.** All routes are targets; the
   within-server dedup lands in an arbitrary (`seq`-first) channel, not a
   designated #general/#random. Routes table (`store/migrations/0022`) has no
   announce/mute column.

Desired: announcements are opt-in per channel, default one main channel per
server, explicitly mutable, and on discord/slack land only in the operator's
chosen (#random) channel — each server sees it exactly once.

**Fix direction (record-only, needs sign-off — redesign):** make announce
opt-in. Candidate mechanism = an `#announce` fragment on the route target
(mirrors the existing `#observe` fragment, `store/migrations/0054`); step (e)
sends ONLY to `#announce`-tagged routes, deduped per server; no tag → no
announce (mute = remove the tag). Operator tags exactly one channel per server
→ naturally once-per-server even across groups. Alternative: a small resreg
`announce_targets` resource (heavier; the "every management entity is a resreg
resource" rule favors this if it grows beyond a flag). Related but opposite
symptom to the "broadcast silently lost / no channel for jid" entry below —
both need the announce target-resolution audited together.

- **Severity:** low-medium (noise/duplicate pings, no data loss)
- **Status:** IMPLEMENTED (code+skill, 2026-07-14), pending release plumbing +
  deploy. Mechanism signed off = `#announce` route fragment. `ParseRouteTarget`
  (`core/types.go`) reserves `announce` (inbound-neutral); `router.Describe`
  annotates `announce`; migrate skill step (e) sends only to `#announce`
  routes, deduped per server (web/slink/wildcard skipped). To reach live
  agents: bump `MIGRATION_VERSION` + rebuild ant image + redeploy. Residual:
  cross-group global dedup is still per-group — two groups tagging the same
  server double-post (operator-controlled: tag one channel per server).

## L23 — PROPOSED, needs sign-off (verified still open 2026-08-15) — a dead-lettered message is countable but not readable (2026-08-11)

Premise re-verified at this tree: `dashd/routd_page.go` still only shows a
`dead-lettered` COUNT; grepped `resreg/` for any dead-letter resource —
none exists. Still true: an operator can tell messages were lost but not
which ones, to whom, or with what content, and nothing can replay them.

Not contained — this is a new resreg `Resource` (REST + MCP, per the
CLAUDE.md "every cold-tier management entity is a resreg resource" rule),
not a one-file fix, so it needs the redesign sign-off this pass doesn't
have authority to give. Recommended shape, unchanged from the original
report: `messages WHERE status='failed' AND is_bot_message=1` is already the
durable, identifiable row set — read-only surface, no new state.

L22 (this pass) added the one piece this needs that didn't exist yet:
`delivery_attempts` is now readable (`core.Message.DeliveryAttempts` via
`msgReadCols`/`scanMessages`), where before it was write-only. The
resource itself — registration, REST handler, MCP annotation, a replay
action — is still undone.

- **Severity:** medium — the recovery path the dead-letter state exists for does not exist.
- **Scope:** dead-letter visibility
- **Affected:** dashd, routd, resreg
- **Source:** `dashd/routd_page.go`
- **Status:** open

## L16 — legacy plaintext secret rows are never sealed; the migrator has no caller (2026-08-11, open)

`store.EncryptPlaintextSecrets` exists to seal pre-encryption rows in place, and
it has **zero non-test callers** — it is wired to no startup path, so it has
never run on any instance.

This matters more since `S2`: requiring the key stops NEW plaintext writes, but
every row dashd already wrote in plaintext stays that way on disk. `S2` closed
the leak; it did not clean up what leaked. A reader still decrypts those rows
correctly (`open` falls back for un-prefixed values), so nothing is broken —
they are simply unencrypted at rest, which is exactly what `5/14` promises they
are not.

Wiring a write-migration into boot is a behavior change on a live table, so it
is recorded rather than done.

- **Severity:** medium — a real at-rest exposure, bounded to rows written
  before the fix
- **Scope:** secrets at-rest migration
- **Affected:** store, routd
- **Source:** `store/secrets.go` `EncryptPlaintextSecrets`
- **Status:** open — needs sign-off on where the migration runs.

**Premise re-verified 2026-08-15 at `bcb5bb83`, and it holds exactly.**
`EncryptPlaintextSecrets` has two callers and both are in
`store/secrets_test.go`; no daemon boot path, no CLI verb, no migration
references it. It is correct code (idempotent, `WHERE value NOT LIKE 'v2:%'`,
re-seals in one transaction, no-ops without a key) with nowhere to run.

**PROPOSED SHAPE — an explicit operator verb, NOT a boot hook.** Recommending
`arizuko secret seal-legacy <instance>` (a `--dry-run` counting rows first)
over wiring the call into routd's boot after `SetSecretKeys`, for two reasons
this file already paid for:

- The function it REPLACED, `PurgeUnencryptedSecrets`, was a boot-time mutation
  of this same table, and it wiped every secret on each startup because writes
  never wrote the prefix it keyed on (recorded in its own doc comment). A
  second boot-time in-place rewrite of `secrets.value` is the same shape as the
  outage.
- `derived_vs_stored_migration` is the standing lesson: a data migration whose
  rehearsal matches 0 rows is a red flag, not an all-clear. An operator verb
  reports the count before and after; a boot hook runs silently on three
  instances at once with the daemon's exit code as its only signal.

Blast radius if wrong is total — this rewrites the column every credential
lives in — so it does not ship inline, and it wants a rehearsal against a
read-only `.backup` copy of each instance before the real run.

## Web docs claim URL↔kind binding on route tokens; shipped code accepts any token at either URL (2026-07-12, fixed, LOW)

**Location**: `template/web/pub/arizuko/reference/tokens.html` §"/chat/ and /hook/ — each URL bound to its JID prefix kind" (+ echoes in `concepts/tokens.html`, `reference/schema.html`, both howto ledes)

The docs say "a hook: token at /chat/… returns 404; a web: token at /hook/…
returns 404 — binding enforced at token lookup". The shipped contract is the
opposite: spec 5/W §"URL routing" locked "both URL prefixes accept ANY valid
route_token; kind is metadata, not a URL access gate", and
`webd/route_token.go` implements exactly that (lookup does not filter on JID
prefix). Also spec 5/W §"Where this runs" still describes the old filtered
lookup — internal spec inconsistency. Fix: align the four web pages + that
spec section to the any-token contract (docs-only).

- **Status:** fixed 2026-07-13 — all four pages + spec 5/W (lede, "Where this
  runs", Tests) state the any-token contract. Same pass trued
  `reference/tokens.html`'s endpoint table to the shipped handlers (no GET
  `/hook`, no hook SSE row; POST `/hook` → 204) and removed its residual
  headers-delivery claim (same drift the fixed howto/webhooks entry removed —
  ingest forwards body only). `legacy/` snapshot pages left as-is.

## System-turn / broadcast delivery targets a folder-jid or prefix-less room → "no channel for jid" → the broadcast is silently lost (2026-07-13, OPEN)

From the "chats stop responding" eval. System/auto turns (the auto-migrate
release broadcast; some proactive turns) call `deliver.Send(jid, …)` with `jid`
set to the **group folder** (`main`, `atlas/support`) or a **prefix-less/stale
room** (`user/137865889`) instead of the group's actual routed **channel** jid.
`chanDeliverer.resolve` (`routd/deliver.go:78`) maps a jid → adapter via
`lookupSource` (the adapter that delivered the jid's newest inbound); a folder or
a prefix-less room has no inbound source → nil channel → `no channel for jid …`
and the message is dropped. Live: marinade `user/137865889` during
`auto-migrate-atlas/martin` (release note never delivered); sloth `jid:main` on
restart. **Impact:** the migration/announcement broadcast silently fails to reach
users on affected groups; error-spam in logs (51 hits/2d). NOT a user chat dying
(distinct from the stale-session fix). **Fix direction (record-only):** system-turn
delivery should resolve the group's bound channel jid (the routes-table channel /
newest routed inbound), not the folder; or skip+debug-log when a folder has no
bound channel instead of ERROR-spamming a doomed `Send`. Needs the broadcast/send
target-resolution audited — design, not a one-liner.

**v0.59 confirmation:** the new opt-in path still has this exact prefix-loss.
Normal CLI-created routes use `room=` plus `core.JidRoom(jid)`, so the migrate
script strips `room=` and passes values such as `T/channel/C` or `group/42` to
`send`. Adapter ownership requires the original `slack:`/`telegram:`/`discord:`
prefix, and routd returns `no channel for jid`. A normal `room=` route tagged
`#announce` therefore cannot announce. Source: `cmd/arizuko/main.go:403-425`,
`ant/skills/migrate/SKILL.md:280-295`, `chanreg/chanreg.go:41-47`,
`routd/deliver.go:112-120`.

- **Severity:** medium (announcements silently lost on some groups; log noise)
- **Status:** OPEN, record-only.

## Cost attribution is folder-only — per-chat lost, per-user auth-only, plus a duplicate cost_log (2026-07-13, OPEN)

Question raised: is LLM cost attributed completely to individual chats + users? **No.**
- **Cost → folder: yes.** `recordTurnResult` writes `cost_log(folder, turn_id, model, …)` once per model (`routd/turns.go:684-689`; schema `routd/migrations/0001` + `0011-cost-log-user-sub.sql`).
- **Cost → chat: NO (derivable, unwired).** `cost_log` has no `chat_jid`. The turn's `chat_jid` is on `turn_context` (same `turn_id` PK) but nothing joins it — not `CostByTurn` (`routd/db.go:1104`), `/v1/cost` (`routd/reads_http.go:299`), or dashd. A folder with many chats collapses to one folder total. **Min fix:** add `chat_jid` to `cost_log`, set from `tc.ChatJID` at the one write-site (`turns.go:685-689`).
- **Cost → user: PARTIAL.** `userSub := callerSubOfMsg(tc.Trigger)` resolves only `google:`/`github:`/`local:` senders (`routd/budget.go:9-23`); native adapter senders (`telegram:user/…`, WhatsApp, Slack) → `""` → folder-only. Most chat traffic has no user dimension. **Decision needed:** widen `callerSubOfMsg` to key on the raw adapter sender, OR document per-user cost as auth-only by design (currently reads as a bug, not a decision).
- **Duplicate cost_log (no-duplication violation).** A second, structurally different `cost_log` exists in `store/` (`store/cost_log.go` + `store/migrations/0049-cost-log.sql`, cols `ts/cache_read/cache_write`, no `turn_id`) with its own `LogCost`/`GroupUsageBulk` (dashd usage reads this one). Two cost-recording paths that will drift — consolidate to one owner.
- **Reporting:** dashd usage + `GroupUsageBulk` slice by folder only (`dashd/usage_page.go`, `store/cost_log.go:119`); `/v1/cost` is per-turn only. No per-chat or per-user rollup surface exists.

- **Severity:** medium (billing/showback can't split by chat or by unauthenticated user)
- **Status:** OPEN — chat gap is a 1-column + 1-write-site fix; user gap needs a design call; the duplicate cost_log needs sign-off (schema consolidation).

## Codex mount assumes creds readable by the container uid — 0600 operator creds break codex-in-container (2026-07-12, unblocked live, DURABLE FIX OPEN)

`container/runner.go:582-590` RO-mounts `HOST_CODEX_DIR/{auth.json,config.toml}`
into the agent container, which runs as uid 1000 (`mivu`). But those files are
the operator's personal codex creds under `/home/onvos/.codex`, owned
`onvos:1001` mode `0600` → uid 1000 gets `Permission denied` on `config.toml`,
so the in-container `/oracle`/codex path dies (looks like "not logged in", is
actually a uid mismatch). Live on krons 2026-07-12.

- **Unblocked live (fragile):** `chmod o+r /home/onvos/.codex/{auth.json,config.toml}`
  (setfacl not installed → couldn't scope to just uid 1000). uid 1000 now reads
  them; next fresh container's codex works, no restart. **Two caveats:** (1)
  world-readable now exposes the operator's ChatGPT OAuth token to any local
  user — acceptable on a single-operator box, tighten with an ACL (`apt install
acl`; `setfacl -m u:1000:r …`) if the host gains other users; (2) if onvos's
  host codex REWRITES these files (refresh/config change via atomic rename), the
  `o+r` is lost and it breaks again — re-apply, or ship the durable fix.
- **Durable fix (code, needs sign-off):** don't RO-mount the 0600 originals;
  at spawn, COPY `auth.json`/`config.toml` into the per-group writable `.codex`
  dir (`runner.go:583`, already chowned to the run uid) so the container reads
  its own uid-1000 copy. Keeps operator creds untouched, survives host-side
  rewrites, no world-read. Redesign of the codex mount → record + sign-off.

## OPERATIONAL 2026-08-15 — Admin cannot log in to the krons dashboard (2026-07-12, HIGH, config-not-code)

Reported: an admin cannot get into krons (`/dash`/`/auth`). Facts gathered:
`auth_users` in krons `auth.db` has **0 rows**; krons `.env` and
`docker-compose.yml` carry **no OAuth provider config** (no `GOOGLE_CLIENT_ID`/
`_SECRET`, no `*_ALLOWED_EMAILS`) — only `AUTH_SECRET` + `AUTH_BASE_URL`. So
either the login flow has no identity provider to authenticate against, or a
successful login isn't being recognized as operator (operator status is a grant
check — `proxyd/main.go:678-681`, and durable once the acl table has a row per
`main.go:236`). No `/auth` deny lines in journalctl (the 401 spam is the known
whapd re-pair loop, unrelated). **Suggested fix (root-cause first, do NOT hack):**
trace the `/auth/login` → callback → `requireAuth` path on krons: (a) is a
provider configured at all — if not, that's an operator config gap (document the
exact `.env` keys), not a code change; (b) if login succeeds but `/dash` 403s,
the first operator needs a bootstrap grant (`**` acl row) — confirm how the
first operator is meant to be seeded and whether that path is broken. Fail loud
on the real cause; if it's config, surface the missing keys rather than
inventing a fallback.

- **Severity:** high (operator locked out of their own instance)
- **Scope:** proxyd/main.go requireAuth + /auth flow; authd; krons `.env`
- **Status:** config-not-code (root-caused 2026-07-12). **Every server-side hop
  verified working on live krons; no code change.** Corrections to the facts
  above: krons `.env` DOES carry an OAuth provider — `GITHUB_CLIENT_ID` +
  `GITHUB_CLIENT_SECRET` (the original check greped only `GOOGLE_*`), and
  compose delivers them (`env/authd.env`). Verified live: `/auth/login` 200
  renders the GitHub button; `/auth/github` 307s to GitHub authorize with a
  valid client_id + PKCE state; GitHub shows the normal sign-in (no
  redirect_uri-mismatch banner → the app's callback URL is registered
  correctly); `/auth/github/callback` is mounted (403 `invalid state` on a
  bogus probe); proxyd has `AUTHD_URL` so the cookie→`/v1/refresh`→JWKS chain
  (`tryRefreshViaAuthd`) is wired; and the first-operator grant is ALREADY
  seeded: routd.db `acl_membership: github:kronael → role:operator` +
  `acl: role:operator | * | ** | allow` (migration-0053). What's actually true:
  **no login has EVER completed** — `auth_users` = 0 rows, `refresh_tokens` =
  0 rows, zero `/auth/github/callback` hits in 11 days of journal. The flow
  dies at the human step, not in the platform. **Exact operator actions:**
  (1) open `https://krons.fiu.wtf/dash/` → redirected to `/auth/login` →
  "Continue with GitHub" → sign in to GitHub as **kronael** (the seeded
  operator sub) → operator scope resolves and `/dash` opens. (2) If the
  admin's GitHub account is NOT `kronael`: log in once (creates the
  `auth_users` row), then seed the grant:
  `arizuko grant krons github:<login> '**'` (→ `role:operator` membership,
  cmd/arizuko/main.go:532). (3) If Google login is wanted (as on marinade):
  add `GOOGLE_CLIENT_ID` + `GOOGLE_CLIENT_SECRET` (and optionally
  `GOOGLE_ALLOWED_EMAILS`) to `/srv/data/arizuko_krons/.env`, then
  `sudo systemctl restart arizuko_krons`.

  **2026-08-15 re-check:** OPERATIONAL — a live-instance login attempt, not
  a repo-verifiable claim. Operator: run `sudo sqlite3
  /srv/data/arizuko_krons/store/authd/auth.db "select sub, created_at from
  auth_users;"` to confirm whether a login has since completed; if still 0
  rows, follow the three numbered actions above. No further code change.

## 5/16 invites fold — agent-forwarder half deferred (2026-07-07, by design)

onbod's `/v1/invites` REST face is folded onto resreg (`154cd17f`, mirrors the
gates fold): create/list/revoke ride the shared handler + one tx + audit, and
invites is on onbod's `/openapi.json`. The AGENT invite tools (`invite_create`/
`invite_list`/`invite_revoke`, ipc/ipc.go registerRaw → GatedFns → routd/mcp.go
→ httpOnbod federation) are deliberately NOT folded yet — same precedent as the
route_tokens operator twin left hand-rolled where the shape doesn't fit resreg.

Blocker for a mechanical forwarder fold (Store nil → forward to onbod): the agent
face carries three things a single HTTP forward can't express —
- **caller-bound issuer** (`issued_by="agent:"+folder`, never a client arg),
- **accept_url synthesis** (routd computes `AcceptURLBase + /invite/<token>`),
- **revoke ownership as list-then-delete** (routd/mcp.go:141 lists THIS folder's
  invites and confirms the token before calling onbod's token-only DELETE — the
  comment: "an agent must not revoke another folder's invite even though the wire
  call would"). A naive forwarded DELETE `/v1/invites/{token}` REGRESSES this into
  a cross-folder revoke.

Safe fold needs onbod's DELETE to gain an `issued_by` scope (`DELETE ... WHERE
token=? AND issued_by_sub=?`) so the forwarder passes the caller-bound issuer and
onbod enforces ownership in one call; create needs the forwarder to inject issuer
+ synthesize accept_url. That's the "one-owner + federation" design step, not a
mechanical fold — left for a focused change on the live agent surface.

## 5/16 two-faces rollout — status (updated 2026-07-06)

All 5 cold-tier agent-MCP faces ride one resreg.Resource via the injected Gate
seam. REST faces folded onto their shared handlers: web_routes (27537500 +
9d649e59 list-all leak fix), acl (44c53cef, closed a cross-folder scope hole),
routes (3195f867), tasks (0b6ca53e). Open:

1. **routes/tasks REST bake the agent tier model in the handler — two failure modes
   (being fixed by the containment decouple).** These handlers run
   `auth.AuthorizeStructural(auth.Resolve(Caller.Folder), …)` — a predicate written for
   the agent tier identity — against a REST operator whose real authority is `ownsFolder`
   (own-or-descendant, tier-independent). The two diverge in BOTH directions:
   - **routes — over-restrictive (no leak).** A tier-1+ REST token managing its OWN
     folder gets 403 (tier-1 needs a STRICT descendant); a tier-2 REST token can't manage
     routes at all. Latent (existing tests use tier-0 folders where the tier cap is a no-op).
   - **tasks — LIVE CROSS-TENANT LEAK (security).** `tasksRESTGate` keys `ownsFolder(jwt,
     Caller.Folder)` where `Caller.Folder==jwt` for per-task ops → a no-op; the only per-task
     check is the handler's tier cap, which is LOOSER than ownsFolder: a **tier-0 REST
     operator can `DELETE /v1/tasks/{anyId}` and cancel ANOTHER tenant's task**; a tier-1
     operator can cancel/patch any same-world sibling's task (`isInWorld`). Uncovered
     (`TestRESTTaskScopedSelfService` uses tier-2, where exact-own ≈ ownsFolder). Present on
     main since the tasks fold (0b6ca53e).
   **RESOLVED 2026-07-06 (0d25b687).** Decoupled containment into a routd-internal
   per-face `containFn(caller,action,target)` — agent→tier `AuthorizeStructural`,
   REST→`ownsFolder` — dropping the baked tier check from the handler (resreg untouched;
   set_routes keeps its face-agnostic `routeTargetWithin` loop). TDD'd: the two tasks
   guards returned 200+deleted pre-fix (leak confirmed), 403 post-fix. Guards:
   `TestRESTTask{Tier0NoCrossTenant,Tier1NoWorldLeak,Tier2Descendant}`,
   `TestRESTRoute{Tier1OwnSubtree,Tier2OperatorOwnSubtree}`. Agent tier semantics
   (`TestRoutesMCP_*`/`TestScheduledTasksMCP_*`) unchanged.
1b. **scheduled_tasks REST (`/v1/tasks`) not OpenAPI-discoverable (minor, follow-on).**
   OpenAPI truthfulness shipped (7c14efd6 — docs emit real Endpoints, no phantom paths).
   But `scheduled_tasks`'s REST face is mounted at `/v1/tasks/{taskId}` (`mountTasks`
   override) with a DIFFERENT action set (list/get/patch/cancel) than its agent tools
   (schedule/pause/resume/cancel/list), so it can't share the resource's registered
   `ScheduledTasksEndpoints` (`/v1/scheduled_tasks`, agent-derivation) — and advertising
   the resource would emit the wrong paths. So it's correctly LEFT OUT of routd's
   `/openapi.json` list (`[routes, web_routes, acl]`). Truthfully advertising `/v1/tasks`
   needs a separate REST resource declaration for the tasks operator face — a follow-on,
   not a phantom.
2. **network_rules: already clean, out of scope.** Its `AuthorizeStructural` lives in the
   injected Gate (network_rules_resource.go:187), the handler is auth-agnostic, and it has
   NO REST twin — the model the decouple brings routes/tasks to. Adopt the same containFn
   only if/when a REST face lands.
3. **Tool-browser drift — RESOLVED 2026-07-11** (df9ebad3 single-sources facade-tool
   MCP metadata; d5023c60 `ipc.ListTools` renders grant-visible cold-tier facade tools
   for dashd; `facade_tools_test.go`). `dashd`'s schema browser now shows the migrated
   cold-tier facade tools. Verified this pass (`ipc/ipc.go:2263`).
4. **`container/runner.go` standalone ServeMCP (minor).** The non-split dev path
   (`!ExternalMCP`) gets no postBuild → no facade tools there. Production (split, routd
   hosts the socket) unaffected.
5. **surrogate refresh: bodyless-4xx nulls the row (5/15).** RESOLVED 2026-07-07 (e813efd5):
   `Engine.Refresh` now signals `ErrReconnect` ONLY on a definitive OAuth error body
   (`tr.Error` set); a bodyless/unparseable non-2xx is a transient error that keeps the
   credential. Test `TestRefresh_BodylessErrorIsNotReconnect`.
6. **flaky test: `TestRefreshRotationRaceSingleWinner` (authd/bugfix_test.go:131).** A
   refresh-rotation concurrency race test that intermittently fails under full-suite parallel
   load but passes 5/5 in isolation. Pre-existing, unrelated to the 5/16/5/15 work. Needs a
   sync point or serialization in the test harness. Low priority (not a product bug).

## ConnectorSecrets resolves folder scope only — user BYOA key never reaches MCP subprocess

**Severity**: medium
**Found**: 2026-06-26
**Location**: `routd/sibling_db.go:ConnectorSecrets` (calls `FolderSecrets(folder)`, not `FolderSecretsForUser`)

A user who sets `GITHUB_TOKEN` in their user-scoped secrets (via `/dash/me/secrets`)
expects it to be used when they invoke a GitHub MCP connector tool. It isn't.
`ConnectorSecrets` reads folder scope only — the user-scoped override is invisible
to the connector subprocess.

Fix requires:
1. `sibling_db.go:ConnectorSecrets(folder, callerSub string)` — add `callerSub` param,
   call `FolderSecretsForUser(folder, callerSub)` instead of `FolderSecrets(folder)`.
2. Thread `callerSub` (the turn's trigger sender) into `ipc/ipc.go:1027` where
   `db.ResolveConnectorSecrets(folder, ...)` is called.
3. `routd/mcp.go:569` pass `callerSub` alongside `folder` into the resolver.

Spec: `specs/5/14-credentials.md § ConnectorSecrets user-scope`.

- **Status:** fixed 0d244973 (2026-06-26) — ConnectorSecrets now takes callerSub, calls FolderSecretsForUser

## dashd reads SQLite directly — spec 7/1 (formerly 6/1) says it shouldn't; code and CLAUDE.md say it should (2026-06-16, PROPOSED — spec/code conflict, needs sign-off, verified 2026-08-15)

`dashd/main.go` opens `routd.db` (`dbRoutd`), `onbod.db` (`dbOnbod`) and `runed.db`
(`dbRuned`, via `openSiblingOwner`) directly and queries them from most page handlers
(groups, tasks, routes, activity, secrets, usage). The renamed spec (`6/1` →
`specs/7/1-cockpit-index.md`, still `status: draft`) still says, verbatim: "Every
datum a dashboard shows or mutates flows through the owning daemon's `/v1` surface.
No dashboard — hub or per-daemon — reads a SQLite DB directly. This retires dashd's
current direct reads of routd.db/onbod.db/messages.db."

That is the OPPOSITE of root `CLAUDE.md`'s current "### Split write-discipline":
"FS-mounted daemons (dashd, onbod, CLI) write owned tables directly to the owner's
DB" — and the code comments agree with CLAUDE.md, not the spec: `dashd/main.go:385`
"dbRoutd *sql.DB // routd.db handle — dashd's ONLY message/config store", `:417`
"dashd IS FS-mounted on onbod.db (dbOnbod, for the invites page)". `openSiblingOwner`
(`dashd/main.go:451`) is a THIRD documented pattern: dashd also opens `runed.db`
read-only as a "sibling owner" for dashboard views it doesn't own.

The one page that WAS moved off direct DB reads is `/dash/audit/`
(`dashd/audit_page.go`), whose header comment explicitly frames the old direct-read
pattern as "the recorded defect class that dashd/route_tokens.go already is" and
fans out to each owner's `/v1/audit` instead. So the codebase is not uniform: one
page adopted the spec's contract, most did not, and CLAUDE.md documents the
NON-adoption as the shipped decision.

- **Severity:** high
- **Scope:** dashd, spec 7/1 (draft, never shipped) vs. root CLAUDE.md (shipped)
- **Affected:** all dashd pages (groups, status, invites, memory, tokens, activity, tasks, usage)
- **Source:** dashd/main.go:385-451; dashd/audit_page.go:3-16; specs/7/1-cockpit-index.md:33-61;
  root CLAUDE.md "### Split write-discipline"
- **Status:** PROPOSED, not fixed — this is a spec-vs-implementation conflict, not a
  contained bug: resolving it either means (a) implementing the full `/v1`-only
  read-path for every dashd page the way `/dash/audit/` already does (cross-daemon
  control-flow change, new `/v1` read endpoints per `specs/7/3`/`7/12`), or (b)
  marking spec 7/1's "no direct DB" section declined/superseded and updating it to
  document the FS-mounted direct-read pattern CLAUDE.md and the code already ship.
  Either direction needs a decision, not a docs-only close. NOT fixed this pass.
- **Fix:** needs sign-off — see Status.

## Codex audit 2026-06-18 — dashd full sweep

---

## chat: loadChatSessions limits before filtering; nondeterministic continue links (2026-06-18, PARTIAL — folderSessionTokens half PROPOSED, needs sign-off, verified still open 2026-08-15)

`chat.go:90`: newest 200 rows fetched then filtered in Go — older sessions disappear once table
grows. `chat.go:361`: `folderSessionTokens` infers topic→token by time proximity; two sessions
without an early message produce nondeterministic continue links.

- **Severity:** medium
- **Scope:** dashd/chat.go:90,:361
- **Source:** codex audit 2026-06-18
- **Status:** partial — LIMIT-before-filter half resolved a51989cc (2026-07-05,
  verified 2026-07-13: `folder IN (...)` pushed into SQL before the LIMIT 200).
  `folderSessionTokens` half re-verified 2026-08-15, still open and NOT a contained
  fix: for each `chat_sessions` row (newest created_at first) it takes the first
  `messages` row with `timestamp >= created_at AND topic != ''` as that session's
  topic, first claim wins (`dashd/chat.go:359-391`). Two sessions from the same
  folder created close together can each have a topic-bearing message fall inside
  the other's `>= created_at` window, so the session→topic mapping can attach a
  continue link to the wrong session — a heuristic on message timing, not an
  explicit link. A real fix needs an explicit session↔topic (or session↔turn)
  column persisted at session-creation/first-message time — a schema change to an
  existing table, not a new call path — so per CLAUDE.md redesign discipline this
  is reported, not fixed, pending sign-off on where that column lives
  (`chat_sessions.topic` set once at first message, vs. `messages.session_id`).
- **Fix:** push filter into SQL (done); persist topic-session linkage explicitly (proposed, open)

## PROPOSAL — anteval `--mcp` parity face needs an inspect-read on a public MCP surface (2026-07-11, proposed)

Spec 5/9 gap (b): `anteval --mcp` expects an "inspect-compatible MCP-over-HTTP face" —
`HTTPTarget.McpMessages` just GETs `<mcp>/v1/messages/inspect`, i.e. a REST-shaped URL, not a
real MCP client. The platform DOES have MCP-over-HTTP (webd `POST /mcp` session-auth'd, and
`POST /chat/{token}/mcp` chat-token-auth'd, stateless streamable HTTP) but its tools are
`send_message`/`get_round` — get_round returns ASSISTANT frames for one turn, so the
`rest-mcp-parity` sentinel (harness-injected USER message identical via both faces) cannot be
read through it. Closing the gap honestly needs ONE of: (1) an inspect-read tool on the
chat-token MCP face (new public contract on webd, folder-bound like routd's REST inspect);
(2) a real streamable-HTTP MCP client in anteval driving send_message/get_round and a
reshaped parity case (assert the ROUND's frames match via REST + MCP instead of the raw
sentinel). Both change a public surface contract or the spec's case semantics → needs
sign-off before shipping. Until then `--mcp` stays unset and `rest-mcp-parity` fails loudly
("surface not configured") — honest, not silent; `mcp-roundtrip` is unaffected (agent-driven,
callback-checked).

- **Severity:** low (one non-smoke case ungated; documented honest gap)
- **Scope:** anteval/pkg/run/target.go McpMessages + webd chat MCP toolset + spec 5/9
- **Affected:** anteval `rest-mcp-parity` case
- **Source:** anteval/README.md "Two known gaps"; webd/chat_mcp.go toolset
- **Status:** proposed (redesign, needs sign-off)
- **Fix:**

## krons: `/hook/` proxyd route missing — webhook surface dead; route table not hot-reloaded (2026-07-11, open)

Found by anteval's `webhook-in` live case. `POST /hook/<token>` on krons falls through to the
public catch-all (302 → `/pub/krons/hook/<token>`) for EVERY token — the `proxyd_routes` table
(messages.db) had `/chat/` but no `/hook/` row. `compose.coreProxydRoutes` HAS declared
`{/hook/ → webd, public}` since e2d2b5df (2026-05-18), but proxyd seeds the table only when it
is EMPTY, so instances seeded before that date never gain new core routes — a seeded-once vs
evolving-defaults drift class (same class as MIGRATION_VERSION-style drift, no re-seed path).
Spec 5/W webhooks (`issue_webhook` → `POST /hook/<token>`) are silently dead on krons: the
agent mints a token, the POST 302s, no turn fires.

Second facet: the row was INSERTed live (2026-07-11) and a `/zzztest/` probe row confirmed the
running proxyd does NOT pick up proxyd_routes changes without a restart — despite
`proxyd/resource.go snapshot()` reading the DB per request (spec 5/8, d9796a62). Either the
deployed build predates the per-request read or something still caches; verify on next deploy
and re-run `anteval run ... --case webhook-in` (the `/hook/` row is already in place, so the
next proxyd restart activates it).

- **Severity:** medium (a documented public surface is dead on the flagship instance; silent)
- **Scope:** proxyd route seeding / krons data drift
- **Affected:** krons (and any instance seeded before 2026-05-18); spec 5/W `/hook` surface
- **Source:** anteval webhook-in live run 2026-07-11; proxyd request log `status:302 path:/hook/...`
- **Status:** open (row inserted on krons, awaits proxyd restart; re-seed/upsert path undesigned)
- **Fix:**
- **2026-08-15 re-check:** two separate facets, still both true. (a) OPERATIONAL,
  live-krons-only — verify with `sudo curl -s -o /dev/null -w '%{http_code}\n'
  https://krons.fiu.wtf/hook/<a-real-token>` (should NOT 302 to `/pub/...`) and
  `sudo sqlite3 /srv/data/arizuko_krons/store/proxyd/proxyd_routes.db "select
  path from proxyd_routes where path like '/hook%';"`. (b) code-level design
  gap CONFIRMED still present: `proxyd/main.go:loadInitialRoutes` (`:238-273`)
  still seeds `PROXYD_ROUTES_JSON` into `proxyd_routes` ONLY when the table has
  zero rows ("Persistence wins when proxyd_routes has rows" — spec 6/2
  Phase-3), so any pre-existing instance never gains a newly-added
  `coreProxydRoutes` entry without a manual row insert. A durable fix is an
  upsert-missing-core-routes-on-boot path — new boot behavior, cross-cutting
  (every instance's route table), so this stays a PROPOSAL needing sign-off,
  not a same-session fix.

## Positioning claims "diff, fork, git revert" but nothing versions agent folders (2026-07-14, open)

specs/5/A (grand message, shipped) + specs/17/9 sell ownership as "files you diff,
fork, and `git revert` on your own host". Verified: the platform has NO `git init`
at group creation (onbod/container/cmd grep clean), no auto-commit; live homes show
.git in only 4/8 krons groups + marinade/atlas, and those repos have ZERO commits
(rhias: empty master, 86 dirty files) — agent/operator-initiated shells. So `git
revert` mostly has nothing to revert to. The true claims: plain files, tar = full
backup, git POSSIBLE (ant image ships git + commit skill).

- **Severity:** low (positioning honesty, no runtime impact)
- **Scope:** specs/5/A + 17/9 wording; landing/README ownership sections; the gap itself
- **Fix options:** (a) soften wording to possibility framing (landing sub already
  instructed, 2026-07-14); (b) make it TRUE: `git init` + initial commit at group
  create + a commit hook on /migrate + diary writes — small, but that IS
  specs/9/3-git-as-truth scope creep; decide there, not ad hoc.
- **Status:** open — wording fix in flight; the make-it-true decision belongs to 9/3

## P3 — Proxyd dispatch combines routes from two database snapshots (2026-07-30, proposed)

Route matching reads one snapshot, then proxy selection rereads the table and
rebuilds proxies. A concurrent route mutation can pair old authentication policy
with a new backend, return a false 404, and doubles database/proxy construction
work on every routed request.

- **Severity:** high
- **Scope:** proxyd request dispatch
- **Affected:** requests concurrent with `proxyd_routes` mutation
- **Source:** proxyd/main.go:124-136,625-626,654-660; proxyd/resource.go:60-91
- **Status:** proposed — passing one route/proxy snapshot through dispatch changes
  request control flow and needs sign-off
- **Fix:**

## K1 — Security-sensitive identifier generators discard entropy failures (2026-07-30, proposed)

JWT JTIs, OAuth-state nonces, signing-key IDs, run IDs, and session UUIDs are
returned after unchecked `crypto/rand.Read`. Entropy failure therefore emits
predictable or duplicate identifiers instead of aborting issuance or execution.
PKCE and refresh-token generators already return these errors correctly.

- **Severity:** medium
- **Scope:** authentication and execution identifier generation
- **Affected:** JWT lineage, OAuth state uniqueness, key rotation, run/session identity
- **Source:** auth/es256.go:140-145; auth/oauth.go:160-164;
  authd/server.go:95-101; runed/manager.go:364-376
- **Status:** proposed — correcting helper signatures and propagating errors
  crosses authd/runed contracts and needs sign-off
- **Fix:**

## A4 — ACL read errors are indistinguishable from valid empty policy (2026-07-30, proposed)

Query failures intentionally return no rows and therefore deny authorization,
but the API cannot distinguish that failure from an empty policy. User-scope and
list endpoints consequently return successful empty results. Worse, individual
scan failures are skipped, producing a partial policy that can omit a deny row
while retaining allows.

- **Severity:** high
- **Scope:** ACL storage and authorization evaluation
- **Affected:** agent authorization, login scope snapshots, ACL management surfaces
- **Source:** store/acl.go:176-310; auth/authorize.go:59-99; routd/server.go:506-514
- **Status:** proposed — authorization must remain fail-closed, but
  error-returning store contracts and 5xx management responses require
  cross-component sign-off
- **Fix:**

## Adversarial review of the 5/16+surrogate features — deferred findings (2026-07-30)

Bug-hunt on the newly-shipped features. Fixed inline: F3 (Delegate glob-widening,
cc11eace), F4 (Delegate deny/wildcard, cc11eace), F8 (tier-cap on operator REST
mint, 48c7e5ce), F9 (surrogate over-aggressive credential null, 25e0d91c), F16
(OpenAPI omitted route_tokens, 1f66d77f). Remaining, deferred:

- **F1 (fix before wiring Delegate — phase 3).** `role:operator` grant_option=1 is
  set by an UPDATE in routd/0021 + store/0075, but on a split-migrated instance
  `cmd/arizuko/migrate_split.go` copies `acl` (omitting grant_option) at :344
  AFTER routd.Open runs 0021 at :300 → the root ends up grant_option=0, so
  `auth.Delegate`'s root can delegate nothing once wired. Fresh-install seeding of
  role:operator into routd.db also needs grant_option=1. Latent today (Delegate
  unwired). Fix: seed grant_option on the operator row idempotently at the point
  Delegate is wired, after any copy.
- **F6 (verify before relying on /v1/groups for humans).** `groupsRESTAuthz` wants
  a `routes:read`/`acl:read` resource:verb scope, but human JWTs carry folder-glob
  scopes (`handleUserScopes`) that `auth/scope.go` rejects → the read twin may be
  reachable only by service/CLI tokens (folder="" → unfiltered list-all), and the
  subtree filter (`ownsFolder`) inverts for folder=""/"**". Entangled with the
  tier/scope claim model that `5/33` rewrites — resolve there.
- **F7 (pre-existing).** `DELETE /v1/route_tokens/{jid}` matches one path segment;
  hook JIDs (`hook:acme/github`) and nested `web:` JIDs contain `/` → 404. Fix:
  `{jid...}` catch-all (verify resreg path-param binding) or a query param.
- **F5 (by-design, pre-existing).** Both new REST faces (+the pre-existing acl one)
  are open when `verify==nil` (unset AUTHD_URL = local-dev open). Inert in prod
  (AUTHD_URL always set). Changing the open-when-unverified model needs its own
  sign-off.
- **Surrogate robustness (F10–F14, medium).** F10: misconfig (unknown provider / no
  creds) laundered as transient + near-expiry token dropped not stale-used. F11: one
  malformed operator TOML `os.Exit(1)`s routd (routing outage from an optional
  feature) + a shadowing datadir filename silently inherits the built-in's client
  secret. F12: `Provider.AllowedDomain` parsed, never enforced (false egress
  guarantee). F13: unvalidated `secret_key` collisions overwrite/clobber rows. F14:
  a provider removed from the registry leaves a live, unlistable, unrevocable token.

## 5/33 grant-surface flip — attempted, reverted (2026-07-30, adversary-caught)

The role-based grant flip (`600fc408`) shipped broken; reverted (`0f8d956f`). An
adversarial review (fable sub; codex dead this session) found + reproduced:

- **BUG1 (critical):** the "differential" test drove `SeedFolderGrants` — dead code
  the shipped `deriveFolderGrants` never called. "Proven safe by the differential"
  was FALSE. Lesson: an equivalence test must diff the ACTUAL production function.
- **BUG2 (critical):** `deriveFolderGrants` blind-rebound `folder:<path>` → its
  DEPTH role on every call → restriction-by-role impossible (only widening). The
  "decoupled from location" claim was false. Fix: assign the role once at
  create/invite, never re-derive per read.
- **BUG3 (high, live):** `grants.CheckAction` is last-match-wins; `store.ACLRowsFor`
  has no `ORDER BY`; the `acl_by_principal_action` index returns `folder:` before
  `role:`, so an operator DENY sorted before a role ALLOW and was masked. Confirmed
  repro. Live unmitigated consumer: `container/runner.go:553` `share_mount` RO gate
  (fed from `deriveFolderGrants`, no `db.Authorize` pairing). Fix in the correct
  flip: render denies LAST, or add `ORDER BY effect` / deny-wins to the render.
- **BUG4 (medium):** `SeedTierRoles` is INSERT-OR-IGNORE-only — a tightened tier
  bundle never revokes; stale role rows survive a redeploy. Now moot (unwired).
- **BUG5 (medium, pre-existing):** `dashd/tools_admin.go:36` renders the tool list
  from `grants.DeriveRules` directly — a second sink that drifts from the socket's
  `deriveFolderGrants` (overlay/roles). Violates one-renderer-many-sinks. Predates
  the flip; fold both onto one renderer when the flip lands.

Status: BUG1-3 un-shipped by the revert; deriveFolderGrants back to DeriveRules-base
+ expanded overlay (deny appended last = correct precedence). BUG4 moot until the
flip; BUG5 pre-existing. The correct flip must satisfy all five + the tests the
review named (differential over the REAL path; operator-deny-vs-role-verb; restrict-
via-role; multi-platform dedup; dashd/socket parity; reseed-staleness).

## CEO audit — release-readiness doc gaps (2026-07-31)

Release-ready per the CEO audit after these (docs/positioning, not code):

- **Connector/ext-mcp capability is buried (biggest pitch-vs-capability gap).** The
  ext-mcp connector injection (5/13) — arizuko's actual differentiator — has no
  landing-page or concepts-page presence, only `reference/mcp.html:830-871` + one
  line in `security/index.html:44`. Fix: write `concepts/ext-mcp.html` (connectors)
  + link from landing; add `howto/add-connector.html` mirroring
  `howto/oauth-provider.html`. Half-day.
- **Tier-language doc-debt (NOT stale yet — tracked for the 5/33 rename pass).** ~27
  web pages + root docs say "tier"; today they're still factually accurate (roles ==
  the tier bundles), so NOT release-blocking. But when 5/33 phase (b)
  (AuthorizeStructural→scope) + the tier-scalar deletion land, every "tier"-language
  file must update in one coordinated pass. Files to sweep: `security/index.html`,
  `concepts/*`, `reference/mcp.html`+`grants.html`, root `SECURITY.md`/`ARCHITECTURE.md`/
  `ROUTING.md`, ant self-skills reading `ARIZUKO_TIER`. Owner: the 5/33 rename pass.
- **README/landing polish:** SDK-comparison pitch (`README.md:88-102`) should move onto
  `index.html` (done: README "What's planned" 5/17-shipped staleness fixed 2026-07-31).

## CTO audit — 5/33 partial cutover (2026-07-31)

CRITICAL C1+C2 FIXED by reverting step (d) `edd42181` (commit `3a8b8291`): `egress`/`web:publish`
off `tierOf` onto grants was NOT equivalence-preserving — `container.tierOf` (count+1) and
`auth.Resolve().Tier` (min(count,3), floored to 1) diverge below top level, so 18 live sub-groups
would gain unconstrained egress and `/root` turns silently lost web mounts (`CheckAction(["*"],
"web:publish")` is false — `matchGlob` stops `*` at `:`). Real fix (deferred, needs sign-off):
a reviewed backfill migration writing explicit per-folder `egress`/`web:publish` grants that
reproduce the `tierOf` predicate EXACTLY, then delete `tierOf` — the capability was never
derivable from the tier scalar. H1 (test fixtures) + H2/H3 (atomic seed + loud assign) FIXED.

Open (MEDIUM, deferred — none release-blocking; fail-closed or latent):

- **M3 — folderGrantsFromACLOnly drops ACL row Scope.** `routd/seed_grants.go` renders a
  scoped operator DENY (`mcp:send` on `acme/eng/secret`) as a GLOBAL `!send` for the folder.
  Fail-closed (over-denies), but wrong. The flip made the overlay the whole bundle so it bites
  more. Fix: thread scope into the rule render / evaluate scope in CheckAction.
- **M4 — `/root` gets a READ-ONLY share mount (inverted).** `container/runner.go:554`
  `CheckAction(["*"], "share_mount", {readonly:"true"})` matches (rule `*` has no params) so
  elevated gets RO while tier-1 gets RW. Pre-existing `*`-glob-vs-params gap (same as C2).
- **M5 — no un-assign path.** `store.RemoveMembershipBare` has no caller; `remove_acl` scope `**`
  only deletes `role:operator` edges. The operator-rebind rationale isn't reachable via any tool.
- **M6 — role rows are scope `**`.** Bypasses authorize.go's "tier defaults apply only at own
  folder" guard. Harmless today (every db.Authorize passes scope==socket folder) but the `**`
  scope, not the guard, bounds delegation once `AuthorizeStructural` is deleted in step (b).
- **M1/BUG5 — dashd tools display off DeriveRules.** `dashd/tools_admin.go:36`. The revert
  neutralized the "made-it-live" divergence (egress/web back on tierOf); still a second sink to
  fold when step (e) lands. **M2 self-resolved by the revert** — `SECURITY.md:210` "tier ≤ 1
  egress" is accurate again.

## P2 — three identity-link mechanisms; authd owns two of them (2026-08-02, decided)

"One human, many logins" is implemented three times. Operator decision
2026-08-02: **collapse to one, exposed by authd in the system's canonical way**
(a resreg resource — REST + MCP from one handler, per root CLAUDE.md), with
proxyd and dashd consuming that surface rather than reading a table.

| mechanism | DB | writer | read surface | live rows |
|---|---|---|---|---|
| `identities` + `identity_claims` | auth.db | `store/identities.go:33` | **yes** — `GET /v1/identities/{sub}` (`authd/http.go:384`), consumed by routd's `inspect_identity` | 0 everywhere |
| `auth_users(user_id)` + `oauth_identities` | auth.db | `authd/store.go:122` (the live OAuth login path) | **none** | 1 (marinade) |
| `auth_users.linked_to_sub` | **routd.db** | none (`LinkSubToCanonical` has no non-test caller) | read by `proxyd/main.go:885`, `dashd/profile.go:81` | 0 everywhere |

So authd holds one model that is exposed but never populated, and one that is
populated but never exposed; routd holds a third that is neither. The two
readers in row 3 therefore always get the empty answer — proxyd canonicalises
to the input sub unchanged, dashd renders an empty linked-accounts list.

**Trap worth knowing**: `auth_users` exists in BOTH routd.db and auth.db with
DIFFERENT schemas — `(sub, username, hash, name, created_at, linked_to_sub)`
versus `(user_id, name, created_at)`. Same name, two databases, two meanings.

### Target

1. `oauth_identities` (+ `auth_users(user_id)`) becomes the single model: it has
   the live writer and a first-class person entity, so no sub is "elected"
   canonical and losing one provider account cannot orphan the rest.
   `UNIQUE(provider, provider_sub)` already prevents one login mapping to two
   people.
2. Re-point `GET /v1/identities/{sub}` at it and register it as a resreg
   resource so the MCP twin is derived rather than hand-rolled. The endpoint
   already exists as the seed; today it reads the empty model.
3. proxyd and dashd consume that surface. Note the accepted cost: this puts an
   HTTP hop on proxyd's per-request cookie path, so authd being unreachable
   degrades session canonicalisation — decide the failure mode (fail closed, or
   fall through to the raw sub) as part of the work.
4. Delete `identities`, `identity_claims`, `store/identities.go`,
   `auth_users.linked_to_sub`, `CanonicalSub`, `LinkSubToCanonical`,
   `SubsLinkedTo`, and `dashd`'s `linkedSubs` — additive DROP migrations in
   both schemas.

Deleting before step 2 lands would remove `inspect_identity`'s backing store,
so the order matters.

- **Severity:** medium (account linking non-functional on two surfaces)
- **Source:** `store/identities.go`; `authd/store.go:122`; `authd/http.go:384`;
  `store/auth.go:60,73`; `proxyd/main.go:885`; `dashd/profile.go:81`
### Step 1 SHIPPED (90e46d62)

`GET /v1/identities/{sub}` now reads `auth_users`+`oauth_identities`. The
endpoint had been answering `{identity:null,subs:[]}` for every sub, so routd's
`inspect_identity` reported everyone as unclaimed.

### Step 2 is NOT a deletion — correction to this entry

The old model has two more live surfaces, found while attempting the delete:

- **an operator CLI** — `arizuko identity list/create/link/unlink`
  (`cmd/arizuko/main.go:815-870`), the only way to bind a platform sub to a
  person by hand;
- **`inspect_identity`** (`ipc/inspect.go:112`) reads
  `GetIdentityForSub` straight from the DB, NOT through authd's endpoint — so
  re-pointing the endpoint did not re-point the tool.

So the two models are not redundant in the way this entry first claimed. They
differ by WRITER: `identities`/`identity_claims` is manual and
operator-driven and accepts any sub (`tg:42`); `oauth_identities` is written
only by the OAuth login path (`upsertOAuthUser`) and has no manual entry point.
Deleting the first would remove the operator's ability to link a platform
identity by hand — which is the pairing capability `5/31` is about.

**Revised step 2**: give `oauth_identities` a manual writer, migrate the CLI and
`inspect_identity` onto it, THEN delete `identities`, `identity_claims`,
`store/identities.go`, `linked_to_sub` + accessors, and dashd's `linkedSubs`.
`provider`/`provider_sub` already holds a platform sub fine — the endpoint test
seeds `tg:42` that way.

### Step 2 SHIPPED as a deletion (2026-08-04, fcd845cb + 54125cbd)

The revision above was itself wrong on one point: `inspect_identity` had
already moved. `routd/identity.go` resolves through authd's
`GET /v1/identities/{sub}` (`b6aabfd2`), which step 1 re-pointed at
`auth_users` + `oauth_identities` — so the tool needed no work.

That left `arizuko identity link|unlink` as the only writer, filling a table no
reader ever looked at, which is not a capability to preserve. And the manual
bind it nominally offered is what `5/31` pairing now does with consent and an
inverse. So step 2 landed as a straight delete, no manual writer added:
`store/identities.go`, the CLI subcommand, `linked_to_sub` + `CanonicalSub` +
`LinkSubToCanonical` + `LinkedSubs` + `AuthUserBySub`, dashd's `linkedSubs`,
and migrate-split's copy of the two tables. Dropped by authd `0006` and routd
`0027`.

**Step 3 is what remains** and is an addition, not a deletion: dashd's
"Linked accounts" section was removed rather than left rendering an
always-empty table, so showing a real list means dashd consuming authd's
`GET /v1/identities/{sub}` — including the failure-mode decision this entry
already flagged. proxyd no longer reads `CanonicalSub` at all.

- **Status:** steps 1 + 2 shipped; step 3 (dashd consumes authd's endpoint) open

## P3b — rejected minimization phases, recorded so they are not retried (2026-08-01, closed)

Two phases from the 2026-08-01 minimization plan were rejected on review and
should NOT be reattempted as written:

- **Fold `onboarding.token` into `invites`** — forbidden by `specs/5/31`: an
  invite carries a SCOPE and is issued by a granter; a pairing nonce carries a
  JID and proves chat control. Merging them yields a table whose redemption
  semantics depend on which columns are NULL, with a bug class where a pairing
  token grants. If they ever share storage it must be via an explicit `kind`
  column, never NULL-inference.
- **Drop `onboarding.status` as derivable from its timestamps** — it is not
  derivable, and the fail-loud work (`c6bfe2b0`) added a refusal outcome that
  makes it less so.

- **Status:** closed — recorded to prevent a repeat, no action wanted

## ✅ FIXED 2026-08-15 S1 — retired `CHANNEL_SECRET` still documented as the live adapter credential — re-verified clean; helm split to S1b

`CHANNEL_SECRET` is gone from the code — `chanlib.RouterClient.svcToken` carries
a `service:<daemon>` ES256 JWT on every authenticated routd call, and five call
sites carry the comment "no `CHANNEL_SECRET` remains". Three places still tell a
reader otherwise:

- Root `CLAUDE.md ## Config` lists it as an anchor env var an operator must set.
- `template/web/pub/arizuko/security/index.html` states adapter ingress is
  "authenticated by `Authorization: Bearer $CHANNEL_SECRET`" and lists it under
  operator anchors — this is the **live** security page on krons.
- Two legacy product setup pages emit it in their `.env` sample.

An operator following the security page sets a variable nothing reads, and
believes ingress is guarded by a shared secret when it is guarded by a rotating
signed token pinned to a verified `sub`. Spec-side copies were corrected with
the `5/34` move (`6ae3e954`); these three were left because they are a docs
concern, not a spec one.

- **Severity:** medium (no exploit — the real gate is stronger than documented)
- **Scope:** operator docs accuracy
- **Affected:** `CLAUDE.md`, `template/web/pub/arizuko/security/index.html`,
  `template/web/pub/arizuko/legacy/products/{reality,slack-team}/setup.html`
- **Fix:** replace with `AUTHD_SERVICE_KEY` (the one surviving symmetric
  bootstrap secret) and describe the ES256 exchange; redeploy `/pub`.
- **Status:** docs fixed 2026-08-05; **helm split out to S1b, needs sign-off**.
  - `CLAUDE.md` + `security/index.html` — the two live pages the entry named
    were already done in `d81cc2d2` (2026-08-04).
  - The sweep the entry did not do found the var alive on **17 more live
    pages**, including the one an operator is likeliest to follow:
    `reference/env.html` documented `CHANNEL_SECRET` as "Required in
    production" plus TEN per-daemon `<DAEMON>_CHANNEL_SECRET` overrides, all
    dead (`grep -rn '_CHANNEL_SECRET' --include='*.go'` → 0). Those eleven
    `env-entry` blocks are deleted, not reworded — there is no surviving var to
    redirect them to, and `AUTHD_SERVICE_KEY` already had its own entry.
  - The wording fix is not a rename. The pages described a **symmetric HMAC**
    ("routd verifies inbound HMACs, the adapter verifies outbound HMACs"). Both
    directions are ES256 bearers verified against authd's JWKS: routd requires a
    `service:` subject, and the adapter's `chanlib.authGate` (`chanlib/auth.go:67`)
    requires exactly `service:routd`. Symmetric, but no shared secret exists to
    verify — so the sentence had to be rewritten, not have a token swapped.
  - `howto/index.html` and `reference/cli.html` claimed `arizuko create`
    generates a `CHANNEL_SECRET` into `.env`. It writes `AUTH_SECRET` and
    `SECRETS_KEY` (`cmd/arizuko/main.go:270-284`) and never wrote this one.
  - `template/web/pub/arizuko/legacy/**` deliberately untouched (~60 hits). The
    changelog page calls it "the previous docs are preserved at /legacy/" — an
    archive that also documents `gated` and `emaid`. Editing one var inside a
    snapshot of a retired topology makes it neither the old docs nor the new.
  - NOT deployed: `template/web/pub/` is source-of-truth, the `/pub` deploy is
    the operator's step (`template/web/CLAUDE.md`).

**Re-verified 2026-08-15:** `grep -rn CHANNEL_SECRET --include=*.go .` finds
zero live references (three comments note its retirement); `CLAUDE.md`,
`security/index.html`, `reference/env.html`, `howto/index.html`,
`reference/cli.html` are all clean. S1b (helm `gated` deploy) stays open,
separately, as its own PROPOSED redesign entry.

## S1b — the helm chart deploys the deleted `gated` daemon (2026-08-05, PROPOSED — redesign, needs sign-off)

Split out of S1, whose fix ("wire `AUTHD_SERVICE_KEY` instead of the dead
`CHANNEL_SECRET`") cannot be applied to `deploy/helm` as a config change. The
chart is pre-split and cannot deploy arizuko at all:

- `templates/gated.yaml` runs `command: ["gated"]`. `gated` was deleted in
  v0.50.0 (`45e4ab0d` + `24d57a3e`); it is not in the Makefile's `DAEMONS` and
  there is no `gated/` directory.
- `_helpers.tpl:89-97` points every adapter's `ROUTER_URL` at
  `<release>-gated:8080`, so the whole chart's routing target does not exist.
- There is no `authd`, `routd`, or `runed` template, and **zero** occurrences of
  `AUTHD_URL` / `AUTHD_SERVICE_KEY` / `AUTHD_SERVICE_NAME` anywhere in the chart.

That last point is why this is not S1's one-line swap. Wiring
`AUTHD_SERVICE_KEY` into `adapters.yaml` would inject a credential with no
`authd` to exchange it at and no `routd` to present it to — every adapter would
still `os.Exit(1)` on the required-env check (`chanlib/run.go:51-55`), but the
chart would now *look* current. That is worse than the honest breakage.

- **Severity:** medium (k8s install path is dead; no runtime effect on the three
  compose instances)
- **Scope:** deploy/helm, and `template/web/pub/arizuko/howto/kubernetes.html`
  which documents it (left at `CHANNEL_SECRET` on purpose — the page and the
  chart must move together)
- **Affected:** anyone installing via helm
- **Source:** deploy/helm/arizuko/templates/gated.yaml:57;
  deploy/helm/arizuko/templates/_helpers.tpl:89-97;
  deploy/helm/arizuko/values.yaml:119; deploy/helm/arizuko/templates/secret.yaml:13;
  deploy/helm/arizuko/templates/adapters.yaml:68-75
- **Status:** PROPOSED — redesign, needs sign-off. The real fix is a split-topology
  chart (authd/routd/runed deployments + services, repoint `routerService`, add
  the `AUTHD_*` env, drop `gated.yaml` and `secrets.channelSecret`), or deleting
  the chart if helm is not a supported install path. Do not half-wire it.

## O2 — two adapters down on platform credentials, not code (2026-08-02, open)

Fleet eval + redeploy of all three instances (2026-08-02 15:17–15:20). All units
active, all daemons healthy, and the agent path is verified end-to-end on krons —
a real bot row landed in `routd.db` at `2026-08-02T15:17:49Z`
(`telegram:user/1112184352`), 35s after restart. Two adapters are nonetheless
dead, both on expired platform credentials, both pre-existing and unaffected by
the deploy:

- **krons `whapd`** — `Restarting`, ~65-70s cadence, 30+ restarts observed.
  Logs: `"session invalidated, delete auth dir and re-pair","code":401`. routd
  auto-deregisters the `whatsapp` channel every ~3 failures. Needs the
  `--pair <phone>` pairing-code re-auth, not a restart. A pairing code was
  issued 2026-05-21 and never redeemed, so this has been down ~10 weeks.
- **marinade `emaid`** — `health: starting` indefinitely, `RestartCount=0` (it
  does not crash-loop, so it never gets flagged as failing). Logs:
  `imap idle error … [AUTHENTICATIONFAILED] Invalid credentials` every ~60s;
  routd deregisters the `email` channel every ~3 min. Needs the correct IMAP
  password in secrets.

Both are operator credential fixes, not code fixes — recorded so they stop being
rediscovered by each fleet check. The second one is also a monitoring gap worth
noting: an adapter stuck in `health: starting` forever looks less alarming in
`docker ps` than a crash loop, while being equally down.

- **Severity:** medium (two channels dead; no data loss, no security exposure)
- **Scope:** operator credentials
- **Affected:** `arizuko_whapd_krons`, `arizuko_emaid_marinade`
- **Fix:** re-pair whapd per `reference_whapd_pairing`; set marinade's IMAP
  password. Neither requires a deploy.

## A1 — the split is deployment-shaped only: auth data lives in the router's DB (2026-08-02, proposal)

The daemons look like microservices and are coupled through a shared filesystem.
Three verified facts:

**1. Every daemon mounts every DB.** `compose/compose.go:812`, in the *generic*
`writeSvc` emitter, writes `- <dataDir>:/srv/app/home` for every service with no
per-daemon condition. Verified on live krons: `arizuko_timed_krons` has the
instance data dir mounted `rw`. So CLAUDE.md's "non-mounted daemons (slakd,
timed) write via the owner's HTTP API" describes a boundary that does not exist —
the split write-discipline is a convention, honored voluntarily in code.

Honoring it: `timed`, `davd`, `teled` open no DB at all. Not honoring it:
`proxyd` (routd.db), `webd` (routd.db), `dashd` (routd.db + onbod.db +
runed.db), and `slakd` (`slakd/main.go:44` opens routd.db) — which CLAUDE.md
names as an HTTP writer. `timed` owns no DB because it chose the wire, not
because it lacks access.

**messages.db is no longer in that list (2026-08-05, `e5e75bd5` + `3c2b7ad7`).**
routd's two boot-time legacy copiers are deleted and dashd is repointed, so no
daemon opens the monolith — it was the one file legitimately shared by several
daemons and therefore the stated blocker on per-owner `store/` subdirectories.
That blocker is lifted; the remaining ones are enumerated in Q1 (three CLI verbs
still open it) and the cross-DB reads catalogued above (onbod→routd.db,
dashd→3 DBs), which a per-owner mount would have to keep reachable. The
messages.db FILE stays on all three instances — it holds pre-split history, and
disposing of it is an operator decision, separate from retiring the code path.

**2. Authorization lives in the router.** `acl`, `acl_membership`,
`auth_sessions`, `auth_users` are in `routd.db`. authd owns `auth.db` and holds
`identities`, `oauth_identities`, `refresh_tokens`, `signing_keys`,
`identity_claims`. So the daemon whose entire job is auth does not hold the
authorization rows, and `auth.Authorize` — the sole evaluator — reads them out of
the router's file.

**3. `auth_users` is duplicated across two owner DBs.** It exists in BOTH
`auth.db` and `routd.db`, and five packages write "it": `onbod/main.go`,
`proxyd/main.go`, `authd/http.go`+`store.go`, `dashd/profile.go`,
`routd/budget.go` — each hitting whichever DB its handle opened. Both are empty
on krons today, so this is a latent split-brain, not live divergence. Nothing
prevents divergence the moment both get rows.

This is the root cause of Y1's confusion and of proxyd reading another daemon's
tables: a daemon needing an auth answer must go where the auth rows happen to
sit, which is routd.

**Proposal.** Move `acl`, `acl_membership`, `auth_sessions`, and the single
canonical `auth_users` into `auth.db` under authd. proxyd then answers identity
by offline-verifying authd's ES256 token — the mechanism every other daemon
already uses (`5/1`) — instead of opening routd.db. The one thing forcing a DB
read is the opaque **session cookie**; have authd's login set a JWT-bearing
cookie and proxyd's identity reads disappear entirely. Afterwards, narrow the
mount per daemon at `compose/compose.go:812`, which turns the write-discipline
from a convention into an enforced boundary.

- **Severity:** high (architectural; latent split-brain on `auth_users`)
- **Scope:** cross-daemon schema ownership + container mounts
- **Affected:** `compose/compose.go:812`, `proxyd/main.go:869-890`,
  `slakd/main.go:44`, `store/auth.go`, `authd/store.go`, CLAUDE.md's
  write-discipline paragraph, `specs/5/16` owner-DB map
- **Fix:** needs sign-off — cross-daemon schema move + migration per instance.
  Do not start piecemeal; `5/8` and `5/16` should be rewritten against the
  outcome, not before it.

### The `auth_users` collision decomposes — there is nothing to merge (2026-08-02)

The two tables are not drifted copies of one table; they model different
things and were wrongly given the same name.

`auth.db.auth_users` — `user_id` PK, `name`, `created_at`. The OAuth identity
record, FK'd from `oauth_identities`.

`routd.db.auth_users` — `sub`, `username`, `hash`, `name`, `created_at`,
`linked_to_sub`, `cost_cap_cents_per_day`. Checked column by column:

- **`hash` is dead; `username` is NOT.** CORRECTED 2026-08-02 — the original
  claim here ("username + hash are dead") was wrong, produced by running the
  evidence grep through `| head -6`, getting six test-file hits, and concluding
  from a truncated list. `onbod/main.go:784` writes `UPDATE auth_users SET
  username = ?` inside `createWorldTx`, and `:593` reads it on every
  `GET /onboard` — a failed scan renders "User not found." and returns, so
  dropping the column breaks onboarding for every user rather than degrading a
  dashboard. `hash` is genuinely dead: no `SELECT` anywhere, written only by
  `store/auth.go:29,96` to satisfy `NOT NULL`. Delete `hash`; keep `username`
  and carry it into `user_profiles`.
- **`cost_cap_cents_per_day` is live but is not identity** —
  `store/cost_log.go:87,98` read/write it as a per-user spend cap (the same
  column also exists on `groups`). It belongs on a budget table keyed by sub,
  not on an identity table.
- **`sub`/`name`/`created_at`/`linked_to_sub`** are identity, duplicating
  authd's row.

So the resolution is not a merge: strip the dead credential columns, move the
budget column to its own table, and the remainder consolidates into authd's
`auth_users` — reconciling `sub` with authd's `user_id`. The name collision
disappears instead of being resolved.

**Timing.** CORRECTED — "both tables hold zero rows" was checked on krons ONLY
and generalised. Real counts (read-only):

| instance | `routd.db` `auth_sessions` | `routd.db` `auth_users` | `auth.db` `auth_users` |
| -------- | -------------------------- | ----------------------- | ---------------------- |
| krons    | 0                          | 0                       | 0                      |
| sloth    | 5                          | 3                       | 0                      |
| marinade | 3                          | 2                       | 1                      |

sloth's rows are real Google identities; marinade has `local:admin` plus one
Google row. `cost_cap_cents_per_day` is 0 everywhere. So the migration must
carry data on two of three instances.

### `auth_sessions` is not writer-less either (CORRECTED 2026-08-02)

`routd/db.go:110` `copyLegacyProxydTables` `INSERT OR IGNORE`s into
`auth_sessions` from the legacy `messages.db` on **every routd boot** — that is
production code writing the table, contradicting the claim that nothing writes
it. What does hold: nothing MINTS a session post-split (`CreateAuthSession` has
only test callers), every row is backfill (counts match `messages.db` exactly),
and all rows are long expired — newest `expires_at` is 2026-06-05 — so the
cookie branch still cannot match in production.

**Correct order, which the drop-first plan had backwards:** remove
`auth_sessions` from `copyLegacyProxydTables` first, so the backfill stops and
the rows go inert; then drop the table in a later migration once every instance
reads zero. `specs/5/16`'s own migration gate already forbids dropping while an
instance is non-zero.

Note what that writer implies more broadly: routd still opens the frozen
`messages.db` at every boot to copy legacy rows forward. That is the real
blocker for retiring `messages.db`, and it is bigger than any single table.

## E1 — five adapters keep state in unmounted paths; it dies on every recreate (2026-08-03, open)

Found by the mount audit. `teled`, `bskyd`, `reditd`, `linkd` and `emaid` each
persist state under a data-dir path, and their compose fragments declare
**`volumes:` zero times** — verified on `template/services/{teled,bskyd,reditd,linkd,emaid}.yml`,
and `sudo docker inspect arizuko_teled_krons` returns an EMPTY mount list. The
state lives inside the container layer and is destroyed on every recreate.

| daemon | state | code |
| ------ | ----- | ---- |
| teled  | Telegram update offset | `teled/bot.go:60` (`loadOffset`), written `:66-101` |
| bskyd  | `bluesky-session.json` | `bskyd/client.go:80`, written `:92,95` |
| reditd | `cursors.json` | `reditd/client.go:78`, written `:88,90` |
| linkd  | `linkd-state-<name>.json` | `linkd/client.go:95,122`, written `:143,161` |
| emaid  | `emaid.db` (a whole SQLite DB) | `emaid/store.go:22`, created `:19-38` |

**teled is the user-visible one.** `loadOffset` is
`data, _ := os.ReadFile(b.cfg.StateFile)` — the error is swallowed, so a missing
file silently returns offset 0. Telegram treats `getUpdates` offset 0 as "send
everything still queued" and retains ~24h, so a recreate can re-deliver up to a
day of updates and the agent answers them all again. Silent by construction: no
log line marks the reset.

`emaid` is the structural oddity — it opens a SQLite database at a path nothing
mounts, so it silently starts empty each time.

Note `reditd` and `emaid` are not even given `DATA_DIR`, so their paths resolve
relative to the container working dir.

Correcting a related claim recorded earlier in **A1**: the universal
`<dataDir>:/srv/app/home` rw mount comes from `compose/compose.go:811` in
`writeSvc`, which emits ONLY the eight core daemons (authd, routd, runed, timed,
onbod, dashd, proxyd, webd). The 10 channel adapters plus davd/ttsd/vited are
NOT emitted there — they are standalone fragments included verbatim
(`compose.go:384`), each declaring its own volumes or none. So "one line hands
every daemon every database" is true of the core eight only, and narrowing the
adapters is a per-fragment edit, not one code change.

- **Severity:** high for teled (duplicate agent replies after any recreate);
  medium for the rest (re-auth / re-scan / lost dedup state)
- **Scope:** compose fragment volumes for channel adapters
- **Affected:** `template/services/{teled,bskyd,reditd,linkd,emaid}.yml`,
  `teled/bot.go:60`
- **Fix:** give each fragment the narrow rw mount it needs (`whapd.yml:9` and
  `twitd.yml:9` already show the pattern — a single subpath, not the data dir),
  set `DATA_DIR` where absent, and make `loadOffset` fail loud instead of
  swallowing the read error.

## R1 — codex review of the 2026-08-03 fix batch: six gaps (2026-08-03, 5 of 6 closed 2026-08-15; 1 open on live-state only)

Adversarial review of `50af5f50..e37a084b` (the Y2/M1/E1/D6/D7 fixes). Findings
verified before logging. Ordered by consequence.

**1. CRITICAL — the adapter-mount fix is inert on every live instance.**
`compose/compose.go:383-402` includes each instance's OWN
`/srv/data/arizuko_<inst>/services/*.yml`, not `template/services/`. Confirmed
on krons: that directory holds installed copies ("Converted from
services/teled.toml by `arizuko generate`"), including a second adapter
`teled-rhias.yml`. Editing the template fixes NEW installs only —
krons/sloth/marinade still have no adapter state mounts, so teled still replays
~24h of Telegram updates on recreate. `cmd/arizuko/packages.go:347-375` copies
templates only on `packages add`; there is no update path for installed
fragments. The installed copies are also still emitting the retired
`CHANNEL_SECRET`.

**2. teled's swallow moved from read to write.** The startup read now fails
loud through `chanlib/run.go:90-94`, but `teled/bot.go:92-94` only warns when
the SAVE fails, advances the in-memory offset, and continues. A full or
read-only mount looks healthy until restart, then replays from the last
offset that actually persisted.

**3. The dissolved-tier class is incomplete.** `resreg/resources/route_tokens.go:86`
still tells `issue_webhook` "Tier rules match issue_chat_link", and — worse —
the agent's own global instructions assert tier ceilings at `ant/CLAUDE.md:28`
and `:165-166`. Those reach the model on every turn.

**4. The invite-bearer fix is YAML-only.** `yaml:"-"` does stop `export`
(`resreg/engine.go:359-375`), but `json:"token"` remains, so the live token is
still returned by `GET /v1/invites` (`onbod/admin.go:53-72`), rendered in full
by dashd (`dashd/invites.go:72-84`), printed by the CLI
(`cmd/arizuko/main.go:732-743`), and published in OpenAPI
(`resreg/openapi.go:122-143`). Y2 fixed the manifest path, not credential
exposure generally.

**5. `SkipApplyRebuild` now yields false advice.** `cmd/arizuko/apply.go:165-169`
assumes every skipped resource is a secret and prints "set via `arizuko secret
set`" — nonsense for invites.

**6. The vacuous-target class remains.** `webd/Makefile:10-11` still runs
`-run E2E` with no zero-match guard; only the root Makefile was hardened.

Minimality notes (not defects): `shouldLink` is a one-line wrapper with a
dedicated 21-line test the renderer test already covers; commit `32b180da`
bundles three narrowings with the persistence fix and `e37a084b` bundles dashd
with Make.

- **Severity:** high (1 leaves the reported bug live on all three instances)
- **Fix:** 1 needs an installed-fragment update path — a real `arizuko` command,
  not a manual edit, since it must run on every instance and survive the next
  `generate`. 2-6 are small and independent.
- **Status:** 2026-08-15 — each of the six re-verified SEPARATELY at `bcb5bb83`.
  Five are closed, one (1) is closed in code and unverifiable here. Verdicts:

  1. **Closed in code; live state unverified.** The mechanism claim still holds —
     `compose/compose.go:764` writes `./services/<name>.yml`, i.e. the
     instance's own dir, never `template/services/`. But the update path R1
     demanded now exists as a real command: `arizuko packages upgrade` (one
     package) and `arizuko packages sync` (all) both re-fetch the recorded
     source and rewrite the installed fragment through ONE updater,
     `reapplyPackage` (`cmd/arizuko/packages.go:371`), which refuses to
     overwrite a locally edited asset instead of clobbering it. `template/
     services/teled.yml` carries the `${DATA_DIR}/store/teled` state mount and
     no `CHANNEL_SECRET`. What cannot be checked from this worktree: whether
     krons/sloth/marinade have actually RUN it, and whether hand-made variants
     like `teled-rhias.yml` carry an `installed_packages` record for `sync` to
     reach. Both are live-host questions.
  2. **Closed.** teled no longer swallows the save. `maxSaveFailStreak = 3`
     consecutive `saveOffset` failures now call `fatalExit` (`teled/bot.go`),
     so a full or read-only mount crashes the process instead of looking
     healthy until restart. A single failure is still logged and tolerated,
     which is correct — a transient write hiccup must not crash-loop the poll
     goroutine.
  3. **Closed.** Both halves. `resreg/resources/*.go` contains no tier language
     at all (the `route_tokens.go:86` sentence is gone), and `ant/CLAUDE.md`
     now states the opposite of what R1 found: lines 4, 35 and 173 each say
     "5/33: no tiers" explicitly. Repo-wide, nothing outside this queue's own
     history says "Tier 0-2 only" or "Tier 0-1 only". See `M1` for the last
     residue of this class, fixed in the same pass.
  4. **Closed by a schema change, not by a serializer tag.** `json:"token"` is
     gone because the FIELD is gone: migration
     `store/migrations/0077-invites-hash-at-rest` made `ref` (=
     `hex(sha256(token))`) the primary key, so the raw bearer is never
     persisted and `InvitesRow` has no token field to leak by omission. dashd
     renders `ref`. The only surviving raw token is
     `toInviteCreatedJSON`'s mint-time response (`onbod/admin.go`), which is the
     one moment the caller must receive it.
  5. **FIXED 2026-08-15** — see the commit closing it. It was real: FIVE
     resources carry `SkipApplyRebuild` (secrets, route_tokens, invites,
     onboarding, installed_packages), and `printPlan` printed the secrets
     command for all five.
  6. **Closed.** `webd/Makefile` `test-e2e` captures the output and `exit 1`s on
     "no tests to run", with a comment naming the root Makefile's guard as its
     mirror. A selector matching zero tests can no longer pass as green.

## V2 — dashd duplicates ~4,500 LOC of control planes phase 7 moves away (2026-08-04, proposal)

Codex review of the dashd surface against the post-phase-5 end state. Almost
nothing is *dead* — the identity deletions left only textual rot. The problem is
**duplication**: dashd hosts control planes that `specs/7/3` moves to each
daemon's own `/dash/<daemon>/`.

| target | LOC | why |
| ------ | --- | ---- |
| `chat.go` + test | ~1,019 | duplicates webd's `/me/chats/new`; **records the RAW reusable route bearer** (`chat.go:25,38,450`) |
| routes / route_tokens / errored-retry | ~867 | duplicates routd's planned `/dash/routd/`. The mint form (`route_tokens.go:103`) should not survive anywhere — cockpit may list/revoke, minting belongs to webd and agent tools |
| `runed_page.go` | 526 | reads `runed.db.spawns` directly and proxies kill; both are runed's |
| tasks (`main.go:771`, `tasks_admin.go`) | ~505 | duplicates timed's dashboard, including a SECOND cron parser and next-run computation |
| invites + whapd pairing | ~782 | belong on `/dash/onbod/` and `/dash/whapd/` |
| usage / audit / packages | ~842 | usage reads the frozen `messages.db` (`usage_page.go:38`); audit reads only routd's `audit_log` while claiming to be global; packages duplicates the CLI |

Plus in-page duplicates: the `/dash/` tile portal repeats `/dash/services/`;
`/dash/status/` repeats counts and calls them health while the hub probes real
health; groups repeats usage and route rows; the 53-line tool browser duplicates
MCP `tools/list`.

**`chat.go` is deletable NOW, ahead of the rest.** `chat_sessions` does not exist
on krons, sloth or marinade — the portal has never been used, so there is no data
to preserve and no capability to lose. Deleting it also removes a plaintext
bearer store of exactly the class Y2/I1 just closed for invites.

**Everything else is SEQUENCED, not free.** Deleting dashd's routes page before
routd serves `/dash/routd/` loses the capability. Order: build the per-daemon
page, then delete dashd's copy. That ordering is the whole proposal.

**The one real gap** is not an edge list — it is an **authority-provenance view**.
`grants_admin.go:52` renders only `acl` rows and consumes `acl_membership` merely
as autocomplete (`:92`), so an operator cannot answer "why does this Telegram
account carry Alice's authority?". The view needs child → parent, `added_by`,
role chains and the resulting effective grants, wired to the existing unpair
action.

Explicitly NOT to add: an outstanding-pairings page (a 10-minute single-use token
offers an operator no action) and an archive page (`arizuko archive` is a CLI
operation).

- **Severity:** medium (duplication + one unused plaintext bearer store)
- **Fix:** delete `chat.go` now; sequence the rest behind phase 7; add authority
  provenance.

## N2 — an operator can grant to a non-canonical alias, silently (2026-08-04, open)

Residual risk from N1 (`beecf595`), verified as having **zero live instances**
today but no guard against acquiring one.

authd now resolves a linked OAuth login to the account's canonical sub
(`auth_users.user_id`) at mint. Grants are keyed on provider subs. So if Alice's
canonical is `google:X` and she links `github:Y`, an operator who grants to
`github:Y` creates a row that **can never match**: Alice's JWT always carries
`google:X`, and `expandPrincipals` compares exact strings.

Same shape for pairing: an `acl_membership.parent` naming a non-canonical alias
is unreachable — and unreachable to unpair too, since the contain seam compares
the caller's canonical sub against the stored parent.

Verified fleet-wide: `oauth_identities` is EMPTY on krons and sloth, and
marinade's single row is its own canonical (`user_id` = `provider:provider_sub`).
sloth's two granted principals (`google:114015391913260915382`,
`discord:user/811295670702047272`) appear in no link table, so each resolves to
itself. **Nothing is stranded today**, and nothing can be until someone links a
second OAuth identity — which no one has.

- **Severity:** low now, medium once anyone links a second login
- **Fix:** reject (or loudly warn on) a grant or pairing whose principal is a
  known non-canonical `oauth_identities.provider_sub`. authd already answers
  this — `oauthIdentityForSub` returns the owning account — so the check is a
  lookup, not new state. Do NOT silently rewrite the principal: moving a grant
  without the operator asking is the failure mode N1 chose an immutable
  canonical to avoid.
- **Status:** open — PROPOSAL, needs sign-off. Premise re-verified 2026-08-15
  at `bcb5bb83` and it holds.

**"The check is a lookup, not new state" understates it: the lookup crosses a
daemon.** `oauth_identities` lives in **auth.db** and `oauthIdentityForSub` is
unexported in package `main` of **authd** (`authd/store.go:116`). The grant and
pairing writes are **routd**, against **routd.db** (`routd/acl_resource.go`
`add_acl`). routd holds no handle on auth.db — `routd/sibling_db.go:13` states
identity is federated over HTTP, never opened — so the check is a cross-daemon
call, not a local lookup.

The transport exists: `GET /v1/identities/{sub}` returns the canonical identity
plus every linked sub, and routd already calls it via `httpIdentity.Resolve`
(`routd/identity.go:47`). Canonicality is then `identity.id != principal`.

**Why it still needs sign-off rather than being contained.** `Resolve` is
deliberately advisory: EVERY failure — transport, auth, non-200, decode —
returns the unclaimed shape, never an error, because its only caller today is
an inspect tool. Making `add_acl` REFUSE on its answer puts a hard dependency
on authd into a write path that has none, and the fail-open/fail-closed choice
is the whole decision:

- fail-CLOSED (authd down → no grants) turns an authd blip into a control-plane
  outage, over a risk with zero live instances.
- fail-OPEN (authd down → grant proceeds) is a guard that stops working exactly
  when identity data is unavailable, which is a guarantee worth less than it
  reads.

Recommending fail-OPEN with a LOUD warn on both the unreachable case and the
non-canonical case, on the grounds that N2 is a data-hygiene guard and not a
containment boundary — but that is the user's call, and either way `Resolve`
must grow a distinguishable "could not answer" result instead of folding it
into "unclaimed". Do not ship this inline.

## Z1 — all three live instances run a routd/onbod image ~5 days stale; a chunk of shipped migrations is not live (2026-08-04, open)

Found verifying `83cccc78` (the 5/8 CAS/repoint) against read-only `.backup`
copies of the three live instance DBs (never touched the originals). Every
instance's `migrations` table stops at `routd` version **19** and `onbod`
version **2**, dated 2026-07-12 and 2026-06-06 respectively — but the
codebase's `routd/migrations/` goes to **0027** and `onbod/migrations/` to
**0003**, with 0021 (2026-07-30), 0023/0025/0026/0027 (2026-08-01 through
2026-08-04) and onbod 0003 (2026-08-03) all added AFTER the running image.
`sudo docker inspect arizuko_routd_krons` shows image `30c7bd65…`, tagged
`arizuko:latest`, **created 2026-07-30 07:42**; `sudo systemctl status
arizuko_krons` shows the container restarted 11h ago (2026-08-04 10:39) —
so the SERVICE restarted recently, but on the SAME stale image, and no new
migration ran because the binary inside it doesn't contain them. Identical
version numbers (19 / 2) on krons, sloth, AND marinade — one shared stale
image, not a per-instance drift.

Concrete consequence for code shipped this week: `route_tokens.kind`
(migration 0026, 5/31 pairing-vs-route distinction) and onbod's invites
hash-at-rest `ref` PK (migration 0003) do not exist on any live routd.db/
onbod.db yet — `resreg.Export`/`Checksum` fail outright on those two
resources against live data today (`no such column: kind` / `no such column:
ref`), confirmed by the same verification run. This is NOT a bug in
`83cccc78`'s `route_tokens`/`invites` RowFilter changes — the code is correct
against the schema the codebase defines and against a freshly-migrated DB
(`routd.OpenMem()`, which the CI-equivalent tests use) — it is a **deployment
gap**: this week's migrations, and everything else shipped since the 2026-07-30
image build, are not protecting any live user.

- **Severity:** high (a growing list of shipped fixes — pairing grants,
  role-member 4R, user-profiles, drop-linked-to-sub, invites hash-at-rest —
  is not live on any instance)
- **Scope:** deployment / image freshness, all three instances
- **Affected:** krons, sloth, marinade (`routd`, `onbod`)
- **Source:** `.backup` copies verified 2026-08-04 21:4x UTC; live `migrations`
  table + `docker inspect`/`systemctl status` (read-only checks)
- **Fix:** rebuild + redeploy (`sudo make images` then `sudo systemctl restart
  arizuko_<instance>` per instance) — an operator action, not attempted here
  per "never touch a live instance." Once redeployed, re-verify `route_tokens`/
  `invites` export against real data (this session's synthetic-schema tests
  already cover the post-migration shape; only the live-data path was blocked).

## Z5 — `network_rules`' seed migration uses `CURRENT_TIMESTAMP`, making two fresh instances' checksums permanently unequal (2026-08-04, open)

Found chasing a flaky archive test (a cross-agent report of ONE failure led to
a 50-run `-count` sweep, not dismissed as a fluke). `routd/migrations/0005-
network-rules.sql` seeds two default rows (`anthropic.com`, `api.anthropic.
com`) with `created_at = CURRENT_TIMESTAMP` — SQLite's own wall-clock `now()`,
evaluated at MIGRATION-RUN time, second resolution. `network_rules` is a
registered `resreg` resource (`resreg/resources/network_rules.go`) whose row
content — `created_at` included — is part of `resreg.Export`/`Checksum`'s
projection for every `routd.db`, unconditionally (not just for resources a
given manifest mentions). Two independently-bootstrapped `routd.Open` calls
(the exact shape `cmd/arizuko/apply_test.go`'s `openInstance` helper uses
everywhere) therefore get DIFFERENT `network_rules` seed timestamps whenever
the two migration runs cross a wall-clock second — same-second runs
coincidentally match, which is why this was never caught before: every
existing cross-instance test (`TestCLI_ExportApply_RealFiles` and this
session's own archive tests) already uses `--force` for its apply, which
skips the checksum comparison entirely and never exercises this path.

`CreatedAt` is declared `StampedFields: []string{"CreatedAt"}` on this
resource, so `Diff`/`plan`'s comparison already correctly ignores it — this
is NOT a live bug in ordinary `apply`/`plan` output today. It only bites the
content-hash CAS `Checksum` computes (spec 5/8's gate for whether `apply`
requires `--force`), which hashes the raw `Export` projection with no
StampedFields exclusion. `routd/migrations/0022-seed-operator-grant-
option.sql` seeds a comparable default row (`role:operator`'s base ACL grant)
and gets this right — a fixed literal `'2026-07-30T00:00:00Z'`, not
`CURRENT_TIMESTAMP` — so the inconsistency is between two migrations solving
the same "seed a default row" problem, not a property of seeding itself.

- **Severity:** low (no live-instance impact — a real instance's
  `network_rules` seed timestamp never changes after first boot, so its OWN
  checksum stays stable across repeated exports; this only bites a scenario
  comparing checksums ACROSS two independently-bootstrapped instances without
  `--force`, which no shipped code path does today) — but a real round-trip-
  honesty gap in the same class as Z2, and a landmine for the next test or
  tool that tries a no-force cross-instance config compare
- **Scope:** `routd/migrations/0005-network-rules.sql`; the general question
  of whether `Checksum` should exclude `StampedFields` the way `Diff` already
  does (Z2's `payloadEqual` fix, if it lands, is the natural place to also
  fix this — both are "the hash is more sensitive than the comparison the
  rest of the engine already treats as canonical")
- **Affected:** any code comparing two independently-migrated `routd.db`
  checksums without `--force`; `cmd/arizuko/archive_test.go`'s
  `TestArchive_Apply_SkipsNonEmptyFolderUnlessForce` worked around it by
  testing `tarGroups`/`extractGroups` directly instead of the full checksum-
  gated `applyArchive` pipeline
- **Source:** `routd/migrations/0005-network-rules.sql`,
  `resreg/resources/network_rules.go:73`, verified 2026-08-04 by a 50-run
  `-count` sweep with the migration file moved aside vs restored
- **Fix:** re-seed via a new migration with a fixed literal timestamp
  (matching 0022's pattern) to normalize future installs — cannot rewrite an
  already-applied migration; separately, decide whether `Checksum` should
  exclude `StampedFields` generically (would also close part of Z2's gap).
  Needs sign-off (schema + a content-hash semantics change), not attempted
  here.

## ✅ FIXED 2026-08-15 F16 — the tier-drift sweep never scanned the web docs, and root docs still drift

The sweep that removed numeric tiers from the docs reported taking stale
references from 114 to 1. The measurement is not sound: its grep used
`--include="*.md"`, and the 169 files under `template/web/pub` are all `.html`
— so the entire operator-facing docs site scored zero by construction, whether
or not it drifted.

Hand-classified against the current code (excluding hot-tier/cold-tier, which is
a different and still-valid concept, and excluding CHANGELOG/BUGS history where
past state is the point), roughly eight stale tier-as-authority references
remain, concentrated in the root documents the sweep evidently did not re-check:
`CLAUDE.md:54`, `ARCHITECTURE.md:332,594-595,864`, `EXTENDING.md:59`,
`SECURITY.md:39,131`, and `specs/4/P-personas.md:45` (still "Tier 2/3: ro
mounts"). Tiers are gone from the code — no `Tier` field, no `auth/policy.go`,
no `AuthorizeStructural` — so each of these describes an authority model that
does not exist.

The three specs the sweep named — `5/33`, `5/A`, `5/M` — read clean in full and
keep `status: shipped`.

Re-verified 2026-08-15: `CLAUDE.md`, `ARCHITECTURE.md`, `EXTENDING.md`,
`SECURITY.md` are now clean of numeric-tier authority language (a prior pass
fixed them; only `grep -i tier` hits left are `hot-tier`/`cold-tier`, a
different and still-valid concept). The one straggler,
`specs/4/P-personas.md:45` ("Tier 2/3: ro mounts over inherited files"),
described an authority model that no longer exists — the actual mechanism is
the `share_mount(readonly=true)` grant (5/33; `routd/dispatch.go:546`,
`container/runner.go:571`) — fixed here to name that instead.

Live (non-`legacy/`) web docs (`concepts/scopes.html`, `reference/mcp.html`,
`reference/grants.html`, `concepts/autoviv.html`, `components/dashd.html`,
`concepts/tokens.html`, `concepts/ant.html`) carry zero numeric-tier hits.
`template/web/pub/arizuko/legacy/**` still has the old tier-1/tier-2 language,
but that tree is an intentionally preserved historical snapshot (its own
`index.html`/`legacy/` namespace), the same category as CHANGELOG history —
not drift. `slack-team/setup.html`'s one "Tier 2 (rate-limited)" hit is
Slack's own API rate-limit tier, unrelated to arizuko's auth model — false
positive, not a doc bug.

- **Severity:** low (docs describe a removed authority model)
- **Scope:** root docs + web docs tier references
- **Affected:** readers of CLAUDE/ARCHITECTURE/EXTENDING/SECURITY
- **Fix:** `specs/4/P-personas.md:45` rewritten
  (`share_mount(readonly=true)` grant, not "Tier 2/3"). Root docs and live
  web docs were already clean at HEAD `0d376586`; `legacy/` is an
  intentional archive, out of scope.
