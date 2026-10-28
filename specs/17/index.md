---
status: active
---

# specs/17 — products

Products built on arizuko: what a product _is_ lives in
[EXTENDING.md](../../EXTENDING.md#installing-packages) (arizuko.yml format, `--product`
install flow, authoring guide, shipped catalog). This phase holds the
product-adjacent designs that are not yet platform core.

Platform/API surface lives in [specs/5/](../5/) — products consume the
control API; the API ships before the products that depend on it.

## Specs

| Spec                                             | Status      | Hook                                                                                                                             |
| ------------------------------------------------ | ----------- | -------------------------------------------------------------------------------------------------------------------------------- |
| [A-socials-daemon.md](A-socials-daemon.md)       | planned     | Broadcast platforms leave chanlib for one socials-daemon MCP surface (decision 2026-07-13).                                      |
| [3-file-event-stream.md](3-file-event-stream.md) | draft       | `filewd` inotify watcher in the agent container → MCP `file_event` → SSE + audit.                                                |
| [4-chat-web-app.md](4-chat-web-app.md)           | draft       | React/Vite chat SPA at `/pub/chat/`; replaces webd's HTMX pages.                                                                 |
| [8-company-brain.md](8-company-brain.md)         | not planned | Positioning: arizuko as the action layer (not retrieval) for "company brain".                                                    |
| [9-positioning.md](9-positioning.md)             | research    | Competitive landscape vs LangGraph/CrewAI/Dify/n8n/managed cloud.                                                                |
| [→ 6/27-git-channel](../6/27-git-channel.md)     | moved       | `gitd` is a channel adapter — platform, not a product; moved 2026-08-25 to the active platform phase, beside its sibling `6/26`. |
| [→ 5/19-hitl-firewall](../5/19-hitl-firewall.md) | moved       | Pulled into platform core; one `pending_actions` resreg resource + injected `CheckHold`, no dispatcher.                          |

## Product catalog

The shipped catalog lives in `ant/examples/`. [EXTENDING.md](../../EXTENDING.md#installing-packages) describes package selection. The catalog is **not** restated here. Per-product truth lives in three places, none of which is a
spec file:

- `ant/examples/<name>/arizuko.yml` — skills, `env`, operator setup.
- `ant/examples/<name>/{PERSONA,SOUL,BRANDING}.md` — voice and register.
- `template/web/pub/arizuko/products/<name>/` — the public pitch.

A product earns a spec file here only when it carries a design decision
those three cannot express (none currently — the `github` product's one
decision, gitd, is a platform spec: [6/27](../6/27-git-channel.md)).

### Unshipped product ideas

No `ant/examples/` template, no page — intent only, kept as lines:

- **ops** — DevOps/SRE with runbooks in `facts/` + scoped `bash`; daily
  health digest via `timed`. Superseded in practice by later products,
  which is the same shape made concrete; revive only if a
  cloud-neutral runbook agent is actually wanted.
- **companion** — proactive personal check-ins via `timed`; no code
  tools, memory + presence only. Distinct from `personal` (capability-first)
  and `reality` (context-first) by having _no_ task surface at all.

### Cross-product blockers

The HITL firewall ([5/19](../5/19-hitl-firewall.md)) shipped 2026-08-07 and
no longer blocks `creator`/`socials`. What remains: per-channel budgets
([6/39-gateway-owns-spend](../6/39-gateway-owns-spend.md), the
cap the gateway enforces)
gate `support` and `socials`.

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
  `EXTENDING.md`; authoring instructions live there.
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

The corrected per-user credential path was recorded, with code pointers,
in `product-aws-devops.md` and left with it (retired 2026-08-20).
