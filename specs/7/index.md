---
status: active
---

# specs/7 — products

Launching products built on arizuko: persona templates, packaging,
and the publish surface that lets operators deploy a configured agent
out of the box.

## Infrastructure

| Spec                                             | Status   | Hook                                                                          |
| ------------------------------------------------ | -------- | ----------------------------------------------------------------------------- |
| [R-products.md](R-products.md)                   | active   | Curated persona+skill templates; `--product` flag on `arizuko create`.        |
| [4-hitl-firewall.md](4-hitl-firewall.md)         | deferred | pending_actions queue + /dash/review; holds MCP calls for operator review.    |
| [5-authoring-product.md](5-authoring-product.md) | deferred | Authoring agent design reference (see product-creator.md).                    |
| [6-web-routes.md](6-web-routes.md)               | spec     | Agent-controlled web routing: set_web_route MCP tools + direct DB lookup.     |
| [2-support-skill.md](2-support-skill.md)         | spec     | `/support` orchestrator: primary-source citation + multi-turn case threading. |

Platform/API surface moved to [specs/6/](../6/) — products consume the
control API; the API design ships before the products that depend on it.

## Product catalog

Each product ships as `ant/examples/<name>/` and installs via
`arizuko create <instance> --product <name>`. Public page at `/pub/products/<name>/`.

Developer capabilities are embedded in each product that needs them
(oracle + bash grants, scoped per deployment) — not a separate product.

| Spec                                                           | Name     | Brand      | Value prop                                         | Blocked by         |
| -------------------------------------------------------------- | -------- | ---------- | -------------------------------------------------- | ------------------ |
| [product-personal-assistant.md](product-personal-assistant.md) | personal | fiu        | Personal assistant with persistent memory          |                    |
| [product-support.md](product-support.md)                       | support  | atlas      | KB-backed Q&A via ant link; escalates to human     |                    |
| [product-trip.md](product-trip.md)                             | trip     | may        | Multi-step travel research → structured itinerary  |                    |
| [product-strategy.md](product-strategy.md)                     | strategy | prometheus | Domain tracker; weekly synthesis → team briefing   |                    |
| [product-pm.md](product-pm.md)                                 | pm       | sloth      | Team task board + weekly digest                    |                    |
| [product-reality.md](product-reality.md)                       | reality  | rhias      | Ongoing life-context thread holder                 |                    |
| [product-creator.md](product-creator.md)                       | creator  | inari      | Curation + draft pipeline; approve before publish  | HITL firewall      |
| [product-socials.md](product-socials.md)                       | socials  | phosphene  | Multi-platform distribution; schedule + engagement | HITL + rate limits |

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
