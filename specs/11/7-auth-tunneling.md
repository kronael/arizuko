---
status: planned
phase: next
---

# Auth Tunneling — user-completed challenge auth + cookie/token submission

Some platforms refuse to authenticate from datacenter IPs. X (Twitter)
returns "unusual activity" challenges or 403s when the login form is
submitted from Hetzner / AWS / GCP / DigitalOcean ranges. Even when a
challenge can be solved on the host, the operator may not have the 2FA
device, the residential cookies, or the patience to do it on the server.

The fix is well-known: complete the auth on a real human browser — real
IP, real fingerprint, 2FA in pocket — then transport the resulting
credential (cookies, tokens, session blob) to the daemon on the server.

This spec defines a generic **auth-tunneling endpoint** so every channel
that needs human-side auth uses the same shape, the same security model,
and the same dashboard surface. Per-channel specifics live in their own
specs; this one is the shell.

## Why

- X login is unreachable from datacenter IPs; same risk for any future
  challenge-auth platform.
- LinkedIn SSO/2FA approval needs the user's browser, not the host.
- WhatsApp pair-code works today, but next-rev WhatsApp (or any other
  channel) may add a challenge that doesn't.
- Operators currently `ssh` in, edit `.env`, restart — a wall for
  non-technical operators and a blocker for tenant self-service
  (cf. `5/32-tenant-self-service.md`).
- Every adapter that grows challenge auth otherwise reinvents the same
  pairing surface; do it once.

## Concept

A daemon that needs auth generates a **single-use signed pairing URL**.
The operator opens it in their personal browser. The page hosts whatever
flow the platform demands — OAuth redirect, cookie paste, bookmarklet —
and submits the resulting credential back to the daemon over the
arizuko web layer (`webd` / `proxyd`, auth-gated).

Flow:

1. Daemon (e.g. `twitd`) calls `proxyd` to mint a pairing token.
2. `dashd` shows the operator a URL `/auth-tunnel/<channel>/<token>`.
3. Operator opens it on their phone or laptop.
4. Page renders the per-platform challenge.
5. Browser POSTs credential to `/auth-tunnel/<channel>/<token>`.
6. `proxyd` validates the token, forwards to the daemon over the
   internal channel-secret-protected port.
7. Daemon persists, signals reconnect, status flips to `active`.

Nothing about the credential shape is the tunnel's concern. The tunnel
moves a blob from a browser to a daemon; the daemon decides if it's
valid.

## Three flow types

The endpoint family is the same for all three; only the rendered page
and the POST body differ.

### 1. Cookie-import flow (`twitd`, browser-emulation channels)

Page hosts a textarea / file uploader for cookies-export JSON (the
format produced by the `Get cookies.txt LOCALLY` extension or
equivalent). Operator:

1. Logs into x.com on their normal browser.
2. Clicks the extension, copies JSON to clipboard.
3. Pastes into the tunnel page, hits Submit.
4. Daemon validates required cookie names (`auth_token`, `ct0`),
   persists to `/srv/data/<inst>/store/twitter-auth/cookies.json`,
   reconnects.

Failure modes the page must surface: missing required cookies,
expired session, wrong domain.

### 2. OAuth-redirect flow (LinkedIn, Discord-as-channel, future)

Generalization of the existing `auth/oauth.go` pattern, scoped to a
channel rather than to operator login.

1. Page redirects browser to platform OAuth authorize URL with
   `redirect_uri=https://<host>/auth-tunnel/<channel>/<token>/callback`.
2. Platform bounces back with `code`.
3. `proxyd` exchanges code for tokens (using credentials the daemon
   pre-registered with proxyd at tunnel-begin time).
4. Tokens forwarded to daemon, persisted, reconnect.

Reuse `auth/oauth.go` token-exchange helpers; add a per-tunnel
state parameter alongside the existing operator-login state.

### 3. Bookmarklet / extension flow (`twitd` advanced, optional v2)

