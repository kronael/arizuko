---
status: draft
---

# specs/6/6 — middleware chains for inbound messages, HTTP, and MCP

Three separate request pipelines in `gated`-adjacent code hand-wire the
same cross-cutting concerns inline. Mention-gating, persona injection,
attachment enrichment, grant checks, signature stripping — each
stamped at the call site, several stamped at _two_ call sites that
drifted. Root `CLAUDE.md` "one renderer, many sinks" was written
because of one such incident (`gateway.go:535` — steering bypassed
`enrichAttachments` until cold-start renderer was hoisted). Three more
instances are live today. This spec consolidates them, one pipeline at
a time, **without a unifying abstraction**.

## Scope

Three pipelines, three concrete chain types, three independent
migrations. Each ships on its own merits; abort one without touching
the others.

- **6/6a — MCP chain (gated/ipc)** — finish the partial factoring
  already started by `granted` / `registerWithSecrets` / `regSocial`
  closure wrappers in `ipc/ipc.go`. Highest copy-paste density (8
  inline grant checks). Smallest blast radius. Ships first.
- **6/6b — Inbound message chain (gateway poll loop)** — kill the two
  surviving "one renderer, many sinks" duplications:
  `enrichAttachments` (gateway.go:571 vs 713) and the persona +
  autocalls injection pair (gateway.go:731 vs 801). Ships only after
  6/6a proves the pattern.
- **6/6c — HTTP chain (proxyd + per-daemon)** — formalize the existing
  `func(http.Handler) http.Handler` closures. Largest blast radius;
  ships last.

## Anti-spec — what this is NOT

- **Not a generic `Middleware[In, Out]`**. The three pipelines have
  incompatible signatures (`[]core.Message → Disposition`,
  `*http.Request → http.Handler`, `mcp.CallToolRequest →
*mcp.CallToolResult`); they will never share a middleware value.
  Each chain declares its own concrete `type MW func(next H) H`
  alongside its handler. Saves ~6 lines of unification, costs zero in
  type ergonomics, keeps stack traces flat.
- **Not a chain-of-everything**. Single-site sequential calls
  (`handleStickyCommand`, `tryExternalRoute`, `impulseGate.accept`)
  stay as inline calls. Promoting them to "filters" adds an ordering
  rule per concern that today is just sequential `if`. Middleware
  earns the wrapper only on duplicated cross-cutting concerns.
- **Not a cursor-advancement refactor**. The 7 `advanceAgentCursor`
  call sites pass different watermarks (`chatMsgs`, `msgs`, `all`,
  `topicMsgs`) per disposition. Each disposition owns its watermark;
  a blanket `defer` would erase that asymmetry. Cursor stays where it
  is.

---

## 6/6a — MCP chain

### Today

`ipc/ipc.go` already has three closure wrappers acting as informal
middleware:

- `granted` (line 632) — wraps a `ToolHandlerFunc` with
  `grantslib.CheckAction` using tool name.
- `registerWithSecrets` (line 646) — wraps the handler with grant
  check **plus** secret-broker injection.
- `regSocial` (line 922) — wraps per-platform send/reply/post tools
  with a JID-aware grant check (target JID from `req.Args`).

What's left inline: 6 of the 8 grant checks the audit found
(send/reply/send_file/send_voice/post/custom) need JID-context for
the check. They currently call `grantslib.CheckAction(rules, name,
map[string]string{"jid": jid})` inline — same shape every time. One
existing wrapper (`regSocial`) already handles this; the remaining
inline copies just predate it.

### Proposal

Two edits, no new abstraction:

1. Promote `regSocial`'s grant-check shape to a stand-alone wrapper
   `grantedJID` co-located with `granted`. ~8 LOC.
2. Migrate the 6 inline `CheckAction(..., {"jid": jid})` sites to
   `grantedJID`. ~24 → ~12 LOC.

