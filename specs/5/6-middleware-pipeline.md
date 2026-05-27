---
status: draft
relates-to: [4/9-acl-unified, 5/M-webdav, 6/Y-secret-broker, 6/Z-egred-mitm]
---

# specs/5/6 — middleware chains for MCP, HTTP, and inbound messages

Three separate request pipelines in arizuko hand-wire the same
cross-cutting concerns inline. Mention-gating, persona injection,
attachment enrichment, grant checks, signature stripping — each
stamped at the call site, several stamped at _two_ call sites that
drifted. Root `CLAUDE.md` "one renderer, many sinks" was written
because of one such incident (`gateway.go:535` — steering bypassed
`enrichAttachments` until cold-start renderer was hoisted). This spec
consolidates them, one pipeline at a time, **without a unifying
abstraction**.

## Scope

Three pipelines, three concrete chain types, three independent
migrations. Each ships on its own merits; abort one without touching
the others.

- **6/6a — MCP chain (`ipc/ipc.go`)** — finish the partial factoring
  already started by `granted` / `regSocial`. Biggest LOC win.
- **6/6b — inbound message chain (`gateway/gateway.go`)** — kill the
  surviving `enrichAttachments` duplication. Small but kills drift.
- **6/6c — HTTP chain (`proxyd/main.go`)** — extract `groupScope` from
  `davRoute`. Smallest scope; ships only if motivated.

## Anti-spec — what this is NOT

- **Not a generic `Middleware[In, Out]`**. The three pipelines have
  incompatible signatures (`[]core.Message → Disposition`,
  `*http.Request → http.Handler`, `mcp.CallToolRequest →
*mcp.CallToolResult`); they will never share a middleware value.
  Each chain declares its own concrete `type MW func(next H) H`
  alongside its handler.
- **Not a chain-of-everything**. Single-site sequential calls stay as
  inline calls. Middleware earns the wrapper only on duplicated
  cross-cutting concerns.
- **Not a cursor-advancement refactor**. The 7 `advanceAgentCursor`
  call sites carry per-disposition watermarks; that asymmetry is the
  design.
- **No chain reducer.** Each pipeline has ONE chain; nothing to share
  across pipelines.

---

## 6/6a — MCP chain

> **Under `specs/4/9-acl-unified.md`** `granted` and `grantedJID`
> collapse to a single `gated(Authorize)` wrapper — JID flows through
> `params`. The two-wrapper plan below is the interim shape until 4/9
> ships fully.

### Today

`ipc/ipc.go` has two closure wrappers acting as informal middleware:

- **`granted` (line 720)** — registers a tool whose handler is gated
  by the 2-layer check on tool name only:
  `grantslib.CheckAction(rules, name, nil) && authorizeCall(name, nil)`.
- **`regSocial` (line 1029)** — registers per-platform social tools
  (like/delete/forward/quote/repost/dislike/edit) with the 3-layer
  JID-scoped check: `grantslib.CheckAction` + `authorizeCall` +
  `authorizeJID`.

Supporting closures:

- `authorizeCall` (line 683) — calls `db.Authorize`, the unified ACL
  entry-point from spec 4/9.
- `authorizeJID` (line 559) — structural sibling check (target folder
  ⊆ caller subtree).

What's left inline: **7 tool handlers re-implement the 3-layer
JID-scoped check by hand**:

- `send` (line 839)
- `reply` (line 868)
- `send_file` (line 903)
- `send_voice` (line 942)
- `post` (line 983)
- `pane_set_prompts` (line 1172)
- `pane_set_title` (line 1202)

Same ~9-line shape every time. `regSocial` already absorbs this for
the 7 social tools; the 7 remaining inline copies just predate it.

### Proposal

Two edits, no new abstraction:

1. Add `grantedJID(name, desc, opts, h)` co-located with `granted`
   (after line 732). The wrapper extracts `jid` from
   `req.GetString("chatJid", "")` and applies the same 3-layer check
   inlined today and inside `regSocial`. ~12 LOC.
2. Migrate the 7 inline 3-layer sites to `grantedJID`. ~7 × 9 LOC of
   inline check → ~7 × 1 LOC wrapper call.

Net: **~−56 LOC saved, +12 LOC added = ~−44 LOC**, plus the
structural win of one named site for "MCP tool with JID-scoped grant".

`submit_turn` is out of scope (separate JSON-RPC parser, separate
idempotency). The social tools registered via `regSocial` are
unchanged.

### Concerns kept strictly orthogonal

- `granted` — 2-layer check by tool name only (CheckAction +
  authorizeCall).
- `grantedJID` — 3-layer check by tool name + JID target (CheckAction
  - authorizeCall + authorizeJID).
- Error formatting — already factored at `toolErr`/`toolJSON`/`toolOK`
  (ipc.go ~384). Not in scope.