Page provides a one-click bookmarklet. User clicks it while a tab on
x.com is focused. Bookmarklet reads `document.cookie`, scrapes CSRF
token from the page, POSTs to the tunnel endpoint. More automated than
manual export, no extension required, but limited to cookies the page
can read (no `HttpOnly` cookies — which rules X out for this flow,
but works for some platforms with permissive cookie scoping).

Document the limitation; ship flows 1 and 2 first.

## Endpoint surface

New routes in `proxyd`, all under `/auth-tunnel/<channel>/`:

| Method | Path                | Auth     | Purpose                               |
| ------ | ------------------- | -------- | ------------------------------------- |
| POST   | `/begin`            | operator | Daemon-internal: mint a pairing token |
| GET    | `/<token>`          | none     | Render the per-channel challenge page |
| POST   | `/<token>`          | token    | Submit credential blob                |
| GET    | `/<token>/callback` | token    | OAuth redirect target (flow 2 only)   |

`POST /begin` is called by the daemon over the internal
`CHANNEL_SECRET`-protected port; not exposed publicly. It returns
`{token, url, expires_at}`.

Page rendering: `proxyd` asks the daemon (over the same internal
port) for a render spec — `{flow: "cookie-import" | "oauth" | "bookmarklet", fields: [...]}` —
and renders a small server-side template. No per-daemon HTML in
proxyd; proxyd owns the chrome, daemon owns the form.

Submission: `proxyd` forwards the POST body to the daemon's
`/v1/tunnel-receive` endpoint with the token validated and stripped.
Daemon validates the credential shape and persists.

## Security

- Token is single-use, 32 bytes random, base64url-encoded.
- Token is signed with HMAC-SHA256 keyed on `AUTH_SECRET` (reuse
  `auth/hmac.go`).
- Token expires 15 minutes after mint.
- Token is bound to: channel id, operator session id (sub), nonce.
  Submitting from a different operator session is rejected even
  with a valid token.
- Token in URL, not body — yes it lands in browser history. Single
  use + 15min expiry + binding to operator make this acceptable.
  Document the trade-off; do not put a long-lived secret in a URL.
- Credentials are **never logged**. `proxyd` redacts the request
  body for `/auth-tunnel/*/[token]` POSTs in its access log.
- Stored encrypted at rest using the same AES-GCM-from-AUTH_SECRET
  scheme proposed for `creds_json` in `11/6-dynamic-channels.md`.
  This spec does not redefine the scheme; if `7/32` ships first,
  reuse; if this ships first, define the helper here and `7/32`
  reuses it.
- TLS only. `proxyd` already terminates TLS for `/*`; the
  `/auth-tunnel/*` routes share the same termination. Reject
  plain-HTTP requests at the router level.
- Rate limit: 10 token mints per operator per hour, 100 token
  consumptions per IP per hour. Reuse `chanlib` rate limiter.

## Storage

No new storage surface. Per-channel auth dirs already exist:
`/srv/data/<inst>/store/<channel>-auth/`. The tunnel writes into
whatever path the daemon chose on `tunnel-receive`; the daemon
owns the layout.

The `channels` table from `11/6-dynamic-channels.md` (if shipped)
is where status flips from `pairing` to `active`. Without `7/32`,
each daemon owns its own status field; no DB changes here.

In-flight tokens live in memory in `proxyd`. Restart of `proxyd`
invalidates all open tunnels — this is fine, mint a new one. No
persistence of unused tokens.

## UI placement

`dashd` extends its channels page (the same page proposed in
`11/6-dynamic-channels.md`):

```
Channel: x (twitter)
  Status: pairing
  [Generate pairing URL] → https://<host>/auth-tunnel/x/abc123def
                            (expires in 15:00)
  [Cancel]
```

Operator clicks Generate, page returns the URL with a copy button
and a QR code. When the credential lands and the daemon flips
status, dashd refreshes the row via HTMX poll or SSE.

If `7/32` ships first, this is one new button on the existing card.
If this ships first, dashd grows a minimal channel list scoped to
the daemons that opt in.

