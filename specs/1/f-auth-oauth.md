---
status: draft
---

<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Auth OAuth

HTTP auth layer for the web UI. Local accounts + OAuth providers.

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
  id         INTEGER PRIMARY KEY,
  sub        TEXT UNIQUE NOT NULL,     -- e.g. "local:<uuid4>"
  username   TEXT UNIQUE NOT NULL,
  hash       TEXT NOT NULL,            -- argon2id
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,         -- SHA-256 hex of refresh token
  user_sub   TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```
