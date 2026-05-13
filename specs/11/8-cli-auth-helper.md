---
status: planned
phase: next
depends: [32-dynamic-channels, 37-auth-tunneling]
---

# CLI Auth Helper

`arizuko auth <instance> <channel>` — single CLI entry point that
delegates per-channel authentication to channel-specific helpers.

Today every channel authenticates differently: whapd via
`arizuko pair <inst> whapd` (QR scan), teled/discd via env vars, mastd
via OAuth tokens in `.env`, reditd via username/password env, twitd via
cookie file or `--pair`. Operators have to remember which channel uses
which mechanism. This spec unifies the entry point without forcing one
mechanism on all channels.

The CLI command and the dashd web UI (per `7/37-auth-tunneling`) are
two faces of the same underlying auth-tunnel mechanism.

## Interface

```bash
arizuko auth <instance> <channel> [--mode <mode>] [flags...]
arizuko auth <instance> <channel> --revoke
```

Example session:

```
$ arizuko auth krons twitd
twitd supports: cookie_import, oauth_redirect (via auth-tunnel)

Pick auth mode:
  1. cookie_import  — paste cookies-export JSON
  2. oauth_redirect — open URL in your browser, complete X login

[1/2]: 1

Paste cookies JSON (Ctrl-D when done):
{...}

→ wrote /srv/data/arizuko_krons/store/twitter-auth/cookies.json
→ restarted arizuko_twitd_krons
✓ twitd connected (verified via /v1/auth/probe)
```

Auto-pick when only one mode is supported. `--mode <name>` skips the
prompt. `--cookies-file <path>`, `--pair <phone>`, etc. are mode-specific
flags consumed by the dispatcher.

## Flow types

Each daemon advertises one or more of:

1. **`env_vars`** — daemon expects credentials in `.env`. CLI prints
   the required keys and exits. Operator edits `.env`, runs
   `arizuko run <inst>`. Used by: teled, discd, mastd, bskyd, reditd.

2. **`pair_code`** — daemon's `--pair` flag generates a code/QR for
   the operator to enter on a phone. CLI runs
   `docker compose run --rm <daemon> --pair [args]` and streams
   stdout/stderr. Used by: whapd, twitd (local-host only).

3. **`oauth_redirect`** — daemon participates in a one-shot OAuth
   flow via `auth-tunnel` (per `7/37`). CLI calls dashd's
   `/auth-tunnel/<channel>/begin`, gets a single-use URL, prints it.
   Operator opens in their browser; auth completes there; daemon's
   auth dir gets populated; CLI polls `/v1/auth/probe` until the
   daemon reports connected. Used by: linkd, future Mastodon/Bluesky
   OAuth, twitd (cookie-import-via-tunnel).

4. **`cookie_import`** — daemon expects a cookies JSON file at a
   specific path. CLI prompts operator for path or pasted JSON,
   copies into the right location, restarts the daemon. Used by:
   twitd.

## Mode discovery

Two options, daemon picks one:

- **Static metadata**: `<daemon>/auth.toml` shipped with the binary,
  read by the CLI from the local checkout / image tag.
- **Runtime probe**: daemon exposes `GET /v1/auth/modes` returning
  `{"modes": ["pair_code", "qr"], "env_keys": [...], "paths": {...}}`.

CLI prefers runtime probe (single source of truth, survives version
skew). Falls back to static metadata if the daemon isn't running yet
(common on first auth — adapter container hasn't started because it
has no creds).

`auth.toml` shape:

```toml
[modes.cookie_import]
path = "store/twitter-auth/cookies.json"
restart = true

[modes.oauth_redirect]
tunnel_endpoint = "/auth-tunnel/twitd/begin"
probe = "/v1/auth/probe"

[modes.env_vars]
keys = ["TELEGRAM_BOT_TOKEN"]
```

## Implementation surface

### `cmd/arizuko/main.go`

Dispatch `auth` subcommand:

```go
case "auth":
    cmdAuth(os.Args[2:])
```

### `cmd/arizuko/auth.go` (NEW)

- `runAuth(inst, channel string, opts AuthOpts) error`
- Resolves `instanceDir`
- Loads daemon's auth modes (probe → static fallback)
- Selects mode (flag or interactive prompt)
- Dispatches to mode handler
- Verifies via `/v1/auth/probe` where supported

```go
type AuthOpts struct {
    Mode        string
    CookiesFile string
    Pair        string
    Revoke      bool
}
```

Mode handlers are pure functions in the same file; one per flow type.
~200 LOC including help text and error messages.

### Per-daemon support

Each daemon adds either an `auth.toml` next to its `main.go` or a
`GET /v1/auth/modes` HTTP endpoint. ~10 LOC per daemon.

For daemons that already implement `--pair` (whapd, twitd), the CLI
just declares `pair_code` mode pointing at `--pair`. No daemon code
change required.

### Auth-tunneling integration

When mode is `oauth_redirect`, the CLI:

1. POSTs to dashd's `/auth-tunnel/<channel>/begin` (per `7/37`)
2. Receives a single-use URL
3. Prints the URL for the operator
4. Polls `/v1/auth/probe` every 2s until success or timeout (5min)
5. On success, prints the connection status and exits

If dashd is unreachable (bootstrap, before public proxy is up), the
CLI prints the URL with the local `dashd` host:port and instructs the
operator to SSH-tunnel.

### Backwards compat

`arizuko pair <inst> <svc>` continues to work as an alias for
`arizuko auth <inst> <svc> --mode pair_code`. Existing operator
muscle memory is preserved.

### Revoke

`arizuko auth <inst> <channel> --revoke`:

1. Removes credential files (cookies json, oauth tokens dir)
2. Clears env keys from `.env` (with confirmation)
3. Restarts the daemon

## Why CLI matters (alongside dashd)

- Operators who SSH into the host don't always have a browser at hand
- Bootstrap: first auth happens before dashd is reachable from outside
- Scriptable: `arizuko auth krons twitd --cookies-file /path/...` for
  IaC / CI
- Consistent UX across channels — today every channel is different

## Out of scope

- Replacing per-daemon `--pair` flags (CLI delegates to them)
- Multi-account auth (one credential per channel for v1)
- Token rotation / refresh — daemons handle internally
- Generic auth for the in-container agent (`ant`) — separate spec if
  needed

## Effort estimate

- `cmd/arizuko/auth.go`: ~200 LOC
- Per-daemon `auth.toml` or `/v1/auth/modes`: ~10 LOC × 8 daemons = 80
- Wiring + tests: ~100
- Total: ~400 LOC

Can ship with `env_vars`, `pair_code`, `cookie_import` first; add
`oauth_redirect` once `7/37` lands.

## Open questions

1. Should `--revoke` also unregister the channel row (per `7/32`),
   or only clear creds? Default: only clear creds; channel row is
   managed separately.
2. Multi-account on one channel — when does this become real?
   Out of scope for v1.
3. Web-only operators use dashd's channels page directly; the CLI is
   an alternate entry point to the same flow.

## Cross-references

- `11/6-dynamic-channels.md` — channel-row + creds storage layer
- `5/32-tenant-self-service.md` — tenant-controlled channels framing
- `7/37-auth-tunneling.md` — the web side of the same flow