## Cross-references

- **Depends on** `11/6-dynamic-channels.md` for the channel-row +
  encrypted-creds infrastructure. If `7/32` is not yet shipped,
  this spec ships a minimal subset (in-memory channel registry,
  per-daemon creds dir) and migrates to `channels` rows later.
- **Complements** `5/32-tenant-self-service.md`: tenant-controlled
  channels become viable once a non-shell-having tenant can
  complete a challenge auth.
- **Reuses** `auth/hmac.go` for token signing, `auth/oauth.go`
  for OAuth code exchange, `chanlib` rate limiter, `proxyd`
  routing layer, `dashd` channels page (per `7/32`).

Once shipped, the following become viable without operator
shell access:

- `twitd` cookie auth via browser-export.
- LinkedIn SSO/2FA without `.env` editing.
- Bluesky 2FA when it lands.
- Mastodon login challenges when they land.
- Any future channel where auth needs a real browser.

## Why this matters

- Removes the "edit `.env` on the host" wall for non-technical
  operators.
- Makes datacenter-hosted arizuko viable for X and other
  challenge-auth platforms.
- Self-service channel onboarding for tenants.
- Generic mechanism shared across every channel that grows
  challenge auth — write it once.

## Could the in-container agent use this?

Yes, conceptually. If `ant` needs credentials for an external
service the user is asking it to access (Notion, Google Drive,
private API), the same tunnel could deliver tokens into the
agent's session storage rather than to a channel daemon. The
shape is identical: mint, present URL to user via chat, receive
credential, persist into the agent's per-group store.

Out of scope for v1. Flagged in open questions.

## Out of scope (for the initial ship)

- Multi-account-per-channel (two X accounts on one instance) —
  separate concern; needs `7/32`'s channel-rows model.
- Cookie / token refresh and rotation — initial ship persists
  what it gets, daemon retries on auth failure by minting a new
  tunnel. Refresh is v2.
- Encrypted-at-rest storage — covered by `7/32`. If this ships
  first, define a minimal helper and let `7/32` adopt.
- Browser extension that auto-exports cookies on a hotkey —
  could be a follow-up project, not part of arizuko.
- Generic agent credential injection (see above) — future spec.
- Headful browser sandbox on the server (alternative architecture
  where arizuko itself runs the browser) — different threat model,
  separate spec if ever pursued.

## Effort estimate

| Surface                                          | LOC  |
| ------------------------------------------------ | ---- |
| `proxyd`: `/auth-tunnel/*` routes + signed token | ~150 |
| `dashd`: channels page extension (button + QR)   | ~100 |
| Per-daemon `/v1/tunnel-receive` + validator      | ~50  |
| `twitd` cookie validator + persist               | ~50  |
| Tests (token lifecycle, rate limits, redaction)  | ~100 |
| **Total to ship generic surface + twitd**        | ~450 |

OAuth-redirect flow adds ~100 LOC on top, mostly in `proxyd`
sharing code with `auth/oauth.go`. Each additional channel is
~50 LOC for its validator + persist hook.

## Open questions

- Should `proxyd` render the per-channel page, or proxy to a
  daemon-served page? Proxy is more flexible (daemon owns its
  HTML); proxyd-rendering keeps daemons HTML-free. Lean toward
  proxy for cookie-paste (trivial), proxyd-rendered for OAuth
  (it's just a redirect).
- QR code rendering: server-side or browser-side library? Cheap
  either way; pick whichever `dashd` already pulls in.
- Should the agent (`ant`) version of this share `proxyd` routes
  or get its own `/agent-auth-tunnel/*` namespace? Probably
  separate: agent-scoped tokens want different lifetimes and
  different binding (group id, not operator session id).
- How does an operator revoke a tunneled credential? Delete the
  channel row (per `7/32`) is the obvious answer; document.
- Do we need an audit trail (who minted, when, from which IP)?
  Probably yes, low cost, append to `dashd`'s existing audit log
  if one exists; otherwise file-based for v1.
