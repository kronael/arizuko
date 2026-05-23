---
status: draft
---

# Self-service WhatsApp re-pair (operator-only)

When WhatsApp invalidates a session (mass logout, account re-login,
server-side revocation), `whapd` enters a `401 session invalidated`
loop. Recovery today is a manual docker dance — stop container, wipe
auth dir, run `--pair <phone>` one-shot with `--log-driver json-file`,
scrape stdout for the code, restart. Operators without shell access
cannot recover; operators with shell access burn 5–15 minutes per
occurrence.

This spec wires the existing `--pair` path into dashd so an operator
holding the `**` super-grant can re-pair from the browser in under
30 seconds.

The auth-dir auto-rotate on consecutive 401s is a separate concern
(filed as `specs/10/16-whapd-auth-rotate.md`); this spec is only the
re-pair flow.

## Scope (operator-only)

Re-pairing binds the instance's WhatsApp account identity to a new
linked device. That is a deployment-level decision; per
`feedback_operator_implicit`, the operator is the principal holding
the `**` wildcard grant in `acl` (no separate role, no nil sentinel).

Authorization at the dashd handler:
`sub, ok := d.requireAdmin(w, r, "**")` — `requireAdmin` already
short-circuits to operator via `MatchGroups("**")` in `auth/acl.go`
AND invokes `requireSameOrigin` internally (no need to call it
separately). Capture `sub` for the audit row. No new helper.

This is NOT exposed via MCP. The agent CANNOT call pair-start.

## What ships

Two pieces. dashd grows ONE outbound HTTP path (its first — today
dashd is read/write-SQLite-only) and uses `CHANNEL_SECRET` to call
whapd's `/v1/pair/*` endpoints. This is a real new capability in
dashd, not a re-use; it's small and contained. No gateway proxy,
no `core.Pairer` capability, no MCP tool. One renderer per concern.

### 1. whapd HTTP surface

```
POST /v1/pair/start         (chanlib.Auth)
body: {"phone":"+420735544891"}
200:  {"code":"6CF3F748","expires_at":"2026-05-17T13:05:48Z"}
429:  {"error":"pair in progress"}    # state != idle
429:  {"error":"too many attempts"}   # >5 in last hour (WhatsApp rate-limit guard)
500:  {"error":"<message>"}

GET /v1/pair/status         (chanlib.Auth)
200: {"state":"idle"}                       # session alive (connection.update.open seen)
200: {"state":"unauthenticated","since":...} # 401 loop or no creds
200: {"state":"requesting"}                  # pair started, awaiting Baileys code
200: {"state":"pending","expires_at":...}    # waiting for phone entry (NO code in this body)
```

**The pairing code is returned exactly ONCE by `/v1/pair/start`.**
`/v1/pair/status` exposes only the state and timestamps. Rationale:
any process with `CHANNEL_SECRET` (every channel adapter, dashd,
test runners) can read `/v1/pair/status`; surfacing a live pairing
code there hands device-linking to anything with the channel-secret.
The code stays in the response to the originating start call only.

State machine:

```
idle ──start──▶ requesting
requesting ──code from baileys──▶ pending(expires_at, deadline)
requesting ──baileys throws / network──▶ idle (start returns 500)
pending ──connection.update.open──▶ idle (creds written, normal flow)
pending ──60s timeout──▶ idle (failed silently; next /status shows idle)
pending ──new /v1/pair/start──▶ 429 (cannot interrupt)
*  ──auth-rotate (spec 8/16)──▶ unauthenticated (sibling-spec concern)
```

`requesting` and `pending` are guarded by a mutex on the `bot`
struct; concurrent starts from two operator browsers see one win
and one 429.

### 2. whapd code refactor (NOT a one-line extract)

Today `pairOnce()` builds its own socket via `makeSocket()` and
returns when `connection.update.open` fires. The live module-level
`sock` from `connect()` has its own reconnect loop. They cannot
coexist on the same auth dir — both would race the Noise handshake
and invalidate each other's keys.

So this is a refactor, not an extraction:

