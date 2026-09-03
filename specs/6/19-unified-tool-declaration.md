---
status: draft
source: paradigmxyz/centaur@20f3021 (Apache-2.0) peel, 2026-08-10
depends: [../5/13-ext-mcp, ../5/14-credentials]
---

# specs/6/19 — one declaration shape for external tools

Proposal: collapse the two `connectors.toml` block shapes into one
`[[connector]]` declaration with a `transport` discriminator, adopt a
typed secret-entry vocabulary shared with the egress proxy
(an egress credential proxy — declined 2026-08-20), and add the one loader rule
both shapes miss today: ordered sources, later shadows earlier, loud
warn. Execution shapes stay exactly as `5/13` ships them — this unifies
the DECLARATION, never the dispatch.

## The two shapes this replaced (measured 2026-08-20)

External capability tools split across two TOML shapes with two loaders
and two registration loops:

- `ExtTool` (`ipc/extcall.go:20-42`) — `[[ext]]` REST descriptors:
  positional `SecretKey`/`SecretKey2` plus per-method fields
  (`Header`/`Header2`/`Param`), auth method enum
  `bearer|apikey-header|apikey-query|basic|json-body`.
- `ConnectorSpec` (`ipc/connector.go:32-39`) — `[[mcp_connector]]` stdio
  subprocess: `command`, flat `secrets` list, `env_template`.
- Registration is two parallel loops (`ipc/ipc.go:997-1069` connectors,
  `ipc/ipc.go:1073-1104` ext tools).
- `LoadExtProviders` (`routd/ext.go:110`) appends operator `[[ext]]`
  blocks after the embedded built-ins with **no shadow or collision
  rule** — an operator block reusing a built-in provider name yields
  duplicate `LocalName`s with unspecified precedence.
- `5/13` shape 2 became HTTP (`url` + `headers`) on 2026-08-30, so the two
  remaining variants are `[[mcp_connector]]` and `[[ext]]` — both now HTTP,
  which makes one block cheaper to reach, not harder.

resreg resources and the hot-tier hand-authored tools are NOT part of
this: they are platform surface with their own two-face discipline
(CLAUDE.md §MCP + REST hand-rolled and uniform). Folding them into a
tool catalog is the DSL the repo already rejected.

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
one-renderer rule exists to prevent, and arizuko's two-shape loader is
the same disease at smaller scale.

Also noted for scale, not for copying: Centaur has exactly ONE execution
model — every tool is a `uvx`-run CLI shim, no MCP anywhere
(`centaur-sandbox-agent-k8s/src/tools.rs:1-7`,
`services/sandbox/install_tool_shims.py`). One execution shape is what
makes their one-declaration trivially true. arizuko keeps its three
execution shapes; only the declaration unifies.

## The delta — DECIDED 2026-09-03

The trigger this spec set for itself has fired. "Do the merge WHEN `5/13` shape 4
(HTTP upstream MCP) is built" — that shipped 2026-08-30, when the stdio connector
was deleted and `[[mcp_connector]]` became HTTP-only. So the migration is now,
and it is cheaper than costed: **no instance in the fleet has a `connectors.toml`
at all** (krons, sloth, marinade, checked 2026-09-03), so the only files to move
are the four embedded `routd/extproviders/*.toml`, in-repo.

**One `[[connector]]` block, `transport = "mcp" | "rest"`.** Two values, not the
three this spec first listed: `stdio` was deleted 2026-08-30 and does not come
back — an endpoint needs no runtime, a subprocess does, and routd's image has
none. `mcp` discovers its own tools; `rest` declares them in
`[[connector.tool]]`, moved under the block unchanged. One loader replaces
`LoadExtProviders` + the `mcp_connector` parse; grant namespaces (`mcp:` /
`ext:`) and dispatch paths do not move.

**Secrets: one placeholder, three sinks — NOT a typed vocabulary.** An earlier
draft of this spec proposed `secrets` as a list of `{key, type, ...}` entries
with the REST auth-method enum as a mode field. That is more machinery than the
evidence asks for. The `[[mcp_connector]]` shape already has the general answer:
name the keys in `secrets`, and write `{secret:KEY}` where the value goes. It
needs exactly one generalisation to absorb every REST provider — the placeholder
must be usable in three sinks, not one:

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
four.

This DELETES the `[ext.auth]` vocabulary — `method`, `secret`, `secret2`,
`header`, `header2`, `param` — six fields for one idea, with a hardcoded ceiling
of two secrets. `secret2`/`header2` is what a shape looks like after it has run
out of room once; porkbun already sits on that ceiling. The placeholder form has
no ceiling and no enum to extend for the fifth provider.

**The loader rule Centaur has and arizuko lacks**: sources are ordered (embedded
built-ins, then operator file), later shadows earlier BY NAME, every shadow
logged loud. Kills the duplicate-`LocalName` ambiguity at the one place it can
be killed. Independently valuable and shipped with the merge.

## Cost

Smaller than first costed, because the fleet exposure turned out to be zero: no
`connectors.toml` exists on any instance, so there is no operator file to
migrate and no compatibility read for the old block names to carry. The work is
the four embedded providers, one loader, and the `ipc.go` registration walk.

Risk: over-generalizing — held off by the rule that `transport` values must stay
mechanically distinct dispatch paths (`mcp_tool_naming` discipline). The moment
a field means different things per transport, the shape has failed. `mcp` reads
`headers` and ignores `query`/`body`/`tool`; `rest` reads all four. That is a
field being ABSENT for a transport, never a field changing meaning.

## Verdict

Ship it. The condition the earlier verdict named ("timed, not standalone") is
met, and the migration cost it was waiting on has since gone to zero.

## Attribution

Analysis derives from reading `paradigmxyz/centaur` (Apache-2.0), commit
`20f3021`: `tools.toml`, `tools/*/pyproject.toml`,
`services/api-rs/crates/centaur-perms/src/tools.rs`,
`services/api-rs/crates/centaur-api-server/src/tool_discovery.rs`,
`services/api-rs/crates/centaur-sandbox-agent-k8s/src/tools.rs`,
`services/sandbox/install_tool_shims.py`. No code was copied.
