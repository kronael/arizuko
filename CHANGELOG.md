# Changelog

All notable changes to arizuko are documented here.

arizuko is a fork of [nanoclaw](https://github.com/nicholasgasior/nanoclaw)
(upstream at v1.1.3).

---

## [Unreleased]

### Added

- Per-migration announcements. Paired `.md` next to any
  `store/migrations/NNNN-*.sql` is captured into a new `announcements`
  table at migration time. On startup, `gated` drops one
  `system_message` (origin=`migration`) into the root group listing
  pending versions; the new `/announce-migrations` root skill fans
  each body out to every `chats.jid` exactly once, keyed by
  `announcement_sent(service, version, user_jid)`, and notifies inner
  groups via a one-line system message.

### Fixed

- `grants`: tier-1 now hardcodes `send_message`/`send_file`/`send_reply`.
  Production routes store `room=X` without a `platform=` key, so
  `platformRules` returned empty and tier-1 agents had no send rules.
  Tier-2 got the same fix on the same day.
- `compact-memories` skill: recognizes XML-wrapped telegram messages
  (`<messages><message ...>`) as real user activity. Previous heuristic
  discarded them along with tool-result turns, producing false "no
  user activity" summaries.

### Testing

- Per-daemon integration tests landed for all daemons (gated, container,
  timed, onbod, dashd, webd, proxyd, teled, discd, mastd, bskyd, reditd,
  emaid, linkd, whapd) plus MCP socket round-trip.
- New `tests/testutils` package with `FakeChannel`, `FakePlatform`,
  `NewInstance` helpers.
- `container.Runner` interface extracted for test injection; `run_test.go`
  covers docker arg assembly and marker parsing.
- `gateway/integration_test.go` exercises poll loop + runner contract.
- `emaid`: SMTP send happy-path via injectable sender.
- `whapd`: vitest integration test for send handler.

### Changed

- **chanlib**: absorbed cross-package primitives. Single `URLCache` (12-hex
  LRU, cap 4096) replaces three divergent private `fileCache` impls in
  discd/mastd/reditd. `CopyDirNoSymlinks`+`CopyFile` (fsutil) replace
  duplicated copies in container + gateway (io.Copy path wins). `EnvInt`,
  `EnvDur` join existing `EnvOr`; core no longer carries its own copies.
  `ShortHash` replaces identical 4-byte sha256 log tags in onbod + webd.
- **mastd/reditd**: deduped message conversion. mastd `handleNotification`
  now calls `notificationToMsg`; reditd extracted `thingToMsg` shared by
  `handleThing` (poll) and `FetchHistory` (backfill). ~75 lines removed.
- **bskyd**: dropped no-op `oldestInPage` branch in `FetchHistory`; fixed
  staticcheck lints (numeric 401 → `http.StatusUnauthorized`,
  `t.Sub(time.Now())` → `time.Until`).

### Removed

- **store**: `ConsumeInvitation` (dead — onbod has its own atomic consume).
- **webd**: unused `authSecret` + `trustedProxies` config fields.

### Fixed

- **onbod**: `genToken` silently discarded `crypto/rand` errors; now panics
  on RNG failure (matches `core.GenSlinkToken`). A zero-entropy token
  would be a guessable credential.

## [v0.29.4] — 2026-04-19

### Changed

- **ipc**: replace the (disabled) token preamble with kernel-attested
  `SO_PEERCRED` on every MCP connection. `ServeMCP` takes an
  `expectedUID int` parameter (1000 = ant image's `node` user in prod,
  host uid when `--user` override fires in dev) and rejects any peer
  whose kernel-reported uid doesn't match. No client changes needed —
  standard MCP, unchanged socat bridge. Removed dead code:
  `GenerateRuntimeToken`, `verifyToken`, `McpToken` field,
  `ARIZUKO_MCP_TOKEN` env stamp. Security boundary remains per-group
  mount isolation (`buildMounts` bind-mounts only the group's own
  `ipcDir` → `/workspace/ipc`, validated by `folders.IpcPath`);
  peer-uid check is a cheap sanity gate, not the boundary.

## [v0.29.3] — 2026-04-19

### Fixed

- **ipc**: MCP token preamble enforcement disabled. Ant's socat bridge
  (`/ant/src/index.ts`, settings.json mcpServers.arizuko) connects to
  the unix socket and sends MCP JSON-RPC directly — it never wrote the
  `{"token":"<hex>"}\n` preamble that `ipc.ServeMCP` required, so every
  MCP connection was rejected and every `get_history`/`get_facts`/any
  gateway tool failed silently. Symptom: agents replied "nemám záznam"
  / "no context" because `get_history` was unreachable. Fix: pass empty
  token to `ServeMCP`; socket is already 0660 + chowned to container
  uid, filesystem perms are the real isolation boundary.

## [v0.29.2] — 2026-04-17

### Added

- **webd/mcp**: single JWT-gated MCP streamable-HTTP endpoint at `/mcp`.
  One endpoint per instance — the authed user can reach any folder in
  their `user_groups` ACL via `folder` arguments on each tool. Three
  tools: `list_groups` (filtered by grants), `send_message` (stamps
  authed sub/name), `get_history` (topic-scoped). No anonymous MCP.
- **webd/slink**: `POST /slink/<token>` with `Accept: text/event-stream`
  holds the connection open and streams user bubble + subsequent
  assistant responses on the same (folder, topic). Callers can now
  submit and receive on one request instead of POST + separate
  `/slink/stream` SSE.
- **webd/slink**: REST variant — `POST /slink/<token>` with
  `Accept: application/json` returns `{user: {...}}`. Add `?wait=<sec>`
  (1-120) to block until the first assistant reply, returning
  `{user, assistant}`. Enables scriptable curl-style usage without SSE
  plumbing.
- **container**: new env vars for bot identity — `ARIZUKO_GROUP_FOLDER`,
  `ARIZUKO_GROUP_NAME`, `ARIZUKO_GROUP_PARENT`, `ARIZUKO_WORLD` (tier-1
  top-level folder), `ARIZUKO_TIER` (0 root, 1 world, 2 building,
  3+ room). Hello/howto skills use these for in-persona greetings.
- **ant/skills/soul**: user-initiated `/soul` brainstorming skill that
  writes `~/SOUL.md`. Hello + howto read SOUL.md when present and
  inject tagline + persona into output.

### Changed

- **auth/store**: operator is implicit — emergent from grants, not a
  nil-sentinel. `store.UserGroups` now returns plain `[]string`
  (previously `*[]string` with nil = operator). `Claims.Groups` and
  `setUserHeaders` follow suit: always a slice, `**` inside it is the
  only operator signal. `auth.MatchGroups` handles `**` uniformly, so
  downstream gates (davRoute, webd.requireFolder) no longer need a
  "missing header = operator" special case. Less indirection, one
  code path.
- **webd/hub**: `serveSSE` flushes headers immediately on connect so
  plain net/http clients return from `Do` without waiting for the
  first event. Logging middleware's `statusWriter` now passes through
  `http.Flusher`.
- **compose/daemons**: unified internal listen port on `:8080` for
  every daemon (gated, webd, dashd, onbod, proxyd, vited). Host-side
  publish ports (`API_PORT`, `WEB_PORT`, `DASH_PORT`, `DAV_PORT`) map
  to container `:8080`. Peer URLs (`WEBD_URL`, `ROUTER_URL`,
  `DASH_ADDR`, `WEBD_ADDR`, `VITE_ADDR`) default in code to
  `http://<service>:8080` — compose no longer sets them. `proxyd`
  now reads `PROXYD_LISTEN` (default `:8080`) instead of `WEB_PORT`
  for its container-internal listen.
- **compose/env**: every arizuko daemon gets `env_file: ['.env']`.
  Shared config and secrets flow implicitly from the instance `.env`;
  per-service `environment:` blocks now hold only compose-side
  overrides (container paths, `TIMEZONE` transform, `API_PORT=8080`
  pin, feature-gated `DAV_ADDR`/`ONBOD_ADDR`). Eliminates the
  per-service env whitelists. Adapter TOMLs also pick this up.
- **template/services**: adapter TOMLs use literal
  `ROUTER_URL = "http://gated:8080"` — `${API_PORT}` interpolation is
  no longer correct since gated's container-internal port is fixed.
  Deployments with customized services/_.toml must be updated
  manually (e.g. `sed -i 's|:${API_PORT}|:8080|' services/_.toml`).

### Fixed

- **auth/proxyd**: new OAuth users with no groups now land on `/onboard`
  instead of `/`; unauthenticated requests to deep links (`/chat/X`,
  `/dash/Y`) preserve the original path through login via `auth_return`
  cookie (10-min TTL).
- **healthchecks**: unified all daemons on internal `:8080/health`.
  `dashd` compose now pins internal `DASH_PORT=8080` (host publish still
  uses `.env`); `timed` now runs a small HTTP server exposing `/health`
  (db.Ping); `onbod` registers `/health`; `vited` probes `/@vite/client`
  (Vite has no `/health`); `whapd` Dockerfile healthcheck moved from
  `:9002` to `:8080`; adapter service TOMLs (`teled`, `discd`, `mastd`,
  `bskyd`, `reditd`, `whapd`) set `LISTEN_ADDR=:8080`. Eliminates the
  mass of `(unhealthy)` containers from the port-unification migration.
- **webd (mobile)**: chat page now viewport-aware — `100dvh`, safe-area
  padding on footer, larger touch targets (44px send, 36px header
  buttons), 16px textarea font (prevents iOS zoom), `enterkeyhint=send`,
  hidden tagline <640px, bubble max-width 88% on phones.

### Ops

- **Makefile**: `make images` now runs `docker image prune -af` first
  to reclaim dangling layers (prevents disk-full on hosts with a ~100G
  root during successive agent-image rebuilds).
- **tests**: coverage expansion — `teled` →58.5%, `discd` →70.5%,
  `mastd` →69.5%, `bskyd` →84.2%, `timed` →72.1%, `dashd`, `onbod`.
  Integration tests drive inbound/outbound paths against in-process
  httptest mocks per platform.

## [v0.29.1] — 2026-04-16

### Changed

- **ant/resolve**: classification section headings (`## Classify`,
  `Continuation —`, `New task —`) are internal only — never emitted
  to the user. Fixes scaffolding leak observed on marinade Apr 16.
- **ant/compact-memories**: episodes now preserve user corrections
  verbatim rather than agent-drawn conclusions. Conclusions get
  redrawn fresh each recall; corrections don't.
- **ant/recall-memories**: weight corrections over conclusions.
  Never reuse a prior agent summary as a fact.
- **ant/migrate**: `~/.announced-version` is written BEFORE the
  broadcast loop, not after. Prevents a mid-fanout container restart
  from re-announcing the whole release. Also: fix broken
  `refresh_groups | jq .jid` (MCP tool returns folder, not jid) by
  looking up JIDs from the `routes` table.
- **ant/CLAUDE.md**: attachment rule — `[Document: …]` placeholder
  without `<attachment path=…>` tag means the file did NOT arrive.
  Do not claim you read it.

### Fixed

- **store.UserGroups**: correctness — only `**` marks operator (was
  checking `*`). Aligns with spec and CLAUDE.md.
- **onbod.userGroups**: same — drop `*` branch, align on `**`.

## [v0.29.0] — 2026-04-16

### Added

- **cli**: `arizuko group <instance> grant|ungrant|grants` — manage
  `user_groups` ACL rows from the host CLI instead of hand-editing
  SQLite. `grant <sub> <pattern>` is idempotent, `grants [<sub>]`
  prints an aligned table, `ungrant` reports zero rows cleanly.
  Migration `0026-user-groups-granted-at.sql` adds a nullable
  `granted_at` timestamp column.
- **auth**: `MatchGroups(allowed, folder)` helper for glob-matched ACL
  (`auth/acl.go`). `**` matches anything; otherwise `path.Match`
  semantics. Shared by `onbod` route-creation guard and `proxyd.davRoute`.
- **onbod**: second-JID auto-link. When a user who already has a world
  messages from a new platform, the dashboard handler auto-routes the
  new JID into the existing folder and skips the username picker.
- **ant**: `/migrate` now broadcasts new releases — after migrations
  apply, root agent fans out the latest CHANGELOG entry to every
  registered group via `send_message`. Per-group `~/.announced-version`
  prevents re-broadcast. Manual fan-out until the automatic db_utils-based
  announcement path (`specs/3/e-migration-announce.md`) is implemented.

### Changed

- **proxyd**: `davRoute` uses `auth.MatchGroups` for folder
  authorization (was a dumb prefix check). Missing `X-User-Groups`
  header still means operator (unrestricted).
- **onbod**: `handleCreateWorld` gates route INSERTs behind
  `auth.MatchGroups` against the user's `user_groups` entries.
- **groupfolder**: `*` and `**` are now reserved folder names so they
  cannot collide with ACL glob patterns.
- **db_utils**: renamed from `dbmig/` to `db_utils/` (matches the
  `*_utils` convention). Unified schema ownership: `gated` (via `store/`)
  owns the shared DB schema; `timed` and `onbod` connect to the
  already-migrated DB and no longer carry their own migrations.
- **store**: now uses `db_utils.Run` instead of a duplicated inline
  migration runner; exposes `store.Migrate(db)` for tests that need a
  schema'd fixture.

### Removed

- **timed**: `timed/migrations/` (redundant — store creates the same
  tables) and the migration runner in `timed/main.go`.
- **auth**: `auth/migrations/` (dead code — never loaded; tables live
  in store migrations).

### Fixed

- **queue**: remove duplicate error notification — gateway's
  `Failed: ...` message is now the single error surface. Queue-side
  `notifyError` was firing a second message for every failure.

## [v0.28.0] — 2026-04-15

### Added

- **auth**: ACL flip — no user_groups rows = no access, `*` = operator
- **onbod**: token-based web onboarding — chat sends auth link, user picks username on web dashboard
- **store**: user_jids table with unique JID constraint, migration 0024-0025

### Fixed

- **onbod**: XSS escaping on all user-controlled HTML output
- **onbod**: token consumed on first use (prevents replay attacks)
- **onbod**: JID uniqueness enforced (prevents hijacking)
- **ant**: bare-URL rule in output styles, dashboard vs docs clarification
- **ant**: 13 skills synced from host (aligned versions)

### Removed

- **onbod**: dead approve/reject chat flow, registerSelf, onboarding.channel column

## [v0.27.0] — 2026-04-15

### Added

- **webd**: web chat (slink) integrated into compose, auto-generate slink tokens
- **agent**: Python 3.14 via uv, uv/uvx instructions in CLAUDE.md and python skill
- **agent**: web routing table, auth flow, gateway commands in CLAUDE.md
- **skills**: web chat (slink) documentation in howto and web skills
- **proxyd**: unknown paths redirect to /pub/ prefix (public fallback)
- **eval**: checks 17-20 for skill seeding, dispatch discovery, consistency, resolve wiring
- **specs**: user-centric identity model (28), local CLI (29)

### Fixed

- **ant**: drain stale IPC nudges after query to prevent duplicate responses
- **ant**: only discard self-generated nudges, preserve gateway steers
- **ant**: progress nudge thresholds raised (200→500 msgs, 10→15 min)
- **ant**: prevent agents from self-creating or modifying SOUL.md without sign-off
- **proxyd**: /pub/\* always routes to vite (was broken when webd upstream set)
- **gateway**: replace groupForJid with per-message resolveGroup
- **gateway**: mark unrouted JIDs errored to stop drain loop
- **gateway**: skip unrouted JIDs in recoverPendingMessages
- **gateway**: filter silent refusal outputs (No response requested, etc)
- **gateway**: replace silent refusal regex with \<think\> block approach
- **skills**: compact-memories date-filters transcripts, globs all project dirs
- **skills**: web skill enforces /pub/ only, documents all proxyd routes
- **whapd**: Dockerfile COPY paths for repo-root build context

## [v0.26.1] — 2026-04-13

### Fixed

- **gateway: cursor advances once on delivery, not twice**: steer path
  records timestamps in `steeredTs` map; `advanceAgentCursor` merges
  `max(batch, steered)` into a single cursor write on container completion
- **gateway: auto-migrate message starts with /migrate**: ensures the
  `/migrate` skill dispatches correctly instead of being treated as plain text
- **ant: silent means silent**: agents produce no output when declining
  to respond, preventing empty reply messages
- **container: WEB_PREFIX uses full folder path**: `pub/<world>/<child>`
  matching the actual web route hierarchy
- **store: PendingChatJIDs SQL**: replaced brute-force 7-platform recovery
  loop with single SQL query (-180 lines)
- **hello skill**: rewritten to lead with use cases, not mechanics

## [v0.26.0] — 2026-04-13

### Added

- **auto-migrate on startup**: gateway checks agent MIGRATION_VERSION on
  boot, injects `/migrate` system message to outdated root groups
- **`/resolve` skill**: gateway-nudged task classification, context recall,
  and skill dispatch on every prompt (replaces /dispatch which had 0%
  compliance). Migration 054 cleans stale dispatch refs
- **`/root` gateway command**: delegates messages to instance root group
  with grants-based auth (tier <= 1)
- **migration 055 — bookkeeping cron tasks**: seeds the 5 compact-memories
  scheduled tasks for groups missing them
- **mcpc in agent image**: `@apify/mcpc` MCP CLI for ad-hoc scripts to
  call MCP tools. Migration 052 documents usage
- **agent-generated status messages**: `<status>` blocks in agent output
  are stripped and delivered as interim progress messages via IPC nudge
  (replaces generic heartbeat)
- **session inactivity reset**: sessions idle >2 days reset to fresh
  instead of resuming stale context
- **fact staleness check**: `/resolve` recall checks `verified_at` on
  facts, triggers re-research if stale (>14 days)
- **skill 'Use when' triggers**: 9 skill descriptions now include explicit
  trigger conditions for better semantic dispatch matching
- **agent container tools**: xh, websocat, hurl, age, sops added to image
- **eval skill**: episodic + knowledge memory checks across all instances

### Changed

- **`/research` skill renamed to `/hub`**: avoids shadowing Claude's
  built-in `research` tool. Migration 053 cleans stale overlays
- **gated runs as uid 1000**: no longer runs as root inside the container
- **IPC user-message drain**: moved from poll timer to PostToolUse hook
  for lower-latency mid-loop message injection

### Fixed

- **gateway: auto-migrate recovery bugs**: mount path mismatch
  (/srv/app/arizuko vs HostAppDir), parent field check for root detection,
  recoverPendingMessages LIMIT bug (replaced with route-based per-chat check)
- **gateway: @prefix router dropped messages with @handles**: anchored
  prefix regexes to start-of-message; fall through on non-existent child
- **gateway: strip @botname suffix from Telegram commands**: `/new@botname`
  was silently ignored
- **gateway: late-bind channel in makeOutputCallback**: fixes race when
  channel connects after container starts
- **gateway: chown .git to 1000:1000 after git init**: fixes permission
  errors when gated runs as uid 1000
- **whapd: typing indicator across long runs**: ported TypingRefresher
  (15s refresh, 10min cap), closes last adapter typing gap
- **whapd: extract reply metadata from Baileys contextInfo**: inbound
  replies now populate reply_to/reply_to_text/reply_to_sender
- **typing: call clear on maxTTL expiry**: indicator would not stop after
  10min runs in both Go and TS adapters
- **typing: log/validate silent failures** in handler, discd, httpchan
- **store: write sticky-resolved topic on inbound messages**
- **dashd: resolve symlinks in renderMemorySection**
- **compose: add host.docker.internal extra_hosts to gated**
- **ipc: purge legacy file-based IPC dirs**
- **ant: raise progress count threshold from 100 to 200 messages**

### Removed

- **arizuko-mcp CLI**: custom Python MCP client replaced by mcpc
- **backwards-compat shims and band-aid fixups**: dead code cleanup
- **`arizuko create` chownTree and subdir mkdirs**: unnecessary post-seed ops
- **Makefile: dead per-daemon Dockerfile build targets**

## [v0.25.1] — 2026-04-09

### Fixed

- **gateway**: `pollOnce` steering branch now advances `agentCursor` for the
  full steered batch via `SendMessages`. Previously the cursor was left
  behind, so after the container exited `drainGroupLocked` saw the same
  rows as unprocessed and respawned a new container on the same inputs
  (duplicate delivery). Success is now logged at Info:
  `"poll: steered messages into running container" count=N`.

### Changed

- **ant**: true mid-loop steering via a `PostToolUse` hook
  (`createIpcDrainHook`) wired into `query()` options. The hook drains the
  IPC input dir between tool calls and returns queued messages as
  `hookSpecificOutput.additionalContext`, appended to the tool result
  Claude is about to read — injecting follow-ups inside the active
  agentic loop instead of waiting for the next turn. `pollIpcDuringQuery`
  remains as a `stream.push` fallback for text-only turns. A
  `drainIpcInputMutex` flag shared with the poll timer prevents
  double-draining the same files.
- **queue**: `SendMessage(jid, text)` renamed to
  `SendMessages(jid, []string)`. Loops `writeIpcFile` per message and
  signals the container once per batch. Success log at Info level.
- **store**: `messages.source` is now the canonical adapter-of-record per
  message. Inbound messages stamp the receiving adapter; outbound delivery
  resolves the adapter via `store.LatestSource(jid)` (latest non-bot inbound).
  Replaces three stacked layers of channel-pin hotfixes.
- **api/handleMessage**: stamps `messages.source` with the registered adapter
  name on every inbound delivery (was previously a no-op write of `''`).
- **api/handleOutbound**: resolution order is now (1) explicit `channel`
  field, (2) `LatestSource(jid)`, (3) `chanreg.ForJID(jid)`.
- **onbod**: dropped per-onboarding `channel` pin — outbound delivery uses
  `/v1/outbound`'s `LatestSource` lookup instead of explicit channel routing.
- **whapd**: dropped `@lid` -> phone-number translation entirely. Baileys
  removed `makeInMemoryStore` so the contacts/LID discovery paths no longer
  worked; arizuko now treats `@lid` as the canonical opaque WhatsApp identifier.
  Removed `whapd/src/lid.ts` and the dead `normalizeJID` helper in `ipc/`.
- **specs**: scrubbed stale routes/outbound/delegation references, updated
  WhatsApp examples to `@lid` form, and rewrote audit-log + JID-format +
  worlds-rooms specs to reflect post-0023 schema and source semantics.

### Removed

- **store schema (migration 0023)**: dropped dead columns —
  - `chats.name`, `chats.channel`, `chats.is_group`, `chats.last_message_time`
    (chats now `(jid, errored, agent_cursor, sticky_group, sticky_topic)`)
  - `messages.group_folder` (only ever written, never read)
  - `onboarding.sender`, `onboarding.world_name`, `onboarding.channel`
    (onboarding now `(jid, status, prompted_at, created)`)
- **store**: `Store.PutChat` removed — chats rows are no longer pre-created
  per message. `MarkChatErrored` now upserts.

---

## [v0.25.0] — 2026-04-08

### Changed

- **routes**: collapsed routes table to `(id, seq, match, target, impulse_config)` —
  dropped `jid` and `type` columns. Replaced route types (command/verb/pattern/
  keyword/sender/prefix/default) with a single match expression language:
  space-separated `key=glob` pairs over platform/room/chat_jid/sender/verb,
  using Go `path.Match` globs. Empty match = wildcard. See specs/1/F-group-routing.md.
- **gateway**: three-layer pipeline — sticky → command → prefix → routing. Only
  the routing layer reads the routes table; commands and prefixes are in-code.
- **gateway**: `pollOnce` no longer pre-filters by registered JIDs — fetches all
  new messages, resolves each via `DefaultFolderForJID` (routes table). The old
  `RegisteredChatJIDs`/`RegisteredRooms` helpers are removed.
- **gateway**: unified inbound/outbound message paths — agent output now written
  to messages table via `PutMessage` instead of separate `StoreOutbound`.
- **gateway**: delegation is now message-based — `delegateViaMessage` writes to
  `local:targetFolder` with `forwarded_from` as return address. No more closures.
- **gateway**: `#topic` prefix route converted from `EnqueueTask` closure to
  `PutMessage` + `EnqueueMessageCheck`.
- **ipc**: `delegate_group` and `escalate_group` write messages to DB directly
  instead of calling gateway closures.
- **ipc**: `send_message`/`send_reply` record output via `PutMessage` to unified
  messages table.
- **queue**: removed `EnqueueTask`, `taskFn`, `queuedTask`, `runTask` — all work
  flows through messages, no more task closures.

### Removed

- **core**: `OutboundEntry` type.
- **store**: `StoreOutbound()` function.
- **ipc**: `DelegateToChild`/`DelegateToParent` from `GatedFns`.
- **ipc**: `StoreOutbound` from `StoreFns`.

---

## [v0.24.2] — 2026-04-07

### Fixed

- **ant**: output style (e.g. telegram.md) now injected into custom system
  prompts — SDK only injects it for the preset prompt, which we don't use.
- **ant**: progress interval 5min → 10min to reduce noise.

### Changed

- **queue**: DB-implicit pending — removed in-memory `pendingMessages` boolean,
  drain now queries `store.HasPendingMessages` via callback. SendMessage race
  fixed (single lock).
- **container**: hard deadline 30min → 60min; soft deadline warns agent 2min
  before kill via IPC message + SIGUSR1.
- **ant**: time-based progress updates — agent sends status every 10min or 100
  SDK messages, whichever first.

---

## [v0.24.1] — 2026-04-07

### Fixed

- **teled**: use `Request` instead of `Send` for `sendChatAction` — Telegram
  returns `bool`, not `Message`. Was spamming WARN every 4s on every active chat.
- **migration**: onboarding simplification migration now skips JIDs that already
  have routes, preventing re-onboarding of existing users.

### Changed

- **ant**: PreCompact hook names vital files (SOUL.md, CLAUDE.md, diary/, facts/,
  users/) by reference so the summarizer preserves them across compaction.

---

## [v0.24.0] — 2026-04-06

### Fixed

- **gateway**: reply-to always targets triggering user message, not stale bot
  reply from previous run. Steering messages (follow-ups mid-run) now update
  reply-to target so subsequent bot chunks reply to the latest user message.
- **file send**: preserve original filename through temp dir + chanreg multipart
  header. MCP `send_file` tool description updated — caption IS the message.
- **teled**: log typing API errors at warn level instead of silently swallowing.
- **whapd**: resolve LID JIDs via `WHATSAPP_LID_MAP` env + `onWhatsApp` fallback.
- **chanreg**: `CreateFormFile` uses `name` param when non-empty instead of
  `filepath.Base(path)` which sent temp filenames.

### Changed

- **onbod**: simplified from 4-step hierarchical flow to 2-step — greeting +
  leave message, admin picks folder on `/approve <jid> <folder>`.
  `ONBOARDING_GREETING` env var added to compose + all instance configs.
- **gateway**: output callback takes channel directly, eliminating duplicate
  `findChannel` per output chunk. `replyTo` is a local var, no DB read per
  chunk. `SetLastReplyID` persists for IPC consumers only.
- **emaid**: re-fetch attachments from IMAP on demand, no local storage.
- **adapters**: all 6 channels now have consistent file proxy (`GET /files/{id}`
  auth-gated) and inbound media extraction.

### Refactored

- **core**: `MsgID(prefix)` utility replaces 9 scattered `fmt.Sprintf` ID
  generation patterns across 8 packages.
- **groupfolder**: `IpcInputDir`, `IpcSocket`, `IpcSidecars`, `GroupMediaDir`
  helpers replace hardcoded path joins across container/queue/gateway.

### Docs

- Updated stale onboarding refs in ARCHITECTURE.md, specs/4/21-onboarding.md,
  docs/arizuko.html. Routing guide added to docs/routing.md. History backfill
  spec added.

---

## [v0.23.2] — 2026-04-06

### Changed

- **onbod**: redesigned onboarding from single "pick a workspace" to 4-step
  hierarchical flow: pick a world → pick a house → pick a room → leave a
  message for the admin. Name steps validate `[a-z0-9_-]`, message step
  accepts any text. `approveInTx` auto-creates parent groups in the
  hierarchy.
- **container**: unified group filesystem setup into `container.SetupGroup`.
  CLI, IPC, and onbod all use one function for mkdir + prototype copy + seed.
  Removed duplicate `copyDir` from onbod, dead `seedGroupDir` helper from CLI.

---

## [v0.23.1] — 2026-04-06

### Fixed

- **gateway**: canonical file extensions for inbound attachments — pin `.jpg`,
  `.png`, `.gif`, `.webp`, `.ogg`, `.mp3`, `.m4a`, `.mp4` so agents can read
  photos regardless of OS mime table (`mime.ExtensionsByType` returned `.jfif`
  on Debian, breaking Claude Read).
- **gateway**: single-write agent output — `makeOutputCallback` no longer
  dual-writes `unsent-*` + `out-unsent-*` rows; `StoreOutbound` now carries
  `topic` + `routed_to` so `MessagesByTopic` finds bot replies.
- **proxyd**: forward WebSocket upgrades on `/` to vited instead of 302
  redirecting to `/pub/` — Vite HMR client opens `wss://host/` for live reload.
- **vite**: enable `watch.usePolling` (docker bind-mount inotify unreliable)
  and `hmr.clientPort: 443, protocol: 'wss'` for proxy setup.
- **gateway**: onboarding no longer triggers for JIDs that already have a
  default route in the DB.

### Changed

- **gateway**: removed all 4 in-memory DB caches (`groups`, `jidToFolder`,
  `jidAdapters`, `agentCursors`). All lookups now query SQLite directly.
  Route/group changes take effect on next poll cycle without restart. -99 LOC.
- **ant**: added tenancy model section to CLAUDE.md — tier 0 root, tier 1
  world, tier 2 building, tier 3 room, isolation boundaries, threads.

---

## [v0.23.0] — 2026-04-05

### Fixed

- **store**: `StoreOutbound` now sets `is_bot_message=1`. Commit 6cf0f10 added
  `sender='bot'` but left the flag at 0, so `MessagesSince`'s
  `is_bot_message=0 AND sender NOT LIKE botName%` filter kept matching the
  agent's own output — every agent reply was re-ingested as inbound, producing
  a self-reply loop across REDACTED and REDACTED. 71 poisoned rows repaired in-place.
  Regression test `TestStoreOutbound_ExcludedFromMessagesSince` pins the fix.
- **gateway**: `downloadFile` now sends `Authorization: Bearer <CHANNEL_SECRET>`
  when fetching attachments. teled `/files/` is wrapped in `chanlib.Auth`, so
  unauthenticated downloads 401'd silently; `enrichAttachments` swallowed the
  error and the agent received the raw `[Document: …]` placeholder, leading to
  hallucinated "I can't access the document" responses.
- **onbod**: `/v1/outbound` accepts an optional `channel` field and onbod
  threads `onboarding.channel` through every `sendReply` call site (prompt,
  invalid, taken, waiting). Fixes the REDACTED 502 where messages arriving via
  `telegram-REDACTED` were routed via `reg.ForJID` to primary `telegram`, which
  wasn't a member of that chat.
- **onbod**: name-taken branch no longer leaks an enumeration oracle for
  registered worlds; collapsed into the invalid-name path.
- **proxyd**: logging middleware now wraps the whole mux. Previously only `/`
  was wrapped, so `/auth/*` and `/pub/*` routes bypassed logging entirely.
- **ipc**: `register_group fromPrototype` now passes the parent folder instead
  of the child JID. The child's JID was unregistered so `SpawnGroup(jid, jid)`
  always returned "parent group not found". Contract updated to
  `SpawnGroup(parentFolder, childJID)`.
- **ipc**: guard against deleting a tier-0 default route from under a running
  agent.
- **gateway**: persist and seed adapter pinning from `chats.channel` so
  cross-adapter replies keep flowing through the originating adapter.
- **gateway**: advance agent cursor after prefix-route handoff — `@nobody` and
  other non-matching prefix routes no longer reprocess on restart.
- **gateway/store**: `session_log` now records every container run.
  `RecordSession` takes a caller-provided start time for true duration,
  `EndSession` always runs (not gated on a new session id), and continued
  sessions log per-run rows with COALESCE backfill when the session id is
  learned mid-run.
- **store**: `StoreOutbound` no longer silently drops rows on the NOT NULL
  `sender` constraint; synthesizes a unique `out-unsent-<nano>` ID when
  `PlatformMsgID` is empty so failed sends don't collide on the `out-` PK.
- **whapd**: `registerWithRetry` with backoff instead of `process.exit(1)` on
  router register failure. Also `recoverCredsIfEmpty` + atomic `backupCreds`
  so Baileys' non-atomic `writeFile` can no longer corrupt `creds.json` across
  restart loops.
- **proxyd**: fail closed on empty `AUTH_SECRET`; redirect bare `/` to `/pub/`
  and bare `/pub` → `/pub/` so trailing-slash is optional.
- **vited**: bake MPA mode + trailing-slash 301 plugin into the image.
- **auth**: add policy case for `get_grants`/`set_grants`.

### Added

- **chanlib**: `TypingRefresher` wired into teled (4s refresh) and discd (8s
  refresh) via `BotHandler.Typing` → `typing.Set`. Telegram/Discord native
  typing expires in 5–10s, so long agent runs were losing the indicator.
- **auth**: `CheckSpawnAllowed(parent, groups)` helper unifies `MaxChildren`
  enforcement across `gateway/spawn.go` and `ipc/ipc.go` (logic was literally
  duplicated across two buckets).
- **tests**: 15 new regression tests. 9 in `chanreg` covering
  `ForJID`/`Resolve`/`Entry.Owns` (primary-over-variant preference, fallback
  chain, multi-prefix, no-match) and 6 in `api` covering `/v1/outbound`
  (channel-pinned regression test, ForJID fallback, stale-channel fallback,
  404, validation, auth). Plus `TestStoreOutbound_ExcludedFromMessagesSince`.
- **specs/6**: `6-workflows.md` (workflowd — declarative flows reading the
  shared SQLite bus) and HITL firewall + authoring product drafts.

### Refactored

Full refinement pass across 10 subsystem buckets. 58 `[refined]` commits,
~-155 LOC net. Selected highlights:

- **store**: `msgCols` constant + `scanMessage`, `routeCols` + `scanRoute`,
  COALESCE-flattened nullable scans, removed kanipi-era `seedFromPragma` shim,
  dropped unused `TaskCompleted`/`SpawnTTL`/`ArchiveDays` fields.
- **gateway**: `tryExternalRoute` dedupes poll/process routing, `logAgentError`
  flattens callbacks, `containsFold` helper, `strings.Cut` for command
  parsing, consolidated `cmdNew` branches.
- **container**: extracted `prepareInput`, unexported `BuildMounts`/
  `VolumeMount`/`ReadonlyMountArgs`, dropped dead last-line output fallback.
- **queue**: removed dead `idleWaiting` field and `closeStdinLocked` helper.
- **ipc**: `granted()` wrapper dedupes tool registration, folded `CheckAction`
  nil-gate into it, dropped `groupFolderByJid` (use `folderForJid` via routes
  table).
- **auth**: inlined single-use `getTier`, simplified `splitArgon2`, fixed
  login limiter eviction.
- **grants**: unified `matchGlob`/`matchValueGlob` via boundary function.
- **chanreg/api**: deduped auth + JID-owns checks, deduped `HealthCheck`.
- **chanlib**: `Run` helper collapses main.go boilerplate across adapters;
  adapters implement `BotHandler` directly, wrapper layer removed.
- **adapters**: bskyd/mastd/reditd dropped `router_client.go` alias wrappers;
  reditd merged `get`/`post` into single `do`; bskyd merged
  `xrpcAuth`/`xrpcWithAuth` and inlined `createSession`; teled replaced
  `stubHandler` reimpl with real handler + stub bot; whapd dropped unused
  `link-preview-js`/`form-data`/`thread_id`/`topic` fields.
- **onbod**: delegated schema to gated (dropped own migrations + `dbmig`
  dep); `sendReply` takes required channel; `approveInTx` owns timestamp +
  welcome construction; inlined `notify` (single-caller library removed).
- **dashd**: trust proxyd identity headers (dropped JWT reverify); `dash`
  methods own writer helpers; inlined single-use `writeGroupMessageCount`.
- **timed**: dropped `checkedSpawns` debug counter; `cleanupSpawns` via
  routes table.
- **proxyd/webd**: `davRoute` shares `X-User-Groups` parse; dropped dead
  `Description` field, trivial comments, and `fmt` dep for single Hijack
  error; simplified `XTopics`, inlined `hubKey`.
- **cmd/arizuko**: `die` helper, extracted `seedGroupDir` and
  `requireCompose`.
- **compose**: inlined `namedService`, moved `CHANNEL_SECRET` into
  `routerEnvKeys`, dropped dead `HOST_DATA_DIR` empty check.
- **template**: dropped unused `REDACTED_USERS` from `env.example`.
- **groupfolder**: unified `GroupPath` and `IpcPath` via `resolve` helper.
- **mountsec**: dropped `LoadAllowlist` and tests — `container/runner.go`
  always passes empty `Allowlist{}`, so the file loader was dead on arrival.
  ARCHITECTURE.md section corrected.

---

## [v0.22.0] — 2026-04-04

### Changed

- **schema**: `registered_groups` table renamed to `groups`, rekeyed by `folder` (PK) instead of `jid`. Migration 0020 handles the transform automatically.
- **schema**: `agent_cursor` moved from groups to `chats` table (per-JID, not per-folder)
- **schema**: all JID→folder mappings now stored as `type='default'` entries in `routes` table
- **gateway**: dual-map architecture (`groups` + `jidToFolder`) for folder-keyed group lookup with JID resolution via routes
- **cli**: `group rm` now takes folder (was JID); `group list` shows `folder\tname`

### Fixed

- **media**: use original filename for downloaded attachments when available
- **gateway**: `get_history` now checks routes table for JID→folder access (was checking only registered_groups which no longer has JID)
- **gateway**: advance agent cursor after delegation to prevent message replay on restart

### Refactored

- **store/groups.go**: full rewrite for folder-keyed CRUD, `JIDFolderMap()` for route-based JID resolution
- **gateway**: removed dead `isVoiceMime`, inlined `groupByFolderLocked` at 7 call sites, extracted `groupCols` const
- **onbod**: SQL updated for `groups` table; `isTier0` checks routes+groups join
- **timed**: `cleanupSpawns` queries routes for JIDs instead of groups table
- **dashd**: all queries updated for `groups` table
- **docs**: 20+ spec/architecture files updated for consistent `groups` naming

---

## [v0.21.1] — 2026-04-02

### Refactored

- **layout**: flattened `data/ipc/` to `ipc/` at project root; removed `DataDir`, added `IpcDir`
- **ipc**: removed legacy `/workspace/group/` path support (pre-v1, no compat needed)
- **gateway**: removed old cursor format migration in `loadState`
- **container**: removed NANOCLAW\_ env var cleanup loop (no old vars exist)
- **core**: removed dead `AuthUsername` config field

### Removed

- 7 nanoclaw/kanipi name references across Dockerfile, DEV.md, CLAUDE.md, TODO.md, docs
- TODO.md "remaining kanipi ports" section (all features already implemented)

### Changed

- **skills**: synced latest skills + CLAUDE.md to all deployed groups (REDACTED 5 + REDACTED 1)
- **docs**: updated ARCHITECTURE.md, MIGRATION.md, specs for new layout

---

## [v0.21.0] — 2026-04-02

Full daemon audit: 50+ bugs found and fixed across 25+ files. Skills audit and simplification.

### Fixed — Audit rounds 1-3

- **container**: parseBuf unbounded growth; large agent output could OOM the gateway
- **container**: seedSettings race condition; concurrent spawns for the same group could corrupt
  settings files
- **container**: copyFile did not check close error; silent data loss on full disk
- **container**: stdin.Write error swallowed; container could run with no/partial input
- **chanlib**: Chunk byte-split broke multi-byte Unicode sequences at chunk boundaries
- **auth**: extractBearer accepted any Authorization scheme, not just Bearer
- **store**: scanMessage ignored Scan errors, returning zero-value messages
- **onbod**: approve/reject not transactional; crash mid-operation left inconsistent state
- **timed**: fire race condition allowed duplicate task execution
- **store**: FlushSysMsgs not atomic; crash mid-flush could lose messages
- **teled**: `/files` endpoint had no auth; anyone could proxy Telegram files
- **gateway**: downloadFile had no HTTP timeout; slow upstream blocked enricher
- **chanreg**: All() returned shared pointers; callers could mutate registry state (data race)
- **proxyd**: per-IP rate limiter entries never evicted; memory leak over time
- **discd**: dropped attachment captions; made per-message API call instead of batching
- **router**: mdToHTML did not escape HTML entities; user input could inject HTML
- **gateway/spawn**: data race in spawnFromPrototype; `g.groups` map read without mutex
- **emaid**: SMTP header injection via unsanitized CRLF in `to` and `rootMsgID`
- **auth**: JWT injected into script tag without escaping (`%s` → `%q`)
- **gateway**: downloadFile leaves orphan file on error; also checked close error
- **emaid**: IMAP Store command result never awaited; `\Seen` flag could fail silently
- **auth**: loginLimiter memory leak; per-IP entries never evicted (added 10K cap)
- **compose**: yamlList doesn't escape single quotes; could produce invalid docker-compose.yml
- **emaid**: upsertThread tx.Exec errors discarded silently

### Fixed — Audit round 4

- **gateway**: silent delivery failure drops agent responses; now stores response on send error
- **proxyd**: raw AUTH_SECRET bypass removed; only JWTs accepted
- **gateway/spawn**: MaxChildren semantics unified with auth.IsDirectChild
- **container**: seedSettings wrote to wrong path (DataDir instead of GroupsDir); root agent
  never saw ARIZUKO_IS_ROOT
- **container**: stale NANOCLAW\_ env vars cleaned from settings.json on every spawn
- **proxyd**: vhosts rewrite path internally instead of 301 redirect; redirect caused infinite
  loop because browser keeps Host header

### Fixed — Skills audit (11 bugs across 9 skills)

- **skills/info**: migration version hardcoded as 1 instead of 51
- **skills/self**: MCP tools table missing 5 tools
- **skills/hello**: ARIZUKO_IS_WORLD_ADMIN env var never set by runner
- **skills/howto**: same ARIZUKO_IS_WORLD_ADMIN issue
- **skills/reload**: description said "gateway process" but kills container PID 1
- **skills/recall-messages**: get_history described as raw JSON IPC, is MCP tool
- **skills/compact-memories**: schedule_task examples used wrong param names
- **skills/web**: vite restart section referenced PID file inside another container
- **skills/infra**: referenced 301 redirect, now internal path rewrite
- **skills/acquire**: tilde in curl -F doesn't expand; relative Read path
- **skills/tweet**: referenced nonexistent examples/ directory

### Added

- **chanlib**: `NewAdapterMux` — shared adapter HTTP server (send, send-file, typing, health)
- **dbmig**: shared SQL migration framework extracted from onbod + timed
- **chanlib**: 13 handler tests
- **chanreg**: health endpoint test
- **discd**: bot/mentions tests
- **gateway**: impulse test, 8 makeOutputCallback/sendMessageReply tests
- **store**: sessions test
- **skills/migrate**: group discovery via `refresh_groups` MCP tool

### Changed

- **timed**: added SIGTERM handler for graceful shutdown
- **dashd**: added SIGTERM handler for graceful shutdown
- **proxyd**: added SIGTERM handler for graceful shutdown
- **teled, discd, mastd, bskyd, reditd**: converted to use `chanlib.NewAdapterMux`
- **grants**: uses `core.JidPlatform`, removed local duplicate

### Refined

- **skills**: 9 skills simplified — removed defensive checks, assume consistent state
  (recall-memories, recall-messages, self, info, hello, migrate, howto, web, infra)

### Removed

- **store**: dead code — `UnroutedMessages`, `RecentMessages`, `GroupMessages`,
  `SetMessageStatus` (unused)
- **auth**: dead code — `Middleware` function, `publicPrefixes`, `publicExact`

---

## [v0.20.2] — 2026-04-02

### Added

- **agent**: auto-recall on ambiguous tasks — agents run `/recall-memories` to disambiguate
  unclear references before asking the user to clarify

---

## [v0.20.1] — 2026-04-02

### Added

- **ipc/send_file**: `caption` param on `send_file` MCP tool; teled sends as native caption,
  whapd passes through, discd ignores
- **core**: `Channel.SendFile` takes `caption string` param (migration 050)
- **gateway**: inbound media attachment pipeline — enricher downloads attachments to
  `groups/<folder>/media/<YYYYMMDD>/` before container spawn; voice transcribed via Whisper
  when `VOICE_TRANSCRIPTION_ENABLED=true` and `WHISPER_BASE_URL` set
- **chanlib**: `InboundAttachment` struct; channel adapters populate `Attachments` field in
  inbound messages
- **teled**: serves `GET /files/{fileID}` proxy to Telegram CDN for attachment downloads
- **discd**: extracts attachment metadata from Discord message events
- **store**: migration 0019 adds `attachments TEXT` column to `messages`; `EnrichMessage(id, content)`
  updates content and clears attachments after enrichment
- **agent**: sees attachments as `<attachment path="..." mime="..." filename="..."/>` XML in
  message content (path is container-side `/home/node/media/...`)
- **skills**: `/dispatch` skill for task-level skill discovery and reconciliation
- **chanlib**: 17 tests covering RouterClient, Auth, Chunk, WriteJSON, WriteErr

### Fixed

- **container**: scope orphan cleanup and container names to instance; multi-instance deployments
  were killing each other's containers on startup
- **container**: mount GroupsDir at `/workspace/data/groups` for root containers; migrate skill
  was broken because path was never mounted
- **gateway**: log errors in `delegateToFolder` and `handlePrefixRoute` send paths; delivery
  callback tests added
- **gateway**: simplify message loop, retry logic, cache mention regex
- **gateway**: log `sendMessageReply` errors instead of discarding them
- **teled**: remove duplicate typing loop; gateway `keepTyping` already refreshes every 6s
- **teled**: capture reply-to threading from Telegram
- **vite**: bake `allowedHosts` config into Dockerfile; CLI flag not supported in Vite 8
- **skills**: remove dead `ARIZUKO_GROUP_FOLDER`/`TIER`/`CHAT_JID` env vars; fix NANOCLAW→ARIZUKO
  naming in skill files

---

## [v0.20.0] — 2026-03-27

### Fixed

- **router**: `chat_id` attr missing from `<message>` XML (spec N-memory-messages)
- **store**: `<system_message>` tag → `<system>` per spec Y-system-messages
- **ipc**: `once`/ISO-8601 schedule type in `schedule_task` MCP tool was silently broken
- **timed**: `task_run_logs.error` column never populated; `get_history` GetInt cast
- **webd**: anon sender was literal `"anon"` — now `anon:<ip-hash>` per spec W-slink
- **diary/episodes**: block-scalar YAML (`summary: |`, `summary: >`) parsed as empty — diary and episode context was silently dropped from agent prompts
- **mastd**: `follow`/`favourite`/`reblog` notifications were dropped; now mapped to correct verbs
- **bskyd**: all notifications delivered with empty verb; now set from `reason` field
- **reditd**: verb and topic not set; subreddit JID prefix wrong (`reddit:golang` → `reddit:r_golang`)
- **router**: `verb` route type never matched (missing `case "verb":` in routeMatches)
- **gateway**: sticky commands with embedded newlines now rejected

### Added

- **router**: `platform`, `verb`, `thread` attrs in FormatMessages XML per spec i-social-events
- **gateway**: `SEND_DISABLED_CHANNELS` / `SEND_DISABLED_GROUPS` env vars to suppress sends
- **ant/CLAUDE.md**: `<observed>` message guidance — watch-only routing context
- **dashd**: episodes, users, facts sections in memory dashboard
- **core**: migration 049 documenting `get_history` MCP tool for agents
- **specs**: phases 7+8 dissolved into correct phases; 88 files got frontmatter; LinkedIn channel specced

---

## [v0.19.2] — 2026-03-27

### Added

- **whapd: media inbound** — images, video, audio, voice notes, documents
  downloaded via Baileys `downloadMediaMessage` and forwarded to router as
  attachment fields. Media-only messages (no caption) deliver a description
  (`[Image]`, `[Voice Note]`, `[File: name]`).
- **whapd: LID→phone JID translation** — in-memory cache maps WhatsApp LID
  format to phone-based JIDs; required for modern WA accounts.
- **whapd: group metadata sync** — `groupFetchAllParticipating()` on connect
  - 24h refresh; group names passed to router on `sendChat`.
- **whapd: outbound message queue** — messages queued when disconnected,
  flushed on reconnect; `/send` returns `{ queued: true }` instead of 502.
- **whapd: `/send-file` endpoint** — send images, video, audio, documents
  back to users; accepts base64 `data` + `mime` + optional `filename`/`caption`.

---

## [v0.19.1] — 2026-03-27

### Fixed

- **whapd**: `saveCreds()` now awaited before closing socket on pairing —
  `creds.json` was 0 bytes on first pair, forcing repeated re-pair cycles.
- **whapd**: Bot loop guard — skip inbound messages whose `pushName` matches
  `ASSISTANT_NAME` (prevents agent self-reply loops in group chats).
- **whapd**: Read receipts — mark messages read after delivery so users don't
  see perpetual unread badges.
- **whapd**: Markdown→WhatsApp formatting on outbound send — `**bold**` →
  `*bold*`, `~~strike~~` → `~strike~`.
- **whapd**: `makeSocket()` now returns `saveCreds` for callers that need explicit flush.

---

## [v0.19.0] — 2026-03-27

### Changed

- **ant/ rename**: `container/agent-runner/` → `ant/`; `container/skills/` →
  `ant/skills/`; `container/CLAUDE.md` → `ant/CLAUDE.md`. Go spawn code stays
  in `container/`. The in-container agent is now called "ant".

- **Image rename**: `arizuko-agent:latest` → `arizuko-ant:latest` throughout
  config defaults, env.example, and generated `.env` on `arizuko create`.

- **Sessions path collapsed**: `data/sessions/<folder>/.claude/` merged into
  `groups/<folder>/.claude/`. Group folder mount at `/home/node` already covers
  `.claude/`; separate mount removed. Matches kanipi model.

- **SeedGroupDir at creation time**: Skills seeding (`seedSkills`) moved from
  `container.Run()` (every message) to group creation. `container.SeedGroupDir()`
  is now called from `arizuko group add`, `onbod /approve`, and the
  `register_group` MCP tool. `seedSettings()` stays in `Run()` for runtime
  values (grants, session ID, socat MCP config).

- **Service catalog embedded**: `template/services/*.toml` now embedded in
  `arizuko:latest` at `/opt/arizuko/template/services/`. Ansible extracts from
  image — no duplicate TOML files in role.

- **gated.sock**: MCP socket renamed from `router.sock` → `gated.sock`
  throughout docs, specs, and skills.

### Fixed

- MCP socket permissions: `ipc.go` now sets mode `0666` so agent (uid=1000)
  can connect without being blocked.
- `RegisterGroup` error was silently dropped with `//nolint` in `ipc/ipc.go`;
  now logged as warn.
- Dead `sessions/` volume mount for root groups removed from `BuildMounts()`.
- `cmdCreate` now calls `SeedGroupDir` instead of writing a static `CLAUDE.md`
  that diverged from what `cmdGroup add` and onbod produce.
- `groupRunnerDir` (copying `ant/src` on first run) removed — source is baked
  into the container image.

### Added

- **emaid IMAP IDLE**: Replaced 30s poll loop with RFC 2177 IMAP IDLE push;
  28-min safety timer for RFC-compliant reconnect. Eliminates latency and
  reduces load.
- **whapd**: `--pair <phone>` flag for phone-number pairing; QR via
  `qrcode-terminal`; exponential backoff on reconnect; exit on 405.
- **teled-REDACTED**: Service template for second Telegram bot in merged instance.
- **ant/ant**: Standalone CLI script (like dockbox) wrapping `docker run
arizuko-ant` with correct mounts for use outside arizuko.

---

## [v0.18.0] — 2026-03-26

### Added

- **Impulse gate**: Weight-based event batching per JID before agent wake-up.
  Messages accumulate weight; agent fires when threshold (default 100) is
  reached or 5-minute max-hold timeout expires. Social verb events (join/edit/
  delete) carry weight 0 so they don't trigger agents alone. Config via
  `defaultImpulseCfg()` in `gateway/impulse.go`.

- **Verb field on messages**: `core.Message` and `chanlib.InboundMsg` now carry
  a `verb` field (`"join"`, `"edit"`, `"delete"`, etc.; default `""`).
  Stored in SQLite via migration 0017. Channel adapters set `verb` on
  non-standard events; the impulse gate weights by verb type.

- **WebDAV via dufs**: `WEBDAV_ENABLED=true` in `.env` adds a `davd` service
  (`sigoden/dufs:latest`) that mounts `groups/` read-only. `proxyd` exposes
  `/dav/` as an auth-gated reverse proxy with path prefix stripping.
  Controlled via `DAV_ADDR` env in proxyd and `WEBDAV_ENABLED` in compose.

- **Social adapter service templates**: `template/services/mastd.toml`,
  `bskyd.toml`, `reditd.toml` — drop-in service definitions for Mastodon
  (port 9004), Bluesky (port 9005), and Reddit (port 9006) adapters.

- **Auth hardening**:
  - Login rate limiting: 5 attempts per 15 minutes per IP (sliding window,
    in-memory, resets on restart).
  - Secure cookie flag: `refresh_token` and OAuth state cookies set
    `Secure: true` when `WEB_HOST` (or `LISTEN_URL`) starts with `https://`.
  - Telegram replay protection: `auth_date` in widget payload must be within
    5 minutes of now; stale logins are rejected.
  - GitHub OAuth (`GITHUB_CLIENT_ID`/`GITHUB_CLIENT_SECRET`): login button
    shown when configured; optional org membership check via `GITHUB_ALLOWED_ORG`.
  - Discord OAuth (`DISCORD_CLIENT_ID`/`DISCORD_CLIENT_SECRET`): login button
    shown when configured.

- **Voice synthesis spec**: `ttsd` daemon design and `send_voice` MCP tool
  spec in `specs/8/6-voice-synthesis.md`. Open questions resolved.

### Fixed

- **Agent container CWD**: working directory changed from `/workspace/group`
  to `/home/node` (the mounted group folder). Diary paths, conversation
  archive paths, and `cwd` in agent queries all corrected.
- **Diary skill path**: updated from `/workspace/group/diary/` to `~/diary/`
  to match the corrected mount point.
- **Dockerfile mkdir**: removed stale `/workspace/group` from `mkdir -p`
  (no longer a mount target).

### Changed

- **`containerHome` constant**: extracted `/home/node` to a named constant in
  `container/runner.go` and `container/agent-runner/src/index.ts` to avoid
  repeated string literals.
- **License**: public domain (Unlicense). No restrictions. If you build on
  arizuko, acknowledge it — not because you have to, because that's how
  good work compounds.
- **README**: removed Japanese subtitle, added "Why arizuko" section explaining
  the real Claude Code CLI approach vs SDK wrappers.

---

## [v0.17.0] — 2026-03-23

### Added

- **`arizuko generate`**: new subcommand that writes `docker-compose.yml`
  without running the stack. Systemd units now use
  `docker run arizuko:latest arizuko generate <name>` (ExecStartPre) +
  `docker compose up` (ExecStart) — no host binary required. Compose
  version always matches the image.

- **Agent dev tooling**: `gh`, `duckdb`, `grpcurl`, `delta`, `semgrep`, and
  15+ more tools added to the agent container image.

### Fixed

- **Delta install**: switched from `.deb` to tarball binary to avoid `dpkg`
  dependency errors on the agent container build.

### Changed

- **Compose generator**: deduplicated `writeEnv` helper (was copy-pasted
  three times); removed comments that restated code.
- **Renamed throughout**: `webd` → `proxyd` in compose generator, systemd
  Ansible template, and docs.

---

## [v0.16.0] — 2026-03-23

### Fixed

- **Queue**: messages delivered to a running container via stdin injection
  (`SendMessage`) were silently dropped if the container died before
  processing them. `SendMessage` now sets `pendingMessages=true` so the
  next drain spawns a retry. `processGroupMessages` returning no messages
  is now treated as success (was incorrectly incrementing failure counter).

### Added

- **Web chat**: `web:` JID routing in gateway, per-topic agent runs
  (`processWebTopics`), `user_groups` table + `Groups` field in JWT,
  proxyd slink token resolution with per-IP rate limiting (10 req/min),
  `requireFolder` middleware in webd.
- **Structured logging**: info/debug log coverage across `gated`, `timed`,
  `gateway`, `ipc`, `chanlib`, `container/runner` — all routing decisions,
  MCP tool calls, container lifecycle events, channel registration, and
  task scheduling are now traceable from logs alone.
- **Specs**: agent-managed services (`servd`, specs/7/28), self-improvement
  loop (specs/7/29).

### Changed

- **MCP server name**: renamed from `nanoclaw` to `arizuko` in
  `ipc/ipc.go`, `container/runner.go`, and `agent-runner`. Tool names
  visible to agents are now `mcp__arizuko__*`.
- **Isolated container names**: `timed` now encodes the task ID in the
  sender field (`scheduler-isolated:<task_id>`); gateway builds container
  name as `arizuko-<folder>-task-<task_id>` instead of a timestamp.

---

## [v0.15.0] — 2026-03-21

### Changed

- **Build**: all daemons now build their binary in-place (own directory) via
  individual Makefiles. Root Makefile delegates uniformly via `DAEMONS` list.
  `gated`, `onbod`, `dashd`, `proxyd`, `timed` each gain their own `Makefile`.
- **CI**: workflow now sets up Go, installs and runs `pre-commit --all-files`,
  and runs `make lint` + `make test` across all packages.
- **`.gitignore`**: per-dir binary entries for all daemons (`onbod/onbod`,
  `dashd/dashd` were missing).

---

## [v0.14.0] — 2026-03-21

### Changed

- **Agent persona**: agents are now described as "unalive" — not alive, not
  alien. Removed ant/dead metaphors from bio, greeting, and README.
- **Howto skill**: replaced static 66 KB HTML template with `CONTENT.md`
  (20 sections as markdown) + `STYLE.md` (5-axis style generation guide).
  Agent now generates the page fresh each deployment with a unique visual
  style — palette, typography, density, decoration, and dark mode chosen
  from named archetypes or by imitating a given URL.

### Removed

- `.refs/` directory (175 files, ~8 MB of old nanoclaw and arizuka reference
  code) removed from repo and git history. Added to `.gitignore`.
- `docs/kanipi.html` — stale landing page from pre-rename.
- `template/web/pub/howto/index.html` — superseded by CONTENT.md + STYLE.md.

### Docs

- `CLAUDE.md`: noted `mastd`, `bskyd`, `reditd`, `chanlib`; CGO requirement
  for `gated`; single-test command pattern.

---

## [v0.13.0] — 2026-03-21

### Added

- **Email channel** (`emaid/`): IMAP TLS polling (every 30s) + SMTP STARTTLS replies; thread
  tracking via local SQLite; JID `email:<thread_id>`. Config: `EMAIL_IMAP_HOST`,
  `EMAIL_SMTP_HOST`, `EMAIL_ACCOUNT`, `EMAIL_PASSWORD`.
- **Mastodon channel** (`mastd/`): WebSocket streaming for mention notifications; posts and
  replies via REST API. Config: `MASTODON_INSTANCE_URL`, `MASTODON_ACCESS_TOKEN`.
- **Bluesky channel** (`bskyd/`): Polls AT Protocol notifications (replies + mentions) every
  10s; creates posts/replies via XRPC; session persistence. Config: `BLUESKY_IDENTIFIER`,
  `BLUESKY_PASSWORD`.
- **Reddit channel** (`reditd/`): OAuth2 password grant; polls inbox + configured subreddits
  every 30s; replies via `/api/comment`. Config: `REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET`,
  `REDDIT_USERNAME`, `REDDIT_PASSWORD`, `REDDIT_SUBREDDITS`.

- **Reply-to threading** (`core/`, `gateway/gateway.go`): `Channel.Send` signature
  is `Send(jid, text, replyTo string) (string, error)` — accepts a `replyTo`
  message ID and returns the sent message ID; gateway passes the last agent-sent
  message ID as reply context on each outbound send.
- **Chunk chaining** (`gateway/gateway.go`): `Send` returns the sent message ID;
  gateway chains `lastSentID` per agent run so multi-chunk replies thread correctly.
- **recall-memories / recall-messages skills** (`container/skills/`): `recall`
  skill renamed to `recall-memories`; new `recall-messages` skill added for
  message history lookup.
- **Google OAuth workspace hint** (`auth/oauth.go`): `hd=` parameter appended to
  Google OAuth redirect when `GOOGLE_ALLOWED_EMAILS` patterns share a single domain
  (e.g. `*@example.com`), restricting the sign-in picker to that workspace. Supports
  multiple patterns — hint only added when all share one domain.
- **`register_group` fromPrototype** (`ipc/ipc.go`, `gateway/gateway.go`): `register_group`
  now accepts `fromPrototype=true` to copy this group's `prototype/` directory into a new
  child folder (folder derived from jid); `name` is now optional (defaults to jid); merges
  the former `spawn_group` tool.
- **Agent-runner exits on empty IPC input** (`container/agent-runner/src/index.ts`):
  `waitForIpcMessage` (async Promise wrapper) replaced with synchronous
  `checkIpcMessage`; runner exits immediately when IPC input dir is empty,
  enabling L-chat-bound sessions.

### Changed

- `gateway/gateway.go`: `runAgentWithOpts` variadic `msgID ...string` collapsed
  to plain `msgID string`; 3 call sites updated. Removes optional-string ambiguity.
- `ipc/ipc.go`: `send_reply` handler nil-guard on `SendReply` removed; collapsed
  to single unconditional call (guard was dead — `SendReply` is never nil).
- `grants/grants.go`: `sortedKeys` uses `sort.Strings` instead of insertion sort;
  `nullStr` deduped to single definition.
- `auth/jwt.go`, `auth/identity.go`: `mintJWT`, `isInWorld` unexported (internal only).
- `router/router.go`: `EscapeXml`, `TimeAgo`, `StripThinkBlocks`, `SenderToUserFileID`,
  `ExpandTarget` unexported (internal only).

### Removed

- `store/outbound.go`: deleted (was empty file).
- `store/tasks.go`: `LogTaskRun`, `ListTaskRuns`, `TaskRun` removed (dead code,
  unused since timed daemon uses direct SQL).
- `core/config.go`: `VitePort` field removed (unused).

---

## [v0.12.0] — 2026-03-19

### Added

- **Kanipi skill sync** (`container/skills/`, `container/output-styles/`): 6 new
  agent skills (acquire, compact-memories, recall, specs, users, infra), 3 output
  style guides (discord, telegram, email). Agent migrations 015-043 added;
  `MIGRATION_VERSION` bumped 16 → 38. `hello` skill updated to comprehensive format.
  Sync head: fdbac9f.
- **REDACTED instance**: `REDACTED` running. Groups: root, REDACTED, happy.
  Port layout: gated 8081, dashd 8091, web 49165. Telegram bot: REDACTED.

### Changed

- **Compose container naming** (`compose/compose.go`): all services named
  `<app>_<daemon>_<flavor>` (e.g. `arizuko_gated_REDACTED`, `arizuko_teled_REDACTED`).
  Applies to built-in services (gated, timed, dashd) and user-defined services
  from `services/*.toml`. Prevents container name conflicts when multiple instances
  run on the same host.
- **Onbod in compose**: `onbod` auto-included when `ONBOARDING_ENABLED=true`.
  Compose sets `ONBOD_LISTEN_ADDR=:8092` to avoid conflict with `dashd` (`:8090`).

### Infrastructure

- **Ansible role** (`core/ansible/roles/arizuko-service/`): systemd unit generation
  from template. Instances declared as `arizuko_instances: [{flavor: REDACTED}, {flavor: REDACTED}]`
  in `host_vars`. Data dir and secrets are NOT managed by Ansible.

---

## [v0.11.0] — 2026-03-19

### Added

- **Google OAuth** (`auth/oauth.go`, `auth/web.go`, `auth/middleware.go`):
  `/auth/google` and `/auth/google/callback` routes. Login page gains a
  Google button when `GOOGLE_CLIENT_ID` env is set.
- **Prototype spawning** (`gateway/spawn.go`, `store/migrations/0010-prototype-spawn.sql`):
  when a route targets an unregistered folder and the parent group has a
  `prototype/` subdirectory, a child group is auto-created by copying the
  prototype. Groups gain `state`, `spawn_ttl_days`, `archive_closed_days`
  columns. Daily cleanup in `timed` marks idle spawns closed and archives
  them as `.tar.gz`.
- **Episode injection** (`container/episodes.go`, `container/runner.go`):
  `episodes/` YAML-frontmatter files in a group folder are read at session
  start and injected as `<episodes>` XML into the prompt.
- **Dashd** (`dashd/main.go`): operator dashboard daemon (HTMX, read-only
  SQLite). Pages: status, tasks, activity, groups, memory browser. Auth via
  existing JWT session cookie. Added to Makefile and compose generation.
- **Bot-mention guidance** (`container/CLAUDE.md`): agents always respond
  when @mentioned by name, stay silent otherwise.

## [v0.10.0] — 2026-03-18

### Added

- **Topic sessions** (`gateway/gateway.go`, `store/sessions.go`,
  `store/migrations/0008-topic-sessions.sql`): `#topic` prefix routes
  messages to isolated sessions within a group. `/new #topic` resets only
  that topic. `sessions` table gains a `topic` column; PK is now
  `(group_folder, topic)`.
- **Prefix routing** (`router/router.go`): new `prefix` route type. `@name`
  dispatches to a named group, `#topic` selects a topic session. Evaluated
  before `pattern` in tier order.
- **Grants engine** (`grants/grants.go`): `CheckAction`, `NarrowRules`,
  `MatchingRules`, `DeriveRules`. Rules are derived at container spawn
  (`container/runner.go`) and injected into `start.json`.
- **IPC grants integration** (`ipc/ipc.go`): MCP manifest filtered by grants
  rules so agents only see permitted tools. `set_grants`/`get_grants` tools
  added. `delegate_group` calls `NarrowRules` before persisting child rules.
- **Onboarding daemon** (`onbod/main.go`, `store/onboarding.go`,
  `store/migrations/0009-onboarding.sql`): state machine
  `awaiting_name → pending → approved/rejected`. Poll loop prompts users,
  validates names, notifies tier-0 operators. `/approve` and `/reject`
  commands handled via `/send` HTTP endpoint. On approval, creates group dir,
  inserts routes, sends welcome system event.

### Changed

- `gateway/commands.go`: `cmdText()` strips media placeholders and routing
  prefixes before command detection; `isGatewayCommand()` predicate added;
  `handleCommand()` uses `cmdText()` consistently.
- `gateway/gateway.go`: `processGroupMessages()` filters gateway commands
  from agent context (they are never forwarded to the container);
  `pollOnce()` includes unrouted JIDs when `ONBOARDING_ENABLED` is set;
  `insertOnboarding` hook seeds `onboarding` table for new unrouted JIDs.
- `store/groups.go`: `UnroutedChatJIDs(since time.Time)` returns chat JIDs
  with recent messages that have no entry in the routes table.
- `container/runner.go`: `seedSkills()` seeds `.claude.json` if missing
  (SDK requires it); takes `folder` param for stable userID hash.
- `core/config.go`: `OnboardingEnabled bool` field (`ONBOARDING_ENABLED` env).

## [v0.9.1] — 2026-03-17

Channel adapters, flat layout, dead code cleanup, container fix.

### Architecture

- **Flat layout**: services hoisted from `services/` to top-level dirs
  (`gated/`, `timed/`, `teled/`, `discd/`, `whapd/`). Each is a
  standalone program tree.
- **gated split**: gateway daemon is own binary at `gated/main.go`,
  no longer embedded in `cmd/arizuko/`. `arizuko run <instance>`
  generates compose and runs `docker compose up`.
- **Discord adapter** (`discd/`): Go, ~250 LOC. WebSocket events,
  mention rewriting, file sending. Registers via channel protocol.
- **WhatsApp adapter** (`whapd/`): TypeScript/baileys, ~270 LOC.
  QR auth, session persistence, reconnect. Shows multilang versatility.

### Fixed

- Container hangs 30min after output: timer resets to 5s after final
  output (identified by `newSessionId`), not the full idle timeout.

### Removed

- `mime/` package (never imported)
- `store.AllChats()`, `core.ChatInfo` (unused)
- `router.SpawnFolderName()` and related regexes (unused)

### Docs

- All docs updated for flat layout (no more `services/` prefix)
- Specs: discd/whapd status → running (was planned)
- ARCHITECTURE, README, CLAUDE.md aligned with current code

---

## [v0.9.0] — 2026-03-15

Docker compose orchestration, daemon isolation, comprehensive test
coverage, code refinement.

### Architecture

- **Docker compose deployment**: `arizuko compose <instance>` generates
  docker-compose.yml from `.env` + `services/*.toml`. Systemd runs
  `docker compose up` in foreground.
- **`arizuko run`**: gated-only gateway entrypoint. timed and teled are
  separate containers in the compose stack.
- **Single Docker image**: all five binaries (arizuko, gated, timed,
  teled, discd) built into one image, differentiated by entrypoint.
- **Daemon naming**: compose services use spec names (gated, timed, teled)
  with `container_name` for clean log prefixes.

### Changed

- `arizuko create` generates random CHANNEL_SECRET (was empty)
- `instanceDir()` helper replaces 4 repeated sprintf calls
- `delegateToChild`/`delegateToParent` collapsed into `delegateToFolder`
- `groupByFolder`/`groupJIDs` helpers extracted from duplicated loops
- Dead `"verb"` routing case removed from router
- ipc/auth reclassified as libraries (not daemons) in docs

### Tests

- 21 gateway tests (commands, routing, state, channels, system events)
- 20 container tests (sanitize, mounts, args, settings, output parsing)
- 15 timed tests (migration, fire, cron, concurrent dedup)
- Fixed concurrent test: shared-cache SQLite for in-memory multi-goroutine

### Docs

- CLAUDE.md, ARCHITECTURE.md, README.md aligned with deletions
- Service table shows type (daemon/library)
- Routing rules renumbered (verb tier removed)

---

## [v0.8.0] — 2026-03-15

Microservice architecture. Scheduler extracted to standalone daemon,
schema simplified, dead code removed, specs aligned with code.

### Architecture

- **services/timed/**: standalone scheduler daemon (~150 LOC), polls
  scheduled_tasks, inserts into messages. Zero dependencies on gateway.
  Own migration runner (service name: `timed`).
- **Daemon specs**: gated (9), timed (8), actid (10), auth (11) —
  one spec per daemon with clear table ownership.
- **0-architecture.md**: lean service overview replacing 579-line monolith.

### Breaking: scheduled_tasks schema

- `group_folder` → `owner`
- `schedule_type` + `schedule_value` → `cron` (nullable, NULL = one-shot)
- Removed: `context_mode`, `last_run`, `last_result`
- Removed: `task_run_logs` table
- Migration renumbered (0003-0004)

### Removed

- `scheduler/` package (embedded in gateway, replaced by services/timed/)
- `actions/` package (dead code, unused)
- `store.DueTasks()`, `store.LogRun()`, `store.AllTasks()`,
  `store.UnreportedRuns()`, `store.MarkRunsReported()` (old scheduler methods)
- `container.Input.IsTask` field (dead)
- `core.Task.SchedTyp`, `SchedVal`, `CtxMode` fields (old schema)
- `gateway.formatTaskRuns()` (used removed task_run_logs)

### Changed

- `schedule_task` MCP tool: takes `targetJid`, `prompt`, `cron` (optional).
  No more `schedule_type`/`schedule_value`/`context_mode` params.
- `store.UpdateTask`: consolidated from two SQL queries to one.
- `store.ListTasks`: replaced duplicate `AllTasks()`.

### Tests

- 10 timed daemon unit tests (migration, poll, cron, one-shot, paused, future)
- 12 microservice contract integration tests (schema compat, message insertion,
  task lifecycle, table ownership isolation)
- 16 store task edge case tests (duplicates, nonexistent, empty patch, filters)

### Specs

- Tool names aligned with code: `delegate_group`, `reset_session`,
  `get_routes`/`set_routes`/`add_route`/`delete_route`
- Parameter names: camelCase (`targetJid`, `taskId`)
- actid/auth marked as design (currently inline in gated)

---

## [v0.7.0] — 2026-03-07

Go rewrite. All core gateway functionality ported from TypeScript.
16 packages, ~4,700 LOC Go (vs ~9,400 LOC TS).

### Packages

- **core/** — Config, types (Message, Group, Task, Channel interface)
- **store/** — SQLite persistence (12 tables, WAL mode, PRAGMA user_version migrations)
- **gateway/** — Main loop, message routing, commands (/new, /ping, /chatid, /stop)
- **container/** — Docker spawn, 8 volume mount types, MCP sidecar lifecycle, skills seeding
- **queue/** — Per-group concurrency, stdin piping, circuit breaker (3 failures)
- **router/** — XML message formatting, 5-tier routing rules, outbound filtering
- **ipc/** — File-based request/reply + legacy fire-and-forget, SIGUSR1 wake
- **scheduler/** — Cron/interval/once task runner (robfig/cron), run logging
- **diary/** — YAML frontmatter diary annotations with age labels
- **groupfolder/** — Group path resolution and validation
- **mountsec/** — Mount allowlist validation (blocked patterns, read-only enforcement)
- **runtime/** — Docker binary abstraction, orphan cleanup
- **logger/** — slog JSON handler init
- **cmd/arizuko/** — CLI entrypoint (run, create, group subcommands)

### Features

- Per-chat error tracking with cursor rollback on agent failure
- Forward/reply message metadata (forwarded_from, reply_to_text, reply_to_sender)
- MCP sidecar management (start/stop/settings wiring via Unix sockets)
- Gateway capabilities manifest (.gateway-caps TOML)
- Per-channel output styling (outputStyle in settings.json)
- Diary annotations injected into agent context
- Docker-in-docker path translation via HOST_DATA_DIR/HOST_APP_DIR

### Not ported

- Channel adapters (telegram, discord, whatsapp, email) — Channel interface exists
- Action registry (unified action system with typed schemas)
- Web proxy + auth layer
- MIME enricher (attachment pipeline)
- Slink web channel

---

## TypeScript releases (pre-Go rewrite)

---

## [v0.6.3] — 2026-03-06

### Features

- Media-aware file sending: telegram routes photos/videos/audio/animations
  to native API methods (inline display); whatsapp routes by MIME type
- Diary spec: Stop hook nudge after 100 turns, task tracking in entries,
  terse summary format

### Fixes

- Replace agent error retry loop with circuit breaker (manual retry only,
  warns after 3 consecutive failures per group)
- Telegram: removed dead `method` variable in sendDocument

---

## [v0.6.2] — 2026-03-06

### Features

- Telegram: images (PNG/JPG/GIF/WEBP) sent via `sendPhoto` for inline
  display instead of `sendDocument` (file attachment)

### Fixes

- Agent CLAUDE.md: `send_file` no longer prompts follow-up text description

---

## [v0.6.1] — 2026-03-06

### Fixes

- Container stop: `exec()` → `execFileSync`/`spawn` (no shell anywhere)
- Command handlers: `await` instead of fire-and-forget (race condition)
- Cursor rollback: restore cursor on agent error when no output was sent
  (previousCursor was saved but never used — messages in DB but invisible)
- Routing schema: `.max(200)` on pattern/sender Zod fields (was only
  enforced at runtime, silent failure)
- Sidecar socket cleanup: catch only ENOENT (was `catch {}`)
- Agent container: use `bunx tsc` for build, validate-only compile step

---

## [v0.6.0] — 2026-03-06

### Fixes

- IPC: catch only ENOENT on file cleanup (was swallowing all errors)
- IPC: validate envelope id/type fields, reject malformed requests
- IPC: delete failed files instead of accumulating in errors/ dir
- Routing: cap regex pattern length at 200 chars (ReDoS mitigation)
- Config: validate TIMEZONE via Intl.DateTimeFormat, fallback to UTC
- Sidecar: use spawn() instead of exec() for lifecycle (shell injection fix)

### Features

- **Hierarchical group routing**: parent groups delegate to children via
  routing rules (command, pattern, keyword, sender, default). Authorization
  enforces same-world, direct parent-child only. Max delegation depth 3.
- **Sidecar isolation**: per-group MCP sidecars via `SIDECAR_<NAME>_IMAGE`
  env vars. Socket transport at `/workspace/ipc/sidecars/<name>.sock`.
  Gateway manages lifecycle (start, probe, reconcile settings, stop).
- **Action input validation**: Zod schemas on all actions; malformed
  IPC requests rejected with typed error replies.
- **New actions**: `delegate_group`, `set_routing_rules`
- **Session history**: `session_history` table replaces `sessions`;
  new-session injection includes last 2 previous sessions

---

## [v0.5.0] — 2026-03-06

### Features

- **Action registry**: unified action system — all IPC handlers, MCP
  tools, and commands reference a single `Action` interface with typed
  Zod schemas and authorization. `src/action-registry.ts` + `src/actions/`
- **Request-response IPC**: agents write to `requests/`, poll `replies/`.
  Gateway dispatches through action registry and writes typed replies.
  Fire-and-forget IPC retained for backwards compat during rollout.
- **Tool discovery**: gateway writes `action_manifest.json` at spawn
  time. Agent MCP server reads manifest for dynamic tool registration.
- **Agent MCP self-registration**: agent-written `mcpServers` in
  `settings.json` are merged with built-in `nanoclaw` server.
  Dynamic `allowedTools` includes `mcp__<name>__*` wildcards.
- **Message threading types**: `SendOpts { replyTo }` on Channel
  interface, `replyTo` field on `NewMessage`

### Breaking

- `processTaskIpc` moved from `ipc.ts` to `ipc-compat.ts`
- IPC handlers refactored into `src/actions/` modules

---

## [v0.4.0] — 2026-03-06

### Breaking

- `NANOCLAW_IS_MAIN` env var → `NANOCLAW_IS_ROOT`
- `/workspace/global` mount → `/workspace/share`
- `isMain` removed from `ContainerInput` interface

### Changes

- `isMain` → `isRoot(folder)` — structural check (`!folder.includes('/')`)
  replaces hardcoded `MAIN_GROUP_FOLDER = 'main'` comparison
- `groups/global/` → `groups/<world>/share/` — shared state lives inside
  world root, always mounted (rw for root, ro for children)
- Folder validation allows `/` separator for future hierarchy
- Reserved folder `global` → `share`

---

## [v0.3.0] — 2026-03-06

### Features

- **System messages**: `system_messages` and `sessions` DB tables. Gateway
  enqueues context annotations (new-session history, new-day marker, command
  context) and flushes them as XML before user messages in agent stdin.
- **Session recording**: every container spawn/exit recorded in `sessions`
  table with timing, message count, result, and error. New-session injection
  includes last 2 previous sessions as `<previous_session>` XML elements.
- **Command registry** (`src/commands/`): pluggable handlers replace
  hardcoded telegram commands. `/new` (session reset with continuity),
  `/ping`, `/chatid` shipped. Commands intercepted in message loop before
  agent routing.
- **`reset_session` IPC**: agent can clear its own session via IPC message.
- **Error notification**: on agent error, user receives retry prompt and
  message cursor rolls back. If output was already sent, cursor is preserved
  to prevent duplicate delivery.
- **Agent SKILL.md**: documents system message origins, session history
  access (`~/.claude/projects/`), group configuration files, whisper
  language config. Migrations 005-007.
- **agent-runner CLAUDE.md**: session layout documentation for in-container
  agent.

### Fixes

- System message format corrected (origin+event attributes, no colon).
- Voice transcription label now `[voice/auto→en: ...]` (was `[voice: ...]`).

---

## [v0.2.8] — 2026-03-05

### Features

- Agent self-skill documents session history access (`~/.claude/projects/`)
  and `.whisper-language` group configuration file.
- Migration 005: whisper language config docs. Migration 006: session history.

### Fixes

- System message format corrected in specs/SKILL.md (origin+event, no colon).
- Voice transcription label now `[voice/auto→en: ...]` (was `[voice: ...]`).

---

## [v0.2.7] — 2026-03-05

### Fixes

- **Voice transcription in active sessions**: second voice message in a
  running container session was missing transcription. Root cause: message
  objects fetched before `waitForEnrichments`, then used stale after wait.
  Both dispatch paths (new container + stdin pipe) now re-fetch from DB
  after enrichment completes, so voice/video content is always included.
- IPC drain race: concurrent `drainGroupMessages` calls for same group
  caused duplicate file sends. Fixed with per-group boolean lock.

### Features

- Whisper large-v3 model for better multilingual accuracy.
- Per-group language configuration via `.whisper-language` file.
- Parallel transcription passes: auto-detect + each configured language.
  Output labeled `[voice/auto→{detected}]` or `[voice/{forced}]`.
- Sidecar returns detected language in response; whisper.ts returns
  `WhisperResult { text, language }`.
- Whisper timeout increased to 60s for large-v3 multi-pass.

### Testing

- `src/mime-enricher.test.ts`: 7 tests covering enrichment pipeline,
  race condition (fast-settling enrichment before wait), error swallowing.
- `src/mime-handlers/voice.test.ts`: updated for multi-pass labels and
  `WhisperResult` return type.
- `src/mime-handlers/whisper.test.ts`: updated for `WhisperResult`,
  60s abort timeout.
- `specs/2/2-autotesting.md`: test strategy for all subsystems.

---

## [v0.2.6] — 2026-03-04

### Testing

- `vitest` added as devDependency; `make test` and npm scripts use bare
  `vitest run` (no npx/bunx wrapper)
- `src/config.test.ts`: live-binding assertions for config overrides;
  `_resetConfig()` restores defaults from env in `afterEach`
- `container-runner.ts`: `export let _spawnProcess = spawn` seam allows
  mocking docker without a running daemon
- Fixed container-runner test mocks: missing `HOST_APP_DIR`/`WEB_HOST`
  constants; `readFileSync` mock returning `''` now returns `'{}'`
- `specs/1/b-testing.md`: all testability gaps marked shipped

### Config

- 7 constants changed `const` → `let` in `config.ts`: `SLINK_ANON_RPM`,
  `SLINK_AUTH_RPM`, `WHISPER_BASE_URL`, `VOICE_TRANSCRIPTION_ENABLED`,
  `VIDEO_TRANSCRIPTION_ENABLED`, `MEDIA_ENABLED`, `MEDIA_MAX_FILE_BYTES`
- `_overrideConfig` mutates live bindings directly (was partial)
- `_resetConfig()` added to restore defaults from env; both gated behind
  `NODE_ENV=test`

---

## [v0.2.5] — 2026-03-04

### Gateway

- Fix `hostPath()` to replace `PROJECT_ROOT` instead of `APP_DIR`, fixing
  wrong host mount paths for IPC/session dirs when running inside Docker
- Fix `ipc.ts` file sending: use `HOST_GROUPS_DIR` (host path) instead of
  `GROUPS_DIR` (container-internal path), fixing ENOENT on `sendDocument`

### Skills

- Auto-migration nudge: gateway prepends annotation to agent prompt when
  group skills are behind `MIGRATION_VERSION`
- `MIGRATION_VERSION` bumped to 4

### Specs

- All `specs/1/` marked with shipped/partial/open status
- `specs/1/X-sync.md` rewritten as solved

### Cleanup

- Delete stale `template/workspace/mcporter.json` artifact
- Fix stale template path in `container/skills/howto/SKILL.md`

---

## [v0.2.4] — 2026-03-04

### CLI

- `arizuko config <instance> user list|add|rm|passwd` for local user management;
  passwords hashed with argon2; values passed via env vars to prevent shell injection

### Auth

- `POST /auth/refresh`: token rotation — issues new access + refresh token pair,
  invalidates old refresh token
- `POST /auth/refresh` JWT now carries correct user name (was using sub string)
- OAuth providers deferred to `specs/v3/auth-oauth.md`

### Specs

- `specs/1/3-auth.md`: updated to reflect v1 implementation

---

## [v0.2.3] — 2026-03-04

### Gateway

- Email channel: IMAP IDLE loop with SMTP reply threading, routes to main
  group; enabled by `EMAIL_IMAP_HOST` config
- `send_file` Discord support: `sendDocument` via `AttachmentBuilder`
- `send_file` WhatsApp support: `sendDocument` via baileys document message
- `src/mime.ts`: shared `mimeFromFile()` helper using file-type (magic bytes)
- `email_threads` table in DB: `getEmailThread`, `getEmailThreadByMsgId`,
  `storeEmailThread` for SMTP reply threading
- Explicit `DATA_DIR`/`HOST_DATA_DIR`/`HOST_APP_DIR` env vars replace brittle
  `/proc/self/mountinfo` host-path detection; gateway cwd stays at `/srv/app`

### Agent skills

- Migration 004: enforce `send_file` for file delivery (CLAUDE.md rule);
  `send_file` accepts any `/workspace` path, not restricted to `media/`

---

## [v0.2.2] — 2026-03-04

### Gateway

- Outbound file sending: `send_file` MCP tool lets agents send files to users
  as document attachments (Telegram); IPC `type:'file'` handler with
  path-safety check against GROUPS_DIR
- Session error eviction: on agent error output, session ID is not persisted;
  on error status, the session pointer is removed from DB (JSONL kept on disk)
  so the next retry starts a fresh session rather than re-entering a corrupted one
- Inject `NANOCLAW_IS_MAIN` into agent `settings.json` on every spawn (was
  never set, so agents always saw it as empty)

### Agent skills

- `migrate` skill: replace `/workspace/global` dir-existence check with
  `NANOCLAW_IS_MAIN != 1` check — the dir always exists due to Dockerfile
  mkdir, making the old check unreliable for main-group detection

---

## [v0.2.1] — 2026-03-04

### Agent runner

- Progress updates: every 100 SDK messages, emits last assistant text snippet
  to the channel so users see activity on long runs
- `error_max_turns` recovery: resumes the session with `maxTurns=3` and asks
  Claude to summarise what was accomplished and what remains, then prompts the
  user to say "continue"

---

## [v0.2.0] — 2026-03-04

### Slink web channel

- Added `POST /pub/s/:token` endpoint — web channel for groups registered as `web:<name>`
- Served `REDACTED.js` client widget at `/pub/REDACTED.js`
- Verified JWT signatures (HS256) for authenticated senders
- Added anon/auth rate limiting via `SLINK_ANON_RPM` / `SLINK_AUTH_RPM` config
- Supported `media_url` attachments with MIME type guessing
- Added SSE stream at `/slink/stream` for agent-to-browser push
- Added `slink_token` column on `registered_groups`; added `generateSlinkToken` helper
- Fixed expired JWT treated as anon (now returns 401)
- Fixed slink deduplication and SSE error logging

### Auth layer

- Added auth DB schema: `users`, `sessions`, `oauth_accounts` tables
- Added auth query functions: `createUser`, `getUserByProvider`, `createSession`, etc.
- Added `AUTH_SECRET` config constant for JWT signing
- Added web UI auth spec at `specs/1/3-auth.md`

### Whisper sidecar

- Added self-contained `arizuko-whisper` docker image, deployed via Ansible
- Added `whisperTranscribe` helper with 30s abort timeout
- Updated voice and video handlers to use shared whisper endpoint

### Mime pipeline

- Added attachment enrichment before agent dispatch
- Added handler registry: voice, video, image handlers
- Dispatched handlers in parallel with `allSettled` (partial failure safe)
- Added MIME type detection, file save, and annotation lines

### Workspace and agent identity

- Mounted `/workspace/self` read-only to expose full arizuko source to agent
- Replaced `SOUL.md` with ElizaOS-style `character.json`
- Added per-query field randomisation and global override merge in `character.json`
- Split `web/pub/` as unauthenticated boundary; `/pub/` prefix is public

### Skills and migrations

- Added `self` skill: agent introspection — layout, skills, channels, migration version
- Added `migrate` skill: main-group skill sync + migration runner across all groups
- Added migration system: `container/skills/self/migrations/` with versioned files
- Added migration 001: move `web/` root files to `web/pub/` per new layout convention
- Added YAML frontmatter to `web/SKILL.md`
- Updated `info/SKILL.md` to report migration version and warn if migrations pending

### Build

- Added `container/Makefile` for `arizuko-agent` image builds
- Added `sidecar/whisper/Makefile` for `arizuko-whisper` image builds
- Root `make image` now builds only the gateway (`arizuko`)

### Testing

- Added testability seams: `_initTestDatabase`, `setDatabase`, `_overrideConfig`
- Reached 306 tests across 22 files

---

## [v0.1.2] — 2026-03-01

### Added

- Signal-driven IPC: gateway sends SIGUSR1 after writing IPC file; agent
  wakes immediately, falls back to 500ms poll — eliminates busy-waiting

### Fixed

- Race condition in wakeup/timer assignment in agent IPC polling
- `cleanupOrphans` dual-filter restored to OR logic (AND regression in v0.1.1)
- Typing indicator now stops correctly when agent finishes responding
- Extracted `signalContainer` and `scanGroupFolders` helpers to deduplicate
  signal-sending logic

---

## [v0.1.1] — 2026-03-01

### Added

- Skills consolidated into `container/skills/`; seeded once per group on
  first container run
- Vite web server integrated into gateway startup via IPC restart
- Web app template seeded from `template/web/` on `arizuko create`
- Group management CLI (`arizuko group list|add|rm <instance>`)
- `hello` and `howto` skills bundled in agent image
- Pre-commit hooks: prettier, typecheck, hygiene (`.pre-commit-config.yaml`)
- Makefile targets: `build`, `lint`, `test`
- Discord channel via discord.js (`channels/discord.ts`)
- Env-based channel toggling: Telegram by `TELEGRAM_BOT_TOKEN`, Discord by
  `DISCORD_BOT_TOKEN`, WhatsApp by `store/auth/creds.json` presence

### Changed

- `TELEGRAM_ONLY` flag removed; channel selection is token/credential-driven
- Unified `ChannelOpts` type across all three channel modules

### Fixed

- Render markdown as HTML in Telegram; keep typing indicator alive during
  long responses
- Agent-team subcontainers cleaned up on gateway startup
- Fallback to script-relative template dir when not running inside container
- Docker-in-docker mount paths and agent container write permissions
- Bootstrap chicken-and-egg: `group add` now creates DB schema if missing
- `appDir` used for skills source path instead of `process.cwd()`

---

## [v0.1.0] — 2026-03-01

Initial arizuko release — nanoclaw fork with Telegram support and
multitenant instance model.

### Added

- Fork of nanoclaw at upstream v1.1.3
- Telegram channel (`channels/telegram.ts` via grammy)
- `arizuko` bash entrypoint: `create`, `group`, and instance-run commands
- Per-instance data layout: `/srv/data/arizuko_<name>/`
- systemd unit file templating via `arizuko create <name>`
- `container/agent-runner/` in-container Claude Code entrypoint
- Docker-in-docker host path translation (`detectHostPath()` via
  `/proc/self/mountinfo`)

### Inherited from nanoclaw v1.1.x

- Mount project root read-only (container escape prevention)
- Symlink and path-escape blocking in skills file ops
- `fetchLatestWaWebVersion` to prevent WhatsApp 405 failures
- Host timezone propagation to agent container
- `assistantName` passed to agent (was hardcoded as `'Andy'`)
- Idle preemption correctly triggered for scheduled tasks
