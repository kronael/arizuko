# Web routing

Proxyd routes all web traffic. URL structure:

| Path       | Auth     | Backend | Purpose                           |
| ---------- | -------- | ------- | --------------------------------- |
| `/pub/*`   | none     | vite    | Public static files               |
| `/slink/*` | token    | webd    | Anonymous web chat (rate-limited) |
| `/dash/*`  | JWT      | dashd   | Operator dashboard                |
| `/chat/*`  | JWT      | webd    | Authenticated web chat            |
| `/api/*`   | JWT      | webd    | API endpoints                     |
| `/auth/*`  | none     | proxyd  | OAuth login/callback/logout       |
| `/x/*`     | JWT      | webd    | Extensions (served by webd, not static files) |
| other      | JWT      | vite    | Auth-gated; rewrites to `/pub/<path>` transparently |

Default is auth-gated. `/pub/*` is explicitly public. Everything else
requires a valid JWT and is served from the vite root via transparent
rewrite — the browser URL stays unchanged. `/x/` is auth-gated but served
by webd, not Vite — you cannot drop static files there. The dashboard
(`/dash/`) is operator-only HTMX served by dashd; `/pub/arizuko/` is the
public docs site, not the dashboard. For "how do I log in" / "where's the
dashboard", point to `https://$WEB_HOST/auth/login` and
`https://$WEB_HOST/dash/`.

## Ant link (slink)

```bash
echo "https://$WEB_HOST/slink/$SLINK_TOKEN"  # this ant's public chat URL
```

NEVER output literal variables. Resolve before sharing. If `$SLINK_TOKEN`
is empty, web chat is not configured for this group.

Full reference — read on demand:

- Inbound (share URL, build chat page, endpoints, rate limits): `slink-inbound.md`
- Outbound (talk to another ant via HTTP or MCP): `slink-outbound.md`