```ts
// whapd/src/bot.ts (new, or in main.ts)
class WhapdBot {
  private sock: WASocket | null;
  private pairing: { state, code?, expires_at?, deadline?, mu: Mutex };
  private auth_rotate_count: number;  // for spec 8/16

  async requestPair(phone): Promise<{code, expires_at}> {
    await this.pairing.mu.acquire();
    try {
      if (this.pairing.state !== 'idle') throw {code: 429, msg: 'pair in progress'};
      this.pairing.state = 'requesting';
      // suspend the normal reconnect loop — set a flag connect() checks
      this.suspended = true;
      this.sock?.end(undefined);
      this.sock = null;
      // run pairOnce, swap returned authed socket into this.sock
      const code_promise = pairOnceAndCaptureCode(phone);  // resolves once
      const code = await code_promise;
      this.pairing = { state: 'pending', code, expires_at: now+60s,
                       deadline: setTimeout(()=>this.expirePair(), 60_000) };
      return { code, expires_at: this.pairing.expires_at };
    } finally { this.pairing.mu.release(); }
  }

  private onPairOpen(sock_authed: WASocket) {
    this.sock = sock_authed;       // hand the freshly-authed socket back
    this.pairing = { state: 'idle' };
    this.suspended = false;        // connect() loop resumes
    clearTimeout(this.pairing.deadline);
  }

  private expirePair() { /* state→idle, this.suspended=false, connect() resumes */ }
}
```

Critical transitions to handle (all tested below):

- `requesting → failed` (Baileys throws on `requestPairingCode`)
- `pending → cancel` not exposed — operator hits "try again" only after
  60s expiry. Avoids a cancel-race window.
- `pending → rotation` reserved for spec 8/16; this spec is operator-only.

### 3. dashd page

URL: `/dash/channels/whatsapp/pair`. Behind `d.requireAdmin(w, r, "**")`.

HTMX form polls `GET /v1/pair/status` every 2s while open. Two
visible cards:

```
[ session: UNAUTHENTICATED — since 13:42:01 UTC (17 min ago) ]

[ Phone: (+420735544891 — from $WHATSAPP_PHONE env, editable)  ]
[ Start pairing ]
```

After Start, the response body's code lands in:

```
[ Code: 6CF3F748   [copy]   expires in 0:42 ]

1. WhatsApp → Settings → Linked Devices
2. Link a Device → "Link with phone number"
3. Enter the code above
```

The code is rendered ONCE from the start-call response (HTMX
swap). Subsequent /status polls update only the countdown and the
session state. On `state: idle` after `pending` → success banner.
On `pending → idle` without `connection.open` → "Code expired,
try again."

### 4. CSRF + rate-limit + audit (mandatory)

- **CSRF**: handled by `requireAdmin` (which calls `requireSameOrigin`
  internally — `dashd/authz.go`). Capture is implicit; no extra wiring.
- **Rate-limit**: whapd-side, 5 pair-start calls per rolling hour
  per process. Storage: in-memory deque of unix timestamps on the
  `WhapdBot` struct; resets on whapd restart (acceptable — restart
  is rare and operator-initiated). Exceeding returns 429 "too many
  attempts". WhatsApp bans the number on excess link attempts; this
  guard prevents one operator from burning the account in a tantrum.
