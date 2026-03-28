# Issues

## Critical (security/data)

### 1. Sensitive credentials in repo ✅ RESOLVED

**File**: `0xaida.py`
**Issue**: Twitter/X session cookies exposed in untracked file
**Impact**: Session hijacking risk if accidentally committed
**Resolution**: Deleted file, added `*.py` to .gitignore

## High (build artifacts)

### 2. Build artifacts not gitignored ✅ RESOLVED

**Files**: `proxyd/proxyd`, `proxyd/webd`
**Issue**: Compiled binaries present in working dir, not in .gitignore
**Impact**: Repo bloat, merge conflicts
**Resolution**: Added to .gitignore (proxyd/proxyd, proxyd/webd patterns)

## Medium (documentation)

### 3. Spec mismatch: dashd implementation status ✅ RESOLVED

**File**: `specs/7/25-dashboards.md`
**Issue**: Spec said "daemon shell only, no HTML templates yet" but dashd/main.go contains working inline HTML templates for all 5 dashboard pages
**Impact**: Misleading documentation
**Resolution**: Updated spec to "shipped (partial)" with accurate implementation summary

### 4. Distillation artifact untracked ✅ RESOLVED

**File**: `.distill/final.md`
**Issue**: Distillation doc not gitignored
**Impact**: Workspace clutter
**Resolution**: Deleted .distill/ directory, added to .gitignore

### 5. Extension sidecar status unclear ✅ RESOLVED

**File**: `specs/7/2-extensions.md`
**Issue**: Spec marked "planning" with "sidecar pending" note, but container/sidecar.go fully implemented
**Impact**: Misleading documentation
**Resolution**: Updated spec to "shipped (partial)" — sidecars working, plugins deferred

## Low (partial implementations)

### 6. Control chat multi-op notifications pending

**File**: `specs/7/20-control-chat.md`
**Issue**: Spec marked "shipped (partial)" - multi-operator support deferred
**Impact**: Single-operator limitation
**Status**: Design complete, intentionally deferred

### 7. Missing test coverage for daemons

**Files**: `gated/`, `onbod/`, `dashd/`, `proxyd/`, `webd/`, `discd/`
**Issue**: 6 daemon packages have no test files
**Impact**: Changes to daemons lack test safety net
**Priority**: Low (integration tests in tests/ cover most flows)
**Recommendation**: Add unit tests for daemon-specific logic when refactoring

### 8. Dashboard advanced features pending

**File**: `specs/7/25-dashboards.md`
**Issue**: Basic dashboards work but spec lists pending features (banner health, expandable sections, error details, onboarding section, flow viz, route editor)
**Impact**: Limited operator visibility into system state
**Priority**: Low (basic monitoring works)
**Status**: Tracked in spec, implementation deferred

---

## Daemon audit (2026-03-28)

Full audit of test coverage, dead code, redundancy, and boundary violations.
Tags: [TEST-GAP] [DEAD-CODE] [REDUNDANT] [BOUNDARY-LEAK]

### teled

**[TEST-GAP]** `teled/server_test.go`:

- `bot.send` error → 502 not tested
- `bot.sendFile` error → 502 not tested
- Missing `chat_jid` in `/send-file` → 400 not tested
- Missing `file` multipart field → 400 not tested
- Malformed JSON body → 400 not tested
- `testHandler` (server_test.go:19) defined but never called — dead test helper

**[BOUNDARY-LEAK]** `teled/bot.go`:

- Markdown-to-HTML conversion (`mdToHTML`, bot.go:167) — belongs in router, not adapter
- Message chunking at 4096 chars (bot.go:169,182) — gateway should split before send
- `@mention` rewriting (bot.go:119-128) — routing/preprocessing, not adapter concern

### discd

**[TEST-GAP]** `discd/server_test.go`:

- `bot.send` error → 502 not tested
- `bot.sendFile` error → 502 not tested
- Invalid multipart in `/send-file` → 400 not tested
- Malformed JSON in `/send` → 400 not tested
- Rate-limit 429 retry (bot.go:112-127) not tested

**[BOUNDARY-LEAK]** `discd/bot.go`:

- Mention rewriting (bot.go:71-78) — routing logic, not adapter
- Attachment formatting as `[Attachment: filename]` (bot.go:64-66) — router concern
- Topic extraction from thread ID (bot.go:80-83) — routing layer
- Message chunking at 2000 chars (bot.go:105) — gateway should split before send

### mastd

**[TEST-GAP]** `mastd/`:

