---
status: active
---

# specs/16 — products

Launching products built on arizuko: persona templates, packaging,
and the publish surface that lets operators deploy a configured agent
out of the box.

## Infrastructure

| Spec                                                 | Status      | Hook                                                                                                                                         |
| ---------------------------------------------------- | ----------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| [→ 5/21-products](../5/21-products.md)               | moved       | Producer-side product spec (PRODUCT.md format, catalog, authoring) pulled into platform core next to 5/20.                                   |
| [P-product-templates.md](P-product-templates.md)     | draft       | Pointer stub → 5/21-products (merged).                                                                                                       |
| [chat-web-app.md](chat-web-app.md)                   | draft       | Web chat UI surface; ant link + dashboard companion app.                                                                                     |
| [2-support-skill.md](2-support-skill.md)             | draft       | `/support` orchestrator: primary-source citation + multi-turn case threading.                                                                |
| [→ 5/19-hitl-firewall](../5/19-hitl-firewall.md)     | moved       | Pulled into platform core (`5/19`) + collapsed per codex/fable: one `pending_actions` resreg resource + injected `CheckHold`, no dispatcher. |
| [5-authoring-product.md](5-authoring-product.md)     | draft       | Authoring agent design reference (see product-creator.md).                                                                                   |
| [6-web-routes.md](6-web-routes.md)                   | draft       | Agent-controlled web routing: set_web_route MCP tools + direct DB lookup.                                                                    |
| [→ 5/20-ant-portability](../5/20-ant-portability.md) | moved       | Pulled into platform core (5/20), rewritten pg_dump-style: one export/apply, meta-only YAML or `--files` tar.gz; no lockfile/arzpack.        |
| [1-git-channel.md](1-git-channel.md)                 | draft       | `gitd` adapter — repos as channels, PR/issue/commit as messages, repo=workspace.                                                             |
| [3-file-event-stream.md](3-file-event-stream.md)     | draft       | `filewd` inotify watcher in agent container → MCP `file_event` → SSE + audit.                                                                |
| [8-company-brain.md](8-company-brain.md)             | not planned | Positioning: arizuko as the action layer (not retrieval) for "company brain" use cases.                                                      |
| [9-positioning.md](9-positioning.md)                 | research    | Market positioning vs LangGraph/CrewAI/Dify/n8n/managed-cloud; arizuko's gaps + angle.                                                       |

Platform/API surface lives in [specs/5/](../5/) — products consume the
control API; the API design ships before the products that depend on it.

## Product catalog

Each product ships as `ant/examples/<name>/` and installs via
`arizuko create <instance> --product <name>`. Public page at `/pub/products/<name>/`.

Developer capabilities are embedded in each product that needs them
(oracle + bash grants, scoped per deployment) — not a separate product.

| Spec                                                           | Name       | Brand      | Value prop                                                                 | Blocked by         |
| -------------------------------------------------------------- | ---------- | ---------- | -------------------------------------------------------------------------- | ------------------ |
| [product-personal-assistant.md](product-personal-assistant.md) | personal   | fiu        | Personal assistant with persistent memory                                  |                    |
| [product-support.md](product-support.md)                       | support    | atlas      | KB-backed Q&A via ant link; escalates to human                             |                    |
| [product-trip.md](product-trip.md)                             | trip       | may        | Multi-step travel research → structured itinerary                          |                    |
| [product-strategy.md](product-strategy.md)                     | strategy   | prometheus | Domain tracker; weekly synthesis → team briefing                           |                    |
| [product-pm.md](product-pm.md)                                 | pm         | sloth      | Team task board + weekly digest                                            |                    |
| [product-reality.md](product-reality.md)                       | reality    | rhias      | Ongoing life-context thread holder                                         |                    |
| [product-creator.md](product-creator.md)                       | creator    | inari      | Curation + draft pipeline; approve before publish                          | HITL firewall      |
| [product-socials.md](product-socials.md)                       | socials    | phosphene  | Multi-platform distribution; schedule + engagement                         | HITL + rate limits |
| [product-aws-devops.md](product-aws-devops.md)                 | aws-devops | argus      | AWS SRE agent; per-operator keys, read-before-write, CloudTrail-attributed |                    |

## Arizuko features required per product

| Feature (shipped ✓ / unshipped ✗) | Personal | Support | Trip  | Strategy | PM  | Reality | Creator | Socials |
| --------------------------------- | :------: | :-----: | :---: | :------: | :-: | :-----: | :-----: | :-----: |
| ant link (slink) ✓                |    –     |  **✓**  |   –   |    –     |  –  |    –    |    –    |    –    |
| onbod / user reg ✓                |    –     |  **✓**  |   –   |    –     |  –  |    –    |    –    |    –    |
| oracle ✓                          |    –     |    –    | **✓** |  **✓**   |  –  |    –    |  **✓**  |    –    |
| davd ✓                            |    –     |    –    | **✓** |  **✓**   |  –  |    –    |  **✓**  |    –    |
| timed ✓                           |    –     |    –    |   –   |  **✓**   |  –  |  **✓**  |  **✓**  |  **✓**  |
| social adapters ✓                 |    –     |    –    |   –   |    –     |  –  |    –    |  **✓**  |  **✓**  |
| send_file ✓                       |    –     |    –    | **✓** |  **✓**   |  –  |    –    |    –    |    –    |
| rate limits ✗                     |    –     |    ✗    |   –   |    –     |  –  |    –    |    –    |    ✗    |
| HITL firewall ✗                   |    –     |    –    |   –   |    –     |  –  |    –    |    ✗    |    ✗    |

## Products in spec only (not yet in ant/examples/)

Specced in this directory but no template folder shipped yet:

| Spec                                           | Value prop                                                        |
| ---------------------------------------------- | ----------------------------------------------------------------- |
| [product-ops.md](product-ops.md)               | DevOps/SRE with runbooks + scoped bash                            |
| [product-companion.md](product-companion.md)   | Personal companion with proactive check-ins                       |
| [product-slack-team.md](product-slack-team.md) | Slack team agent — shared channel persona, per-user memory/grants |
