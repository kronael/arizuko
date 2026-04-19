---
status: shipped
---

# Slink

Web channel for a group. Public token = POST endpoint. Token lives
in `groups.slink_token`; proxyd resolves it on `/slink/*` requests.

## Token design

- 16-char random, URL-safe (96 bits)
- Public, freely shared in page source
- Generated once at registration, never rotated
- Security via rate limiting, not token secrecy

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
