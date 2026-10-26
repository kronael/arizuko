# Network egress

You run with no Claude Code permission prompt and no sandbox — arizuko
isolates you at the KVM guest + **egress proxy** + MCP-socket layer
instead. Editing `~/.claude/settings.json` `permissions`/`sandbox` to
grant yourself network does NOTHING (the platform rewrites those keys
every spawn). Reach is governed only by the **egress allowlist**, a
per-folder hostname list that inherits down the folder tree. Your turn
has its own proxy, started with that list baked in and stopped when the
turn ends.

## Default-deny: read the 403

A host not on your allowlist is refused at CONNECT — `curl
https://thathost/anything` returns **403 on EVERY path** (`/`, `/pub/`,
`/auth/login`, all). That 403 is **the egress proxy refusing the host,
NOT the target's auth gate.** NEVER conclude "the site blocks everyone"
or "it's auth-gated" — one host 403ing on every path while other hosts
work IS your egress allowlist. (A real auth gate gives mixed codes: 200
on public paths, 302/401 on gated.)

A **502 `upstream unreachable: …`** from the proxy is NOT a grant
problem. The host passed your allowlist and the proxy could not reach it:
`resolve <host>: no such host`, `dial tcp …: connection refused`, a dial
timeout. Report the body verbatim; asking for `network_allow` changes
nothing. Cost: an agent reported a missing grant for a host that was
granted and down (2026-10-25).

A grant scoped to a PARENT folder does not reach you — a path carries
zero authorization (spec `5/33`), so `main` covers `main` and never
`main/sub`. Cost: every subgroup on one instance silently lost egress and
web publishing after the tier removal, and the agent that hit it filed a
chown request four separate times.

## Managing the allowlist

Egress management is default-deny (5/33: no tiers — a grant, not a depth
bracket): `network_allow`/`network_deny`/`network_list` only appear in
your live tool list if an operator (or an ancestor holding it WITH GRANT
OPTION) delegated `mcp:network_allow` etc. to your folder or a folder
covering it. If you have it: `network_allow(folder, host)` — e.g.
`network_allow("atlas/search", "krons.fiu.wtf")` — opens egress for
yourself or any folder your grant scope covers; a rule cascades to all
children under the target. Elevated `/root` reaches any host (`*`) and
can grant any folder.

## Escalating

If `network_allow` is absent from your tool list, you cannot self-grant —
escalate, don't keep retrying a denied host:

1. Give the user the exact fix: "I need `api.example.com` allowlisted.
   Ask an operator: `/root please run network_allow('main/trading',
   'api.example.com')`"
2. Or file via `/issues` for the operator to handle async.
3. NEVER say vague things like "the operator can…" — give the command.

NEVER touch `settings.json` to fix network access.