Net: **~−12 LOC, +8 LOC = −4 LOC**, plus the structural win of one
named site for "MCP tool with JID-scoped grant". `submit_turn` is
out of scope (separate parser, separate idempotency, no grant
duplication).

### Concerns kept strictly orthogonal

- `granted` — grant check by tool name only.
- `grantedJID` — grant check with JID target resolved from `req.Args`.
- `registerWithSecrets` — secret broker only; composed _over_
  `granted` when both are needed (existing pattern, unchanged).
- Error formatting — already factored at `toolErr`/`toolJSON`/`toolOK`
  (ipc.go:384). Not in scope.

What was previously listed as `AuthorizeJID` separate from
`GrantCheck` was the same concern at two granularities — `authorizeJID`
(ipc.go:479) wraps `auth.Authorize`, which the JID grant check already
calls. One filter (`grantedJID`) absorbs both call patterns; no
separate `ResolveJIDTarget` needed because the JID is already a field
on `req.Args`.

---

## 6/6b — inbound message chain

### Today

`gateway/gateway.go`'s poll loop (lines 502-650) and the steering
batch processor (`processSenderBatch`, ~line 700) each call into the
_same_ helpers in different sequences. Two duplications matter:

| Concern                                    | Cold-start call site | Steering call site | LOC  |
| ------------------------------------------ | -------------------- | ------------------ | ---- |
| `enrichAttachments`                        | gateway.go:571       | gateway.go:713     | ~3+3 |
| `personaBlock` + `autocallsBlock` (paired) | gateway.go:731       | gateway.go:801     | ~6+6 |

The rest of the poll loop (resolveGroup, handleStickyCommand,
tryExternalRoute, impulseGate) is single-site and stays as inline
sequential calls. Promoting them to filters would add ordering rules
the code doesn't need today.

### Proposal

Two extractions, no chain abstraction:

1. **`enrichBatch(ctx, msgs []core.Message) ([]core.Message, error)`**
   — single helper, idempotent guard inside (skips already-enriched).
   Replace both call sites. ~3 LOC saved + drift killed.
2. **`buildPromptContext(folder, topic) PromptContext`** — returns
   `{Persona string, Autocalls string}` as separate fields. Replaces
   the _paired_ call at 731 and 801. **Persona and Autocalls stay
   separate concerns inside** — different inputs (PERSONA.md vs
   session state), different failure modes, different cache keys.
   The struct just packages them so the dual call site reduces to
   one function call; callers still address `.Persona` and
   `.Autocalls` independently. ~6 LOC saved + drift killed.

### Concerns kept strictly orthogonal

- `enrichBatch` — attachment download + envelope. One side effect:
  file I/O. Idempotent.
- `buildPromptContext` — pure read of two unrelated sources, packaged
  in a struct. **Not a SystemContext "filter"** — no behavior of its
  own beyond the two reads. If `Autocalls` later grows session-aware
  caching, it stays inside `autocallsBlock`; the packaging function
  doesn't accumulate logic.
- The "mention-gate for onboarding" already shipped inline at
  gateway.go:533. Strictly orthogonal — one Boolean clause, no
  middleware needed.

### Cursor advancement stays as-is

`advanceAgentCursor` keeps its 7 call sites. Each disposition (drop,
steering, web-topic, error) carries its own watermark; the asymmetry
is the design, not duplication. A blanket `defer` would lose
information; a `Disposition{kind, cursor}` carrier would only
relocate it. Out of scope.

### Honest LOC

Two duplications × ~6 LOC each saved = ~12 LOC. Plus two new helper
signatures (~6 LOC). **Net: ~−6 LOC**. The win is structural (drift
killed), not line count.

---

## 6/6c — HTTP chain

### Today

`proxyd/main.go` already chains by closure:
`stripClientHeaders` → `fixForwardedFor` → `accessLog` → `tryAuth` →
`setUserHeaders` → route dispatch. `auth/middleware.go` exposes
`RequireSigned` / `StripUnsigned` in canonical
`func(http.HandlerFunc) http.HandlerFunc` shape, consumed by webd
and onbod. This is the standard Go pattern. **Nothing to abstract.**

