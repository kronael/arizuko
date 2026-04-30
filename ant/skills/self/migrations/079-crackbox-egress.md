# 079 — crackbox egress isolation

Outbound network from your agent container goes through `crackbox`, a
forward proxy. The container's `HTTPS_PROXY` and `HTTP_PROXY` env
vars point at it; curl, wget, pip, npm, git, go, apt all honor that
automatically. Node's built-in fetch is wired up via a global agent
shim. You don't have to do anything.

What you'll see:

- **Tier 0 (root) and tier 1 (world) agents**: traffic flows freely.
  Allowlist contains a `*` wildcard — any host matches. crackbox
  still logs every CONNECT for future audit.
- **Tier 2+ (buildings, rooms)**: strict per-folder allowlist.
  Defaults are `anthropic.com` + `api.anthropic.com`. Connections to
  unlisted hosts return 403 from the proxy; curl prints
  `CONNECT tunnel failed`.

If a tier 2+ agent legitimately needs another host, the operator
adds it via `arizuko network <instance> allow <folder> <target>`.
Don't hammer the proxy with retries — the answer is a config change,
not persistence.

Three knobs you might see in env:

- `HTTP_PROXY=http://crackbox:3128` / `HTTPS_PROXY=http://crackbox:3128`
- `NO_PROXY=localhost,127.0.0.1,gated,crackbox` — direct connections
  to in-instance services bypass the proxy
- `NODE_OPTIONS=--require=/app/proxy-shim.js` — installs the global
  fetch agent

If you see a 403 from the proxy for a host you expect to work, look
at the folder's resolved allowlist via `arizuko network <inst>
resolve <folder>` and ask the operator to add what's missing.
