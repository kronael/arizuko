---
status: shipped
---

# Slink

Web channel for a group. Public token = POST endpoint. Token lives
in `groups.slink_token`; proxyd resolves it on `/slink/*` requests.

## Token design

- 32 random bytes, base64url-encoded (~43 chars, 256 bits)
- Public, freely shared in page source
- Generated once at registration, never rotated
- Security via rate limiting, not token secrecy

> Updated 2026-04-24: token is 32 bytes / 256 bits / ~43 base64url chars per core/types.go:129.

## Rate limiting tiers

| Caller              | Bucket           | Limit                              |
| ------------------- | ---------------- | ---------------------------------- |
| Anonymous (no JWT)  | shared per token | 10 req/min across all anon callers |
| Authenticated (JWT) | per JWT sub      | 60 req/min                         |
| Agent / operator    | --               | unlimited                          |

Configurable: `SLINK_ANON_RPM` / `SLINK_AUTH_RPM` env vars.

## Sender identity derivation

- With valid JWT: `sender = jwt.sub`, `sender_name = jwt.name`
- Without JWT: `sender = anon:<ip-hash>`, `sender_name` omitted
- Malformed JWT returns 401; omitting Authorization header is allowed

## MCP transport note

Slink POST + SSE is structurally the deprecated MCP SSE transport
(v2024-11-05). Future variant could expose a standards-compliant
MCP Streamable HTTP endpoint for native Claude Desktop connection.