### Proposal

Two splits, both small:

1. **Split `davRoute` (proxyd/main.go:546-591)** into `groupScope`
   (folder/membership check) and `pathDeny` (`.env`/`.pem`/`.git`/
   `/logs/*` blocklist). Different concerns smashed into one handler
   by accident. Path-deny is reusable on any route; group-scope
   isn't. ~10 LOC redistributed; no LOC saved, but `pathDeny` becomes
   composable.
2. **Co-locate the proxyd chain definition** at the top of
   `proxyd/main.go` as a `[]func(http.Handler) http.Handler` slice
   with a one-line `Chain(slice, terminal)` reducer. Today the chain
   is implicit in serial calls inside `ServeHTTP`. Naming the chain
   makes the order auditable and lets a new middleware land in one
   slot. ~5 LOC chain reducer, ~−5 LOC at the call site. **Net 0.**

### Concerns kept strictly orthogonal

- `groupScope` — caller's groups ⊆ resource's folder. One concern.
- `pathDeny` — static blocklist on URL path. Independent of caller.
- `accessLog` — observation only, no auth coupling.
- `auth` middlewares stay as they are; they already match the idiom.

### Out of scope

A unified `RouteRegistry` that lets each daemon publish its chain to
proxyd — that's spec 6/4 / 6/5 territory, not this spec.

---

## Realistic LOC delta (all three combined)

| Sub-spec     | Saved   | Added   | Net          |
| ------------ | ------- | ------- | ------------ |
| 6/6a MCP     | ~12     | ~8      | **−4**       |
| 6/6b inbound | ~12     | ~6      | **−6**       |
| 6/6c HTTP    | ~10     | ~10     | **0**        |
| **Total**    | **~34** | **~24** | **~−10 LOC** |

The earlier "−180 LOC" claim was inflated — it counted concern LOC
against single-site code, double-counted already-factored wrappers,
and assumed a cursor-defer rewrite that doesn't work. The real win is
**three named duplications killed** — `enrichAttachments`, persona+
autocalls injection, JID-grant-check copy-paste. Drift prevention has
no LOC equivalent; that's the actual deliverable.

## Migration order

1. Ship 6/6a (MCP `grantedJID` wrapper + 6 site migration). One PR.
   If it doesn't reduce both LOC and call-site count, stop.
2. Ship 6/6b (`enrichBatch` + `buildPromptContext`). One PR. Drift
   tests cover both helpers.
3. Ship 6/6c only if 6/6a + 6/6b expose a real need; otherwise leave
   proxyd alone. Splitting `davRoute` is independently useful and can
   ship without the rest of 6/6c.

## Risks (acknowledged, not hand-waved)

- **Test ergonomics**: each new helper is callable as a plain
  function in tests. No chain construction needed because there is
  no chain (6/6a, 6/6b) or because the chain is the Go idiom anyone
  testing HTTP already knows (6/6c).
- **Stack traces**: unchanged. Three concrete wrappers add one frame
  each, exactly like today's `granted`/`registerWithSecrets`.
- **Binding time**: `grantedJID` reads `rules` from the same closure
  scope as `granted` (ipc.go:632). No ctx-threading, no per-mw
  constructor.
- **Premature abstraction trap**: this spec was nearly the trap.
  Rewritten after audit; the current shape is "finish existing
  factoring + name two duplications + leave the rest alone". If even
  6/6a feels like ceremony, the answer is to delete the wrapper and
  accept the 6 inline copies — not to add scope.

## Open questions

- Should `grantedJID` accept the JID key (`"jid"` vs `"target_jid"`)
  as a parameter, or fix it to `"jid"`? `regSocial` fixes it today;
  consistent with that.
- Does `buildPromptContext` need a context.Context for cancellation
  on slow PERSONA.md reads? `personaBlock` doesn't take one today
  (file is small, sync read). Defer until a real timeout symptom.