- **Audit**: every `/v1/pair/start` writes a row to gated's `messages`
  table via gated's existing HTTP audit endpoint. Fields:
  - `id` = `core.MsgID("pair")`
  - `chat_jid` = `"arizuko:admin/whapd"` (synthetic instance-level JID;
    appears in dashd activity feed but doesn't route)
  - `sender` = `"whapd:pair"`
  - `verb` = `"admin.pair"`
  - `content` = `"operator <sub> started pairing for <phone>"`
  - `timestamp` = `time.Now().UTC().Format(time.RFC3339Nano)`
  - `from_me` = `false` (this is a system audit event, not an outbound
    chat message via the channel; consumers that filter `from_me=true`
    to find sent messages won't see admin events)
  - `bot_msg` = `false` (admin events are not bot replies either)
    Joint decision with spec 8/16: stay in `messages` (existing schema,
    no new table). Both specs adopt the same synthetic-JID convention.

## Phone number storage

Env var: `WHATSAPP_PHONE=+420735544891` in the instance .env.
Pre-fills the dashd form; operator can override per call.

Not in `chats_reply_state`, not in folder `SECRETS.toml`, not in a
new table. SECRETS.toml is per-group; pairing identity is per-
deployment (one WhatsApp account per whapd container, shared across
all groups). The phone belongs in instance-level config, which is
`.env`. Three options were considered (operator types each time /
per-channel cache / env); env wins on minimality + correct scope.

## What this is NOT

- NOT a `core.Pairer` capability interface. There is exactly one
  adapter with a runtime re-auth flow today (whapd). Per CLAUDE.md
  "copy 2-3 times before abstracting" — abstract when mastd, teled,
  or discd actually grow runtime re-auth. They don't today (env-var
  rotation for all three).
- NOT a gateway-proxied endpoint. Gateway routes messages; the dashd
  process can talk to whapd directly with `CHANNEL_SECRET` (the new
  outbound capability documented in §1) without adding a layer to
  gateway.
- NOT MCP-exposed for `pair_start`. Operator-only.
  `pair_status` could be MCP-exposed (read-only state for the
  agent to notify the operator in chat) — left for a follow-up; not
  shipped with this spec.
- NOT a way to bypass WhatsApp's per-account link-rate limit. The
  5/hour cap is for arizuko's protection; WhatsApp will still
  enforce its own.
- NOT the auth-dir auto-rotate. See `specs/10/16-whapd-auth-rotate.md`.

## Tests required

Failure-mode coverage, not happy-path:

1. **Parallel start race**: two concurrent `POST /v1/pair/start`
   from separate goroutines/clients; one returns 200 + code, the
   other returns 429 "pair in progress". Mutex correctness.
2. **Code returned exactly once**: `/v1/pair/status` during the
   `pending` window does NOT include the code field. Asserted on
   the response JSON.
3. **60s timeout cleanup**: start pair, do nothing, after 61s the
   state is `idle` and the next start succeeds. Timer fires, state
   resets, suspended-reconnect flag cleared.
4. **Disk-write failure during pair-open**: mock the creds write to
   fail; state must not stick in `pending`. Returns to `idle` with
   an error visible in status (state remains `unauthenticated`
   because creds didn't land).
5. **`requesting → failed`**: mock `requestPairingCode` to throw.
   Start returns 500; state returns to `idle`; reconnect loop
   resumes.
6. **Rate-limit**: 6th start within an hour returns 429 "too many
   attempts" without invoking Baileys.
7. **CSRF**: dashd POST without same-origin header returns 403
   from `requireSameOrigin`.
8. **Audit row**: each `/v1/pair/start` results in a
   `messages` row with `sender='whapd:pair'` and the calling
   operator's sub in the content.
9. **Non-operator forbidden**: caller with admin grant but NOT the
   `**` super-grant gets 403 from `requireAdmin(w, r, "**")`.
10. **Socket-swap correctness**: after successful pair, sending an
    outbound message via the live `sock` works (the swapped-in
    socket from `pairOnce` is the active one).

## Code surface (minimal — no premature abstraction)

| File                                                | Change                                                                                                                                                                                                                                            | LOC  |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---- |
| `whapd/src/main.ts` + new `whapd/src/bot.ts`        | refactor module-level `sock` into `WhapdBot` class with `requestPair`, socket swap, pairing mutex/state machine, 5/hr rate limit                                                                                                                  | ~80  |
| `whapd/src/server.ts`                               | `/v1/pair/start`, `/v1/pair/status` handlers under existing `chanlib.Auth`; status response REDACTS code                                                                                                                                          | ~50  |
| `dashd/channels.go` (new)                           | `GET /dash/channels/whatsapp/pair` (HTML), `POST /dash/channels/whatsapp/pair/start` (outbound `http.Client` to `whapd:8080/v1/pair/start` with `Authorization: Bearer $CHANNEL_SECRET`); behind `requireAdmin(w, r, "**")` (CSRF handled inside) | ~100 |
| `dashd/templates/channels-whatsapp-pair.html` (new) | HTMX form with two swap targets: `#pair-result` (one-shot from start) and `#pair-status` (polled every 2s for state + countdown)                                                                                                                  | ~60  |
| `dashd/main.go`                                     | register the two new routes in the existing mux                                                                                                                                                                                                   | ~5   |
| Tests                                               | the 10 cases above                                                                                                                                                                                                                                | ~200 |
| `core/types.go`                                     | — (NO `Pairer` interface)                                                                                                                                                                                                                         | 0    |
| `gateway/gateway.go`                                | — (NO proxy)                                                                                                                                                                                                                                      | 0    |
| `ipc/ipc.go`                                        | — (NO MCP tool)                                                                                                                                                                                                                                   | 0    |

**Net: ~475 LOC** including tests. Production code ~275 LOC.

The earlier estimate (~395) understated `pairOnce` refactor and dashd
page. This estimate is honest.

## Migration

No schema migration. The audit row uses an existing `messages`
schema (sender + verb + content all present today). No code path
in the gateway/store needs to learn `whapd:pair`.

## Spec status

Once the open questions below are answered, flip to `spec` and
implement. Order: tests first (1, 2, 6, 9 — easiest to mock the
socket); then the dashd page; then the whapd refactor; then 3, 4,
5, 10 (need the live socket lifecycle).

## Open questions

1. **WHATSAPP_PHONE in .env or in `groups/krons/SECRETS.toml`?**
   Phone is not secret, so .env (alongside `CHANNEL_SECRET`) is
   fine. Confirm.
2. **Audit row's group folder.** `groups/krons` (the instance root)
   or `groups/main`? Probably `groups/krons` since pairing affects
   the instance-wide WhatsApp identity, not the main group.
3. ~~Auto-rotate spec stub.~~ Done — `specs/10/16-whapd-auth-rotate.md`
   is written, status `discussion`, mutex-coordinated with this spec.
