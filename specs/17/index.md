---
status: active
---

# specs/17 — products

Products built on arizuko: what a product _is_ lives in
[5/21-products](../5/21-products.md) (PRODUCT.md format, `--product`
install flow, authoring guide, shipped catalog). This phase holds the
product-adjacent designs that are not yet platform core.

Platform/API surface lives in [specs/5/](../5/) — products consume the
control API; the API ships before the products that depend on it.

## Specs

| Spec                                             | Status      | Hook                                                                                                                                                                                        |
| ------------------------------------------------ | ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [product-aws-devops.md](product-aws-devops.md)   | defected    | AWS SRE product; per-operator keys (BYOA), read-before-write, CloudTrail-attributed. The one product with its own design — BLOCKED on `X2`: its keys-in-container-env model died with `X1`. |
| [A-socials-daemon.md](A-socials-daemon.md)       | planned     | Broadcast platforms leave chanlib for one socials-daemon MCP surface (decision 2026-07-13).                                                                                                 |
| [1-git-channel.md](1-git-channel.md)             | draft       | `gitd` adapter — repos as channels, PR/issue/commit as messages, repo = workspace.                                                                                                          |
| [2-support-skill.md](2-support-skill.md)         | draft       | `/support` orchestrator: primary-source citation + multi-turn case threading.                                                                                                               |
| [3-file-event-stream.md](3-file-event-stream.md) | draft       | `filewd` inotify watcher in the agent container → MCP `file_event` → SSE + audit.                                                                                                           |
| [chat-web-app.md](chat-web-app.md)               | draft       | React/Vite chat SPA at `/pub/chat/`; replaces webd's HTMX pages.                                                                                                                            |
| [8-company-brain.md](8-company-brain.md)         | not planned | Positioning: arizuko as the action layer (not retrieval) for "company brain".                                                                                                               |
| [9-positioning.md](9-positioning.md)             | research    | Competitive landscape vs LangGraph/CrewAI/Dify/n8n/managed cloud.                                                                                                                           |
| [→ 5/19-hitl-firewall](../5/19-hitl-firewall.md) | moved       | Pulled into platform core; one `pending_actions` resreg resource + injected `CheckHold`, no dispatcher.                                                                                     |
| [→ 5/28-packages](../5/28-packages.md)           | moved       | Pulled into platform core, then dissolved 2026-08-04: composition folded into `5/28`, state transport pointed at `5/8`.                                                                     |
| [→ 5/21-products](../5/21-products.md)           | moved       | Producer-side product spec (PRODUCT.md format, catalog, authoring).                                                                                                                         |

## Product catalog

The shipped catalog — name, brand, tagline, `facts/` seed — is a table in
[5/21-products](../5/21-products.md) `## Current catalog`. It is **not**
restated here. Per-product truth lives in three places, none of which is a
spec file:

- `ant/examples/<name>/PRODUCT.md` — skills, `[[env]]`, operator setup.
- `ant/examples/<name>/{PERSONA,SOUL,BRANDING}.md` — voice and register.
- `template/web/pub/arizuko/products/<name>/` — the public pitch.

A product earns a spec file here only when it carries a design decision
those three cannot express — so far only `product-aws-devops.md` does
(the per-operator credential model and the in-container-vs-connector
surface choice).

### Unshipped product ideas

No `ant/examples/` template, no page — intent only, kept as lines:

- **ops** — DevOps/SRE with runbooks in `facts/` + scoped `bash`; daily
  health digest via `timed`. Superseded in practice by `aws-devops`,
  which is the same shape made concrete; revive only if a
  cloud-neutral runbook agent is actually wanted.
- **companion** — proactive personal check-ins via `timed`; no code
  tools, memory + presence only. Distinct from `personal` (capability-first)
  and `reality` (context-first) by having _no_ task surface at all.

### Cross-product blockers

Two unshipped platform features gate three products:

| Blocker                                                 | Blocks           |
| ------------------------------------------------------- | ---------------- |
| HITL firewall ([5/19](../5/19-hitl-firewall.md))        | creator, socials |
| Per-channel rate limits ([11/4](../11/19-cost-caps.md)) | support, socials |

## Removed in the 2026-08-02 minimization

- `product-{personal-assistant,support,trip,strategy,pm,reality,creator,socials,slack-team}.md`
  — **deleted**. Each restated its shipped `ant/examples/<name>/PRODUCT.md`
  (skills, env, setup) plus the web page's pitch, and had already drifted
  from both (e.g. `trip`/`creator` specs listed a `facts` skill their
  manifests omit). `product-slack-team.md` additionally asserted four
  claims the code contradicts — see below.
- `product-{ops,companion}.md` — **deleted**, folded into "Unshipped
  product ideas" above. Neither ships a template; both were one
  paragraph of intent under a template-shaped heading.
- `P-product-templates.md` — **deleted**. A pointer stub to
  `5/21-products`; inbound links repointed there.
- `5-authoring-product.md` — **deleted**. It was an earlier name for what
  shipped as the `creator` product (`ant/examples/creator/`); the index
  already said "see product-creator.md". Its one non-duplicate idea —
  each authoring group renders drafts to a `content_target` under `/pub/`,
  preview vs permanent — is recorded here and nowhere else; it needs a
  `5/V` web-vhost decision before it is a design.
- `6-web-routes.md` — **deleted, shipped**. `web_routes` landed in
  `store/migrations/0045-web-routes.sql` + `store/web_routes.go`, read by
  `proxyd/main.go` and managed by the `set_web_route`/`del_web_route`/
  `list_web_routes` MCP tools (`ipc/ipc.go`). The spec had already drifted:
  it specified a 10s `sync.RWMutex` route cache; proxyd takes a
  per-request snapshot instead (`proxyd/main.go:140`, spec `5/8`).

### Claims corrected, not carried forward

`product-slack-team.md` was written in the "verified against source"
style but its four distinguishing claims are false against `main`:

- "Per-user secrets are never merged for group chats
  (`GetChatIsGroup == false` guard)" — **false**. `routd/dispatch.go:523`
  calls `FolderSecretsForUser(folder, caller)` unconditionally for every
  non-timed, non-system turn. `GetChatIsGroup` has zero production
  callers (`store/groups.go:217`, referenced only from tests).
- "`arizuko secret set` is spec'd but not shipped" — **false**.
  `cmd/arizuko/secret.go:36` (`secret`) and `:43` (`user-secret`).
- "No dash UI to set a folder secret" — **false**. `dashd/me_secrets.go`
  serves `/dash/me/secrets`.
- "`slack` is absent from `router.platformShort`, so user files fall back
  to a 2-char prefix" — **false**. `router/router.go:218` maps
  `"slack": "sl"` explicitly; its own Open item asking for this is done.

The true version of the per-user credential path is documented with code
pointers in [product-aws-devops.md](product-aws-devops.md).
