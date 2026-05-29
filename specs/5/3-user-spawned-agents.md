---
status: draft
---

# User-spawned agents

An end user (not the operator) submits an agent definition — persona,
skills, channel binding — and arizuko spawns a tenant for them, returns
an access token, and lets them interact. The operator runs the
platform; the platform runs the tenants.

Rides on [5-uniform-mcp-rest.md](5-uniform-mcp-rest.md) (federated `/v1/*` and
capability tokens) and [U-genericization.md](U-genericization.md)
(`types.Folder`, `runed`). Shares its seeding code with
[../8/R-products.md](../8/R-products.md) — products are the static path,
this is the dynamic path. Reference for the API shape:
[`refs/openclaw-managed-agents/openapi/openapi.yaml`](../../refs/openclaw-managed-agents/openapi/openapi.yaml).

## Why

`arizuko create --product <name>` is operator-only: the operator picks
one of `ant/examples/` and the platform seeds the group. That doesn't
cover the case where the user _is_ the agent author — e.g. someone
running arizuko offers self-service tenant creation to friends, or a
product like `slack-team` wants end-user onboarding to be "describe
your agent" rather than "pick from a menu".

## Trust model

Runtime sandbox is unchanged — same crackbox boundary
([../11/12-crackbox-sandboxing.md](../11/12-crackbox-sandboxing.md)),
same MCP socket model. The new surface is the _creation_ gate.

- **Per-instance cap.** `USER_SPAWNED_TENANTS_MAX` in `.env` (default
  `0` = feature off). Operator opts in.
- **Per-user cap.** `USER_SPAWNED_TENANTS_PER_USER` (default `3`)
  live tenants per authenticated user.
- **Approval queue.** Default: every `POST /v1/agents` lands in
  `admissions` (onbod), operator approves. `USER_SPAWNED_AUTO_APPROVE=true`
  skips for closed-friend instances.
- **Capability scope.** The token minted by central `authd` at creation
  (per 5/5 §Token model, which consumes authd-issued tokens) carries
  `folder=agents/<user_sub>/<agent_name>` and the capability scopes
  `messages:{read,send}` + `tasks:read` (plus a `user_spawned` marker
  scope if a deployment wants to single these tenants out). There is no
  `tier` — authorization is scope-match
  ([`U-genericization.md`](U-genericization.md) "Capability-vs-tier").
  No `grants:write`, no `routes:write` outside the subtree.
- **Skill allowlist.** Definition's `skills` is intersected with
  `USER_SPAWNED_ALLOWED_SKILLS` (default: `diary`, `facts`,
  `recall-memories`, `web`). The agent cannot opt itself into
  `bash`, `oracle`, or any tool the operator hasn't whitelisted.

The hard rule: runtime cannot escalate beyond what the creation token
granted. Grant mutation stays operator-only.

## API surface

All on the federated `/v1/*` (5/5 §Daemon ownership); the
agent resource lives on `gated`. Seven endpoints:
`POST /v1/agents` (submit; 202 if queued, 200 + token if auto-approved),
`GET /v1/agents` (list caller's), `GET/PATCH/DELETE /v1/agents/{id}`,
`GET /v1/agents/{id}/versions`, `POST /v1/agents/{id}/archive`.
`PATCH` body MUST carry the last-read `version`; mismatch or archived → 409.

### Definition body

```json
{
  "name": "lab-notes",
  "persona": { "soul_md": "...", "claude_md": "..." },
  "skills": ["diary", "facts", "recall-memories"],
  "channels": {
    "slink": { "enabled": true },
    "telegram": { "chat_id": "tg:-123456789", "optional": true }
  },
  "tasks": [{ "cron": "0 9 * * MON", "prompt": "weekly digest" }],
  "version": 1
}
```

Shape mirrors `PRODUCT.md` + `PERSONA.md` + `tasks.toml` so
`container.SetupGroup` accepts either source. Channel `chat_id` must
reference a platform adapter the operator already runs.

### Versioning & idempotency

Each accepted PATCH appends to `user_agent_versions` and bumps the
counter — matches openclaw's optimistic-concurrency pattern. `POST`
accepts `Idempotency-Key`; same key + same body within 24h returns the
original result, different body → 409.

## Storage

Tenant tree: `groups/agents/<user_sub>/<agent_name>/` — same structure
as `ant/examples/*` so existing seeding code is reused. `<user_sub>`
is the stable subject from proxyd/onbod.

New tables (owned by `gated` per 5/5):

| Table                 | Columns                                                                |
| --------------------- | ---------------------------------------------------------------------- |
| `user_agents`         | `id, user_sub, name, folder, version, status, created_at, archived_at` |
| `user_agent_versions` | `agent_id, version, body_json, created_at`                             |

`user_agents.folder` joins to existing `groups`; a `created_by`
column on `groups` distinguishes user-spawned from operator-seeded.

## Spawn flow

```
user --POST /v1/agents--> gated
  ├── validate (skill allowlist, channel ownership, cap)
  ├── queued: insert admissions row, return 202
  └── approved:
        ├── insert groups + user_agents + user_agent_versions
        ├── runed.SetupGroup(folder, definition)
        ├── register channel bindings via gated/v1/routes
        ├── mint slink/MCP token (5/5 §Token)
        └── return { agent, token, slink_url }
```

`arizuko create --product <name>` collapses to the same flow with
`user_sub=operator`, `auto_approve=true`, definition sourced from
`ant/examples/<name>/`. One code path; the menu is the operator's
shortcut. `runed` is per U-genericization Phase C; until that
ships, `container.SetupGroup` + gateway do the job.

## Out of scope

- **Arbitrary code in skills.** Skills stay markdown. Unknown skill
  names rejected at SetupGroup time.
- **Cross-tenant data access.** Token's `folder` claim is the
  boundary, verified per-request.
- **Runtime grant mutation.** Users cannot PATCH grants. Operator-only.
- **New channel adapters.** Users bind to channels the operator
  already runs; spawning a new platform adapter is a separate spec.
- **Per-tenant resource quotas.** Cost/token caps (cf. openclaw `Quota`)
  are a later spec; only the platform-wide caps apply today.

## Touches

| Daemon                           | Change                                                                                    |
| -------------------------------- | ----------------------------------------------------------------------------------------- |
| `gated`                          | `/v1/agents` routes; tables `user_agents`, `user_agent_versions`; `groups.created_by`.    |
| `runed` / `container.SetupGroup` | Accept definition body as alternative to product folder.                                  |
| `onbod`                          | Reuse admissions queue for approvals when not auto-approving.                             |
| `authd`                          | Mints the creation token (sole signer); defines the user-spawned scope-set (no new tier). |
| `dashd`                          | Operator UI for approval queue + tenant list.                                             |

Migration: `store/migrations/NNN-user-agents.sql`.

## Open

- **Auto-approve heuristics.** Trusted IdP / email domain — follow-up.
- **Delete grace.** Lean: 30 days, per-instance configurable.
- **Skill version pinning.** Lean: reuse `MIGRATION_VERSION` policy.