- `postStatus` error → 502 not tested
- `ReplyTo` field sent but result not verified
- Malformed JSON → 400 not tested
- Streaming error / disconnection in `streamOnce` (client.go:34-54) untested
- Notification types "favourite", "reblog", "follow" not tested (client.go:114-159)

**[DEAD-CODE]** `mastd/server.go:31` — `ThreadID` field accepted, never used

### bskyd

**[TEST-GAP]** `bskyd/`:

- `createPost` error → 502 not tested
- Malformed AT URI in `getPostCID` (client.go:223-241) untested
- Auth refresh chain exhaustion in `xrpcAuth` (client.go:243-252) untested
- Null/missing Author or Record in `fetchNotifications` (client.go:135-159) — potential nil panic, untested
- Poll failure propagation (client.go:101-112) untested

**[DEAD-CODE]** `bskyd/server.go:31` — `ThreadID` field accepted, never used (same as mastd)

### reditd

**[TEST-GAP]** `reditd/`:

- `comment` vs `submit` branching in `handleSend` (server.go:37-41) not tested
- `doWithRetry` exhaustion after 3 retries (client.go:125-145) not tested
- Malformed `Retry-After` header parsing (client.go:134-138) not tested
- `handleThing` content assembly with empty body+title+selftext (client.go:242) not tested

**[DEAD-CODE]** `reditd/client.go:27-28` — `cursors`/`skipFirst` in-memory only; daemon restart loses state causing re-polls

**[REDUNDANT]** `reditd/main.go:29` — `rc2` (Reddit client) vs `rc` (router client) in same scope; confusing

### emaid

**[TEST-GAP]** `emaid/`:

- `bot.sendReply` error → 502 not tested
- IMAP login, SELECT, SEARCH failure paths (imap.go:80-204) not tested
- IMAP IDLE reconnect timer (imap.go:97-98, 28-minute) not tested
- `extractPlainText` MIME parse failure fallback (imap.go:311-330) not tested
- `rc.SendChat` error swallowed (imap.go:288) — untested silent failure

**[BOUNDARY-LEAK]** `emaid/imap.go`:

- Email thread/In-Reply-To chain assignment (imap.go:240-269) — routing concern, not adapter
- Email content formatting `From: X\nSubject: Y\n...` (imap.go:284-285) — router/formatter
- MIME preference (text/plain over HTML, imap.go:311-330) — display preference, not adapter
- IMAP SEEN flag before delivery confirmation (imap.go:302-306) — message marked seen even if router delivery fails
- IDLE vs poll fallback strategy (imap.go:32-55) — should be config, not adapter-internal

### gated (gateway)

**[TEST-GAP]** `gateway/`:

- `findChannel` returning nil mid-processing (gateway.go:760-762) not tested
- Agent completing with no text output — `hadOutput=false` (gateway.go:486-488) not tested
- Send suppression + impulse state: suppressed sends don't clear impulse weight (gateway.go:321-326 vs 568-569)
- `delegateToChild`/`delegateToParent` with suppression active (gateway.go:1190-1195 bypasses gate)
- `max_children=0` branch in spawn.go:30 not tested
- Impulse concurrent accept+flush race (impulse.go:95-97)
- No integration test: timed fires task into group mid-processing
- No integration test: onbod approval → gateway picks up new group

**[BOUNDARY-LEAK]** `gateway/gateway.go`:

- Routing logic duplicates `router.ResolveRoute()` (gateway.go:303-318, 402-410)
- Grant derivation mixed into message processing (gateway.go:623-625)
- Last-reply tracking via store calls in message loop (gateway.go:559, 588) — router concern
- Sticky routing managed by gateway, not router (gateway.go:919-993)

**[BOUNDARY-LEAK]** `timed/main.go`:

- Spawn archiving/TTL management (main.go:262-341) — gateway/lifecycle concern, not scheduler
- Direct query of `registered_groups` config fields (main.go:265) — group config concern

### onbod

**[TEST-GAP]** `onbod/main.go` — **zero test coverage** on 586-line file:

- State machine transitions (awaiting_name → pending → approved/rejected) untested
- HTTP `/send` auth failure untested
- `isTier0` permission check untested
- Name validation regex (line 33) and collision detection (line 503) untested
- Entire approval action (main.go:286-366, 80 lines) untested
- `seedDefaultTasks` (main.go:575-585) untested

**[BOUNDARY-LEAK]** `onbod/main.go`:

