---
status: shipped
---

# Auth OAuth

HTTP auth layer for the web UI. Local accounts + OAuth providers.
Code: `auth/`.

## Token model

- **Access token**: JWT, 1hr TTL, in localStorage
  - Claims: `sub`, `name`, `provider`, `exp`
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

## DB schema

```sql
CREATE TABLE auth_users (
  id            INTEGER PRIMARY KEY,
  sub           TEXT UNIQUE NOT NULL,  -- e.g. "local:<uuid4>"
  username      TEXT UNIQUE NOT NULL,
  hash          TEXT NOT NULL,         -- argon2id (empty for OAuth-only)
  name          TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  linked_to_sub TEXT                   -- NULL = canonical; else points at canonical sub
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,         -- SHA-256 hex of refresh token
  user_sub   TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

## Account linking

A user may attach multiple OAuth provider subs to one account. The
`auth_users.linked_to_sub` column points each linked sub at its
canonical sub. A row is canonical when `linked_to_sub IS NULL`. We
do not allow chains — `LinkSubToCanonical` rejects when the target
itself is linked.

`store.CanonicalSub(sub)` is the single resolve point: it returns
`sub` for canonical or unknown subs, otherwise the canonical it's
linked to. Called once, at JWT mint time
(`auth.issueSession`). Downstream code (proxyd's identity-header
stamping, webd, gateway, ipc, …) only ever sees canonical subs in
JWT claims and `X-User-Sub` headers — no callers re-resolve.

Initiating a link: a logged-in user clicks a "Link account" button
on `/dash/profile`, which redirects to
`/auth/{provider}?intent=link&return=/dash/profile/`. The redirect
handler reads the caller's existing session (Bearer JWT or refresh
cookie via the store), encodes `intent=link` and the canonical
sub-to-link-to into the OAuth state cookie's signed payload, and
sends them to the provider as usual.

State token shape: the OAuth state cookie is `ts.nonce.sig` for
plain logins or `ts.nonce.payload.sig` when carrying intent. The
HMAC covers everything before the trailing `.sig`. The payload is
base64url(`{"i":"link","f":"<canonical-sub>","r":"<return>"}`).
Verify accepts both shapes.

## Collision handling

When the OAuth callback resolves to `B:bob`, the dispatcher fans out
to seven cases:

| #   | intent=link? | session? | new sub state                     | action                             |
| --- | ------------ | -------- | --------------------------------- | ---------------------------------- |
| 1   | yes          | (any)    | already linked to `LinkFrom`      | refresh session, no-op             |
| 2   | yes          | (any)    | canonical for some other user `C` | render collision page              |
| 3   | yes          | (any)    | new                               | write link, refresh                |
| 4   | no           | active   | new                               | render collision page              |
| 5   | no           | active   | canonical for some other user `C` | render collision page              |
| 6   | no           | none     | new                               | create canonical row, log in       |
| 7   | no           | none     | exists (canonical or linked)      | log in via canonical (one resolve) |

The collision page is one HTML template with two buttons + a
hidden HMAC-signed `collideToken`:

- **Link**: commits `LinkSubToCanonical(newSub, name, currentSub)`
  and refreshes the session as `currentSub`.
- **Log out**: deletes the refresh cookie + session, then logs in
  as `newSub` (creating a canonical row if new).

The token is HMAC-signed (`AUTH_SECRET`) and 10min-TTL, so a copied
collision URL is uninteresting. The "Link" button is disabled when
the new sub is canonical for an existing user (we don't merge two
existing accounts via this UI — that's a manual operator action).
