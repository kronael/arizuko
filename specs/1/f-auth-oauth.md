---
status: shipped
---

# Auth OAuth

HTTP auth layer for the web UI. Local accounts + OAuth providers.
Code: `auth/`.

## Token model

- **Access token**: JWT, 1hr TTL, in localStorage
  - Claims: `sub`, `name`, `provider`, `exp`
  - Signed **ES256 by `authd`**; backends verify offline against JWKs.
    (The HMAC-SHA256 signing this spec originally described is retired —
    [`../5/1-auth-standalone.md`](../5/1-auth-standalone.md).)
- **Refresh token**: opaque random (32 bytes), 30d TTL
  - Stored as SHA-256 hash in DB (high-entropy, argon2id not needed)
  - HttpOnly; SameSite=Strict; Secure; Path=/auth
  - Single-use rotation on each refresh

## Identity providers

| Provider | Mechanism         | Sub prefix | PKCE |
| -------- | ----------------- | ---------- | ---- |
| Local    | username + argon2 | `local:`   | n/a  |
| Telegram | Login Widget      | `tg:`      | n/a  |
| Discord  | OAuth2            | `discord:` | yes  |
| GitHub   | OAuth2            | `gh:`      | no   |
| Google   | OAuth2 + OIDC     | `google:`  | yes  |

GitHub does not support PKCE. Telegram uses its own widget flow.

## OAuth state verification

HMAC-signed cookie (stateless): `HMAC-SHA256(AUTH_SECRET, nonce + timestamp)`,
10min expiry. Verified on callback by recomputing HMAC.

## Telegram Widget verification

`hash == HMAC-SHA256(sorted_data_check_string, SHA256(bot_token))`.
Also checks `auth_date` within 5 minutes.

## Rate limiting

5 attempts / 15 min per IP on `POST /auth/login`. In-memory sliding
window, keyed by `X-Forwarded-For` or `remoteAddress`.

## OAuth config env vars

| Env var                 | Description                                                                                                                                   |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `GOOGLE_CLIENT_ID`      | Enables Google OAuth button on login page                                                                                                     |
| `GOOGLE_CLIENT_SECRET`  | Google OAuth client secret                                                                                                                    |
| `GOOGLE_ALLOWED_EMAILS` | Comma-separated glob patterns (e.g. `*@example.com`); enables email allowlist and single-domain `hd=` hint when all patterns share one domain |
| `GITHUB_CLIENT_ID`      | Enables GitHub OAuth                                                                                                                          |
| `GITHUB_CLIENT_SECRET`  | GitHub OAuth client secret                                                                                                                    |
| `GITHUB_ALLOWED_ORG`    | GitHub org name; members-only enforcement on callback                                                                                         |

## DB schema, account linking, collision handling — superseded

This section described a `routd.db` `auth_users(linked_to_sub)` account-link
model with a resolve function (`store.CanonicalSub`/`LinkSubToCanonical`), an
`intent=link` collision dispatcher, and a seven-case confirm page. That model
never had a writer — `LinkSubToCanonical` had no non-test caller — so both its
readers (proxyd's identity-header stamping, `dashd`'s linked-accounts list)
always saw the empty answer. It was one of three "one human, many logins"
mechanisms that had accreted side by side (`BUGS.md` P2). Deleted 2026-08-04
(`54125cbd`): the column, `CanonicalSub`, `LinkSubToCanonical`, `LinkedSubs`,
`AuthUserBySub`, and the `AuthUser` row type. `auth_sessions` (the refresh-token
table named above) is likewise gone — refresh tokens live in `authd`'s own
`refresh_tokens`.

The live model is `authd`'s `auth.db`: `auth_users(user_id)` +
`oauth_identities(user_id, provider, provider_sub)`, written by the OAuth
callback (`authd/store.go` `upsertOAuthUser`) whether or not `intent=link` is
set — there is no separate resolve step or collision page. The callback either
creates a new canonical user, resolves an existing `(provider, provider_sub)`
to its user, or (under `intent=link`) attaches the new identity to the
current session's user, hard-failing if that identity already belongs to
someone else. Current schema and the account-linking rules that replace
everything above are [`../5/1-auth-standalone.md`](../5/1-auth-standalone.md)
§"Account linking + collision rules".