- Direct DB writes to `registered_groups` (line 318-323) — gateway owns group creation
- Direct route seeding into `routes` table (line 226-231, 331-340) — router/gateway
- Direct call to `container.SeedGroupDir` (line 326) — gateway method
- Permission check via direct DB query (line 279-283) — belongs in auth/
- Hardcoded default task list (line 575-585) — should be template file in `template/`

**[REDUNDANT]** `onbod/main.go:165-221` — migration code nearly identical to `timed/main.go:203-259`

### dashd

**[TEST-GAP]** `dashd/main.go` — **zero test coverage**:

- All routes (`/dash/`, `/dash/status/`, `/dash/tasks/`, `/dash/activity/`, `/dash/groups/`, `/dash/memory/`) untested
- JWT auth gate (requireAuth, main.go:97-115) untested
- Path traversal guard in `renderMemorySection` (main.go:354-404) untested
- DB query errors silently swallowed, render `<nil>` (main.go:176-201)

**[REDUNDANT]** `dashd/main.go:97-115` — reimplements JWT validation; dashd uses `auth.VerifyJWT()` but no raw-secret bypass, while proxyd adds one — inconsistent auth posture

### proxyd

**[TEST-GAP]** `proxyd/main.go` — **zero test coverage**:

- Auth gate (requireAuth, main.go:327-361) untested
- OAuth callback flow untested
- `/slink/*` rate limiter, token lookup, header injection (main.go:287-313) untested
- Vhost pattern matching (main.go:123-140) untested
- `/dav` prefix rewrite (main.go:62-74) untested
- If store is nil but token matches, proxies without group headers (main.go:301) — silent, untested

**[BOUNDARY-LEAK]** `proxyd/main.go`:

- Opens own DB connection (line 376) instead of gateway API — WAL contention risk
- Hosts OAuth handlers directly (line 225-241: `auth.RegisterRoutes`) — tight coupling

**[REDUNDANT]** `proxyd/main.go:327-361` — auth reimplemented with raw-secret bypass absent from dashd

### whapd

**[TEST-GAP]** `whapd/src/` — **zero test coverage**:

- `/send`, `/send-file`, `/typing`, `/health` all untested
- Queue flush loses messages on error: failed sends not re-queued (main.ts:182-196)
- QR reconnect: recursive `pair()` on 515 error — unbounded recursion risk (main.ts:73-119)
- LID translation uses `any` cast into Baileys internals (main.ts:142-162) — breaks silently on Baileys update
- Media download: no backpressure, oversized media causes OOM (main.ts:200-272)

**[BOUNDARY-LEAK]** `whapd/src/main.ts:383-410` — `sendChat()` errors swallowed with `.catch(() => {})`, `sendMessage()` errors logged — inconsistent

**[REDUNDANT]** `whapd/src/client.ts:11-82` — reimplements RouterClient; unavoidable cross-language gap but surface to keep in sync with Go chanlib

### Cross-daemon

**[REDUNDANT]** Handler + startup boilerplate duplicated across all Go adapters:

- `/send`, `/typing`, `/health` setup identical in each `server.go`
- Config load → adapter init → register → listen → shutdown duplicated in every `main.go`
- `teled/router_client.go`, `discd/router_client.go`, `emaid/router_client.go` — trivial chanlib wrappers; remove and import directly

**[BOUNDARY-LEAK]** All Go adapters rewrite content (mentions, formatting, chunking) — gateway should apply before send; adapters should be content-agnostic

**[TEST-GAP]** `chanlib/chanlib.go:63-82` — `SendMessage` 2-attempt retry logic only happy-path tested

### Priority

| Priority | Issue                                                                     |
| -------- | ------------------------------------------------------------------------- |
| High     | onbod: zero tests; 586-line state machine + approval untested             |
| High     | dashd, proxyd, whapd: zero tests                                          |
| High     | onbod: directly writes groups/routes/tasks bypassing gateway              |
| High     | emaid: IMAP SEEN flag set before delivery confirmed                       |
| Medium   | reditd: in-memory cursor lost on restart                                  |
| Medium   | All adapters: content formatting/chunking in adapters, belongs in gateway |
| Medium   | Adapter handler + main.go boilerplate duplication                         |
| Medium   | gateway: routing logic duplicated instead of fully delegating to router/  |
| Low      | ThreadID dead field in mastd, bskyd                                       |
| Low      | router_client.go wrappers in teled/discd/emaid                            |
| Low      | proxyd/dashd inconsistent auth posture (raw-secret bypass)                |