`authorizeJID` (ipc.go:559) is the structural-tree check (target
folder must be in the caller's subtree). `authorizeCall` (ipc.go:683)
is the unified-ACL check (spec 4/9). They are independent concerns;
the wrapper sequences them, same order, every call site.

---

## 6/6b — inbound message chain

### Today

`gateway/gateway.go`'s poll loop (`pollOnce`, line 557) and the
steering batch processor (`processSenderBatch`, line 793) both call
`enrichAttachments` in a 4-line for-loop. One real duplication:

| Concern             | Poll site          | Steering site      | LOC  |
| ------------------- | ------------------ | ------------------ | ---- |
| `enrichAttachments` | gateway.go:658-661 | gateway.go:798-801 | ~4+4 |

Persona + autocalls is already single-sink (`buildAgentPrompt` at
line 959, called from line 826 and line 907). No duplication there —
the earlier "buildPromptContext" proposal solved a non-problem.

The rest of the poll loop (`resolveGroup`, `handleStickyCommand`,
`tryExternalRoute`) is single-site and stays as inline sequential
calls. Promoting them to filters would add ordering rules the code
doesn't need.

### Proposal

One extraction, no chain abstraction:

1. **`enrichBatch(ctx, msgs []core.Message, folder string)`** — single
   helper, idempotent (no-op for already-enriched messages, matching
   `enrichAttachments`'s current behavior). Replace both call sites.

Net: **~−3 LOC.** The win is structural (drift killed), not line
count.

### Concerns kept strictly orthogonal

- `enrichBatch` — attachment download + envelope. One side effect:
  file I/O. Idempotent.
- The "mention-gate for onboarding" already shipped inline at
  gateway.go:533. One Boolean clause, no middleware needed.

### Cursor advancement stays as-is

`advanceAgentCursor` keeps its 7 call sites. Each disposition (drop,
steering, web-topic, error) carries its own watermark; the asymmetry
is the design. Out of scope.

---

## 6/6c — HTTP chain

### Today

`proxyd/main.go`'s top-level `route()` (line 456) is an `if`-cascade
on URL path, not a closure chain. Inline steps: `stripClientHeaders(r)
→ s.fixForwardedFor(r) → vhost match → per-path dispatch via
`requireAuth`/`dispatchRoute`. The imperative shape is fine —
proxyd has one chain, nothing to share infrastructure with.

`auth/middleware.go` exposes `RequireSigned` (line 15) /
`StripUnsigned` (line 44) in canonical `func(http.HandlerFunc)
http.HandlerFunc` shape. Consumed by webd and onbod. This IS the
standard Go pattern.

`davRoute` (proxyd/main.go:650) does four things in one function:
double-dot path check, group-scope check (`MatchGroups`), path
blocklist (`davAllow` at line 709), and proxy. The path blocklist
(`davAllow`) is **already factored**. The group-scope check is inline
and only reusable if extracted.

### Proposal

One split:

1. **Extract `groupScope(rest string, groups []string) (string, bool)`**
   from `davRoute` (proxyd/main.go:650-695). Takes the path remainder
   after `/dav/` and the caller's groups; returns the resolved group +
   true on allow, `""` + false on deny. ~10 LOC redistributed; no LOC
   saved, but `groupScope` becomes reusable for any future per-group
   route surface (e.g. `/files/`, `/diary/`).

`davAllow` is the spec's previous-draft `pathDeny`; that name was
wrong.

### Concerns kept strictly orthogonal

- `groupScope` (new, extracted) — caller's groups ⊆ resource's
  folder. One concern.
- `davAllow` (already factored) — static blocklist on URL path
  (`.env`/`.pem`/`.git`/`/logs/*`). Independent of caller. Unchanged.
- `accessLog` — observation only, no auth coupling.
- `RequireSigned` / `StripUnsigned` — already idiomatic, untouched.

### Out of scope

A unified `RouteRegistry` that lets each daemon publish its chain to
proxyd — that's `35-proxyd-standalone.md` + `5-uniform-mcp-rest.md`
territory.

---

## Canonical middleware catalog

The middleware concepts arizuko uses are scattered across specs that
ADD concrete middlewares to the chains 5/6 defines. This catalog
maps each middleware to its chain so a future reader has one index.

| Chain                                                | Middleware                                                                        | Defined in                  | Concern                                              |
| ---------------------------------------------------- | --------------------------------------------------------------------------------- | --------------------------- | ---------------------------------------------------- |
| **MCP dispatch** (`ipc/ipc.go`)                      | `granted`                                                                         | spec 5/6 (this)             | CheckAction + authorizeCall on tool name             |
| MCP dispatch                                         | `grantedJID`                                                                      | spec 5/6 (this)             | + authorizeJID for JID-scoped tools                  |
| MCP dispatch                                         | `authorizeCall` (closure)                                                         | spec 4/9                    | unified ACL gate                                     |
| MCP dispatch                                         | `authorizeJID` (closure)                                                          | spec 5/6 (this)             | structural folder-subtree check                      |
| MCP dispatch                                         | `injectSecrets`                                                                   | spec 6/Y                    | per-tool secret broker injection (not yet shipped)   |
| **proxyd HTTP** (`proxyd/main.go`)                   | `stripClientHeaders`, `fixForwardedFor`, `accessLog`, `tryAuth`, `setUserHeaders` | proxyd internal (idiomatic) | request normalization + identity injection           |
| proxyd HTTP                                          | `davAllow`                                                                        | spec 5/M                    | WebDAV write blocklist on sensitive paths            |
| proxyd HTTP                                          | `groupScope` (new, extracted)                                                     | spec 5/6 (this)             | caller's group ⊆ resource folder                     |
| **adapter HTTP** (`chanreg/httpchan.go`, per-daemon) | `statusWriter`, `logging`, HMAC helpers                                           | spec 4/chanlib-refactor     | per-adapter request envelope                         |
| adapter HTTP                                         | `RequireSigned` / `StripUnsigned`                                                 | `auth/middleware.go`        | signed-identity verification (proxyd → backend)      |
| **gateway inbound** (`gateway/gateway.go`)           | `enrichBatch` (new)                                                               | spec 5/6 (this)             | attachment download                                  |
| gateway inbound                                      | mention-gate (inline)                                                             | spec 5/L                    | onboarding mention filter                            |
| **egred egress** (`crackbox/egred/`)                 | `auditMITM`                                                                       | spec 6/Z                    | per-call audit row to `secret_use_log`               |
| egred egress                                         | `injectSecretsBroker`                                                             | spec 6/Z                    | placeholder substitution at egress (uses 6/Y broker) |

**Naming convention:** each middleware is a `func(next H) H` closure
that returns a wrapped handler of the same type as the chain. Names
are verbs or noun-phrases describing the concern, not the chain. New
middlewares land in one slot in their chain's assembly function.

**Testing convention:** middlewares are plain functions, callable
directly in tests without the chain. No chain-test harness; integration
tests cover the chain order separately.

---

## Realistic LOC delta (all three combined)

| Sub-spec     | Saved   | Added   | Net          |
| ------------ | ------- | ------- | ------------ |
| 6/6a MCP     | ~56     | ~12     | **−44**      |
| 6/6b inbound | ~6      | ~3      | **−3**       |
| 6/6c HTTP    | ~10     | ~10     | **0**        |
| **Total**    | **~72** | **~25** | **~−47 LOC** |

The real win is **two named duplications killed** (7-site JID-grant-
check copy-paste, `enrichAttachments` drift) + one factor extracted
(`groupScope`). Drift prevention has no LOC equivalent; that's the
deliverable.

## Migration order

1. Ship 6/6a (MCP `grantedJID` wrapper + 7-site migration). One PR.
   Biggest win, smallest blast radius.
2. Ship 6/6b (`enrichBatch`). One PR. Drift test covers the helper.
3. Ship 6/6c only if motivated; `groupScope` extraction is
   independently useful and small.

## Risks (acknowledged)

- **Test ergonomics**: each helper callable as a plain function. No
  chain construction needed because no chain exists across pipelines.
- **Stack traces**: unchanged. Each wrapper adds one frame, like
  today's `granted`/`regSocial`.
- **Binding time**: `grantedJID` reads `rules` from the same closure
  scope as `granted` (ipc.go:720). No ctx-threading, no per-mw
  constructor.
- **Premature abstraction trap**: this spec was nearly the trap. The
  shape now is "finish existing factoring + name two duplications +
  leave the rest alone". If even 6/6a feels like ceremony, delete the
  wrapper and accept the 7 inline copies — don't add scope.

## Open questions

- Does `enrichBatch` need a `context.Context` for cancellation on
  slow downloads? Today's `enrichAttachments` doesn't take one.
  Defer until a real timeout symptom.

## Pointers

- `ipc/ipc.go:720` — `granted` definition
- `ipc/ipc.go:1029` — `regSocial` definition
- `ipc/ipc.go:683` — `authorizeCall` closure (calls `db.Authorize`)
- `ipc/ipc.go:559` — `authorizeJID` closure
- `gateway/gateway.go:658-661, 798-801` — `enrichAttachments` dup
- `proxyd/main.go:650-695` — `davRoute`; the `groupScope` factor target
- `proxyd/main.go:709` — `davAllow` (already factored)
- `auth/middleware.go` — `RequireSigned`/`StripUnsigned`
- `.ship/audit-5-6-middleware.md` — verification audit (2026-05-27)
