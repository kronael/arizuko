---
status: draft
phase: next
---

# Auth tunneling — complete the challenge in a human browser

Some platforms refuse to authenticate from datacenter IPs. X returns
"unusual activity" challenges or 403s when a login is submitted from
Hetzner / AWS / GCP ranges. Even where the challenge is solvable on the
host, the operator may not have the 2FA device or the residential session
there.

The fix is well known: complete the auth on a real human browser — real IP,
real fingerprint, 2FA in pocket — then move the resulting credential to the
daemon. This spec is the **shell** that makes every channel needing
human-side auth use one shape, one security model, and one surface. Per-
channel specifics live in the channel's own spec.

## Why one mechanism

- X login is unreachable from datacenter IPs; the same risk applies to any
  future challenge-auth platform.
- Operators otherwise `ssh` in and edit `.env` — a wall for non-technical
  operators and a hard blocker for tenant self-service
  ([5/5-worlds-agents-sessions](../5/5-worlds-agents-sessions.md)).
- Every adapter that grows challenge auth would otherwise reinvent the same
  pairing surface.

## Shape

A daemon that needs auth mints a **single-use signed pairing URL**. The
operator opens it in their own browser, completes whatever the platform
demands, and the page submits the credential back through the arizuko web
layer to the daemon, which persists it and reconnects.

Nothing about the credential's shape is the tunnel's concern — it moves an
opaque blob from a browser to a daemon; the daemon decides if it is valid.

Two flows cover the real cases:

- **Cookie import** (`twitd`, browser-emulation channels) — the page takes a
  cookies-export paste or file; the daemon validates the required cookie
  names and persists.
- **OAuth redirect** (LinkedIn, future platform OAuth) — a generalization of
  the existing `auth/oauth.go` pattern scoped to a _channel_ rather than to
  operator login, with a per-tunnel state parameter alongside the existing
  login state.

A bookmarklet flow was considered and rejected for the driving case: it can
only read non-`HttpOnly` cookies, which rules out X — the platform this
exists for.

## Endpoints

Under `/auth-tunnel/<channel>/` in `proxyd`: a daemon-internal `POST /begin`
that mints a token, a public `GET /<token>` that renders the challenge page,
a `POST /<token>` that submits the credential, and a `/<token>/callback` for
the OAuth flow.

**proxyd owns the chrome, the daemon owns the form**: proxyd asks the daemon
for a render spec (flow kind + fields) and renders a small server-side
template, so no per-daemon HTML lives in proxyd and no daemon grows an HTML
surface.

## Security

- Single-use token, 32 random bytes, HMAC-signed, 15-minute expiry.
- **Bound to (channel, operator session, nonce)** — a valid token submitted
  from a different operator session is rejected. This is what makes the
  token-in-URL acceptable: it lands in browser history, but single-use +
  short expiry + session binding bound the damage. Never put a long-lived
  secret in a URL.
- Credentials are **never logged**; proxyd redacts the body for these POSTs.
- In-flight tokens live in memory only. A proxyd restart invalidates open
  tunnels — fine, mint another. Unused tokens are never persisted.
- TLS only; rate-limit both mints per operator and consumptions per IP.

## Storage

No new storage surface. The daemon persists into its own per-channel auth
dir and owns the layout; the tunnel just hands it the blob. Encrypted-at-
rest reuses the scheme in
[6-dynamic-channels](6-dynamic-channels.md) — whichever of the two ships
first defines the helper and the other adopts it. Do not define it twice.

## The CLI is the same mechanism, not a second one

`arizuko auth <instance> <channel>` is the terminal face of this tunnel, not
a parallel path. It matters because: first auth happens before dashd is
reachable from outside; operators who SSH in have no browser at hand; and
it is scriptable for IaC.

Today every channel authenticates differently — `arizuko pair` (QR) for
whapd, env vars for teled/discd/mastd/bskyd/reditd, a cookie file for twitd.
The CLI unifies the _entry point_ without forcing one mechanism: each daemon
advertises its supported modes (`env_vars`, `pair_code`, `cookie_import`,
`oauth_redirect`), the CLI auto-picks when there is only one, and dispatches.

**Mode discovery prefers a runtime probe** over static per-daemon metadata —
one source of truth, survives version skew — falling back to shipped
metadata when the daemon is not running, which is the common case on first
auth (the adapter has not started because it has no credentials yet).

`arizuko pair` stays as an alias for the `pair_code` mode. `--revoke` clears
credentials and restarts the daemon; whether it also removes the channel row
is [6-dynamic-channels](6-dynamic-channels.md)'s call, not this spec's.

## Out of scope

- Multi-account per channel — needs the channel-rows model in
  [6-dynamic-channels](6-dynamic-channels.md).
- Credential refresh and rotation — v1 persists what it gets; the daemon
  mints a new tunnel on auth failure.
- Tunneling credentials to the in-container agent (for a service the user
  asks it to reach). The shape is identical, but agent-scoped tokens want a
  different lifetime and a different binding (group, not operator session) —
  a separate namespace and a separate spec.
- A browser extension that auto-exports cookies — a different project.
- Running the browser on the server — a different threat model entirely.
