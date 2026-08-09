---
status: shipped
depends: [5/13-ext-mcp, 5/14-credentials, 5/1-auth-standalone]
---

# specs/5/15 — surrogate OAuth

> The user clicks "Connect GitHub" in their dashboard; arizuko runs the OAuth
> dance and writes the access + refresh token into the same `secrets` row a
> pasted PAT would land in. Only the writer changes.

**Surrogate** = arizuko authenticates _as the user_ to a third party. Distinct
from **identity OAuth** (`5/1`), which authenticates the user _to_ arizuko. The
two are the outbound and inbound callers of one OAuth core.

This is a **write path for a capability credential**
([`5/14`](14-credentials.md)). `/dash/me/secrets` PAT paste is the floor — zero
OAuth, works today with fine-grained PATs. Surrogate is the upgrade: short-lived
scoped tokens, auto-refresh, a button instead of an opaque string. Resolution,
scope, and injection belong to `5/14`; this spec covers only how the row gets
written and kept fresh.

## Decisions

**Refresh at call time, never a worker.** The broker refreshes when
`expires_at − now < 60s`, then makes the outbound call with the new token —
plus a reactive retry-once on a 401 for providers with sloppy `expires_in`.
Both halves are `DB.refreshOAuth`, one function with a `force` flag: the
proactive call gates on the 60s window, the reactive one (behind
`ipc.CallExtTool`'s 401, at most once, and only when the refresh actually
renewed something) ignores expiry, since an optimistic `expires_in` is exactly
what makes the window useless. A 401 that survives the refresh surfaces. No
background goroutine: most users touch most providers never. A refresh failure
(revoked refresh token) nulls `expires_at` + `refresh_val` and surfaces a
structured "reconnect" error the agent sees — loud, not silent.

**Usage is not special-cased.** Once written, the token is an ordinary
capability credential. Nothing downstream knows the row came from OAuth rather
than a paste, except the refresh check on `expires_at`.

**Connecting grants nothing.** The token enables the capability; a separate
`acl` grant (`mcp:<p>:*` / `ext:<p>:*`) still decides whether the agent may call
the tool — identical to a pasted PAT. Ownership and grant stay orthogonal.

**Surrogate carries its own engine.** Identity's exchange is still per-provider
(`auth/oauth.go` google/github/discord). Generalizing it into the registry-driven
engine is the one refactor that would unify them, and it is **DEFERRED** — it
risks the shipped login path. Surrogate reuses only the low-level primitives:
`auth.PostForm`, `auth.WritePKCE`/`ConsumePKCE` (S256 PKCE),
`auth.SignState`/`VerifyState` (the CSRF-state cookie). PKCE stays at the dashd
layer, so the engine's `AuthorizeURL` takes the challenge rather than returning
a verifier stash; the callback is already authenticated (`X-User-Sub`), so state
is CSRF-only.

**Also covers env-profile keys.** `CLAUDE_CODE_OAUTH_TOKEN` and ChatGPT/codex
are already OAuth tokens pasted by hand. Same dance, same registry, same
columns — only the sink call site differs: these are `5/14` type-1 keys injected
into the container env at spawn, so their refresh check must fire at that
spawn-time resolution point, not the per-call broker path. That half is **not
built** (see Deferred). Static API keys (`sk-ant-…`) stay manual paste; there is
no dance to run for a non-OAuth key.

## What shipped

- `secrets` gains `provider` / `refresh_val` / `expires_at` / `scope_list`
  (routd migration `0017`). PAT rows leave all four NULL and the broker reads
  `value` unchanged; OAuth rows populate all four. `refresh_val` is sealed with
  the same AES-256-GCM as `value`. One row per
  `(scope_kind='user', scope_id=user_sub, key=<provider-env-name>)`, so an OAuth
  row at `key='GITHUB_TOKEN'` shadows a pasted PAT at the same key.
- `auth/surrogate/` — a registry-driven engine (`AuthorizeURL`/`Exchange`/
  `Refresh`/`Revoke`) over `go:embed`ed provider TOMLs
  (`auth/surrogate/providers/`: `github`, `google` — the latter exercises
  `access_type=offline` refresh).
- dashd `/dash/me/connections` — list, `POST /{provider}/start`,
  `GET /{provider}/callback`, `DELETE /{provider}`
  (`dashd/main.go:459-462`), beside `/dash/me/secrets`.
- Broker near-expiry refresh in `routd`'s `ConnectorSecrets`.

## Operator-configurable providers

Adding a provider is pure configuration — no Go, no rebuild. `NewEngine(dataDir)`
loads the embedded defaults then overlays operator files from
`<datadir>/surrogate/*.toml` (a same-named operator file replaces the embedded
default; the operator owns the datadir). A provider TOML carries `auth_url`,
`token_url`, `revoke_url`, `scopes`, `secret_key` (the `secrets.key` to write),
and `allowed_domain` (an egress hint only — egress stays a manual
`network_rules` step).

Confidential-client credentials come from env as
`SURROGATE_<NAME>_CLIENT_ID` / `_CLIENT_SECRET` (NAME upper-cased, `-`→`_`),
bound for **every** registered provider. A provider with no credentials loads
but is inert; one missing `auth_url`/`token_url`/`secret_key` is a **hard boot
error naming the file** — a half-defined provider is a broken dance, so it fails
loud. Scopes are space-joined per RFC 6749 §3.3.

Operator flow: drop `<datadir>/surrogate/slack.toml`, register the redirect URI
`<WEB_HOST>/dash/me/connections/slack/callback` at the provider, set
`SURROGATE_SLACK_CLIENT_ID`/`_SECRET`, restart. `EXTENDING.md` §"Add an OAuth
provider" is the recipe.

## Deferred

- Generalizing identity's per-provider exchange into the registry engine — the
  one refactor, deliberately not taken (risks the shipped login path).
- Spawn-time env-profile refresh (Anthropic/ChatGPT) — the refresh check fires
  only at the per-call broker path today.
- `cmd/arizuko/surrogate.go` operator inspector.

## Out of scope (v1)

Multi-account per provider (single `(user, provider)` row); public clients
(confidential `client_secret` providers only, PKCE always on); multi-instance
CSRF state (in-memory, single-process dashd — add a table if dashd goes
multi-process); auto-opening crackbox egress to `allowed_domain` on connect;
background refresh.

## Cross-references

[`5/13`](13-ext-mcp.md) — the broker that reads these rows.
[`5/14`](14-credentials.md) — the credential model; surrogate is one write path.
[`5/1`](1-auth-standalone.md) — the OAuth primitives reused.
