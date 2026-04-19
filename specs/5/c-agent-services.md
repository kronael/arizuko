---
status: unshipped
---

# Agent-managed services (`servd`)

Agents declare long-running services (web server, bot, API) as
desired state; `servd` reconciles docker containers inside the compose
stack. Sibling to `gated` and `timed`, same shared SQLite DB.

Schema: `services(id, folder, name, image, args, env, ports, restart,
desired, actual, container, error, ...)`. Poll interval ~5s.
`servd` owns `arizuko-svc-<folder>-<name>` container names. Images
pushed externally; bumping `image` field triggers rolling restart on
next reconcile.

MCP tools (scoped to caller's folder): `service_define`,
`service_start`, `service_stop`, `service_restart`, `service_logs`,
`service_list`. Privileged ports (<1024) blocked.

Rationale: agents can run one-shot (`gated`) or cron (`timed`)
containers, but nothing persistent. No need for root — servd uses the
docker socket like gated.

Unblockers: schema, reconciler, MCP tools, port validation across
groups. Out of scope: vhosts, service discovery, cpu/mem quotas.
