---
status: shipped
shipped: 2026-09-03
source: paradigmxyz/centaur@20f3021 (Apache-2.0) peel, 2026-08-10
depends: [../5/13-ext-mcp, ../5/14-credentials]
---

# specs/6/19 — one declaration shape for external tools

One `[[connector]]` declaration with a `transport` discriminator, one
`{secret:KEY}` placeholder usable in three sinks, and one loader rule:
ordered sources, later shadows earlier, loud warn. Execution shapes stay
exactly as `5/13` ships them — this unifies the DECLARATION, never the
dispatch.

## Centaur's evidence

One schema, `[tool.centaur]` in each tool's `pyproject.toml`, covers
every tool's identity, egress hosts, and credentials
(`services/api-rs/crates/centaur-perms/src/tools.rs:358-395`):

- **Typed secret entries.** `secrets = [{ type = "http" | "oauth_token"
| "aws_auth" | "hmac_sign" | "pg_dsn" | "gcp_auth" | "gcp_id_token" |
"brokered_token", name, hosts, ... }]` — each type carries its own
  fields (`tools/infra/datadog/pyproject.toml:26-40` http replace-mode;
  `tools/infra/cloudwatch/pyproject.toml:36-47` aws_auth with
  `allowed_services`; `tools/productivity/gsuite/pyproject.toml:35-62`
  oauth_token pulling `fields[].json_key` out of one JSON secret;
  `tools/research/websearch/pyproject.toml:36-39` `optional_secrets`
  with `mode = "inject"` — the tool process never sees a value).
- **One declaration feeds two enforcement planes.** The same parsed
  secrets build the per-sandbox egress-proxy config at spawn
  (`centaur-api-server/src/tool_discovery.rs:139-159`
  `discover_tool_proxy_fragment`) AND the operator-side credential
  provisioning (`centaur-perms/src/main.rs:605-648` → iron-control).
  Declaration-derived enforcement is the load-bearing idea: a tool
  cannot need a host or a credential its declaration doesn't name.
- **Ordered sources, later shadows earlier, loud.** `tools.toml:1-5`
  declares ordered `plugin_dirs`; the collector overwrites an
  earlier-dir entry and logs a shadow warning
  (`tool_discovery.rs:347-407`, resolution order `:90-124`).

Their cautionary tale is equally load-bearing: that schema is parsed by
TWO Rust implementations (`centaur-perms/src/tools.rs`, ~1084 lines, and
`centaur-api-server/src/tool_discovery.rs`, ~1977 lines) which both cite
a Python `services/api/api/tool_manager.py` as "the source of truth"
(`tools.rs:3-9`) — a file that does not exist in the tree. Two live
copies drifting against a phantom canonical is exactly what the
one-renderer rule exists to prevent.

Also noted for scale, not for copying: Centaur has exactly ONE execution
model — every tool is a `uvx`-run CLI shim, no MCP anywhere
(`centaur-sandbox-agent-k8s/src/tools.rs:1-7`,
`services/sandbox/install_tool_shims.py`). One execution shape is what
makes their one-declaration trivially true. arizuko keeps its execution
shapes; only the declaration unifies.

## The shape

**One `[[connector]]` block, `transport = "mcp" | "rest"`.** There is no
subprocess transport: an endpoint needs no runtime, a subprocess does, and
routd's image has none. `mcp` discovers its own tools; `rest` declares them in
`[[connector.tool]]`. One loader reads both; grant namespaces (`mcp:` / `ext:`)
and dispatch paths are separate.

**Secrets: one placeholder, three sinks.** Name the keys in `secrets`, and
write `{secret:KEY}` where the value goes:

```toml
[[connector]]
name      = "cloudflare"
transport = "rest"
url       = "https://api.cloudflare.com/client/v4"
secrets   = ["CF_API_TOKEN"]
headers   = { Authorization = "Bearer {secret:CF_API_TOKEN}" }

  [[connector.tool]]
  name   = "dns_list"
  scope  = "ext:cloudflare:dns:read"
  method = "GET"
  path   = "/zones/{zone_id}/dns_records"
```

`headers`, `query` and `body` are the three sinks, and they are forced by the
four providers that exist rather than invented: cloudflare and gandi are
`bearer` (a header), namecheap is `apikey-query` (a query param), porkbun is
`json-body` (two values in the JSON body). One substitution rule covers all
four. The placeholder form has no ceiling on the number of secrets and no enum
to extend for the fifth provider.

**The loader rule**: sources are ordered (embedded built-ins in
`routd/extproviders/*.toml`, then the operator file), later shadows earlier BY
NAME, every shadow logged loud.

## Risk

Over-generalizing — held off by the rule that `transport` values must stay
mechanically distinct dispatch paths (`mcp_tool_naming` discipline). The moment
a field means different things per transport, the shape has failed. `mcp` reads
`headers` and ignores `query`/`body`/`tool`; `rest` reads all four. That is a
field being ABSENT for a transport, never a field changing meaning.

## Attribution

Analysis derives from reading `paradigmxyz/centaur` (Apache-2.0), commit
`20f3021`: `tools.toml`, `tools/*/pyproject.toml`,
`services/api-rs/crates/centaur-perms/src/tools.rs`,
`services/api-rs/crates/centaur-api-server/src/tool_discovery.rs`,
`services/api-rs/crates/centaur-sandbox-agent-k8s/src/tools.rs`,
`services/sandbox/install_tool_shims.py`. No code was copied.
