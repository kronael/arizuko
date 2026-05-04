---
status: active
---

# specs/6 — products

Launching products built on arizuko: persona templates, packaging,
and the publish surface that lets operators deploy a configured agent
out of the box.

## Infrastructure

| Spec                                             | Status   | Hook                                                                       |
| ------------------------------------------------ | -------- | -------------------------------------------------------------------------- |
| [R-products.md](R-products.md)                   | planned  | Curated persona+skill templates; `--product` flag on `arizuko create`.     |
| [4-hitl-firewall.md](4-hitl-firewall.md)         | deferred | pending_actions queue + /dash/review; holds MCP calls for operator review. |
| [5-authoring-product.md](5-authoring-product.md) | deferred | Authoring agent design reference (see product-creator.md).                 |

## Product catalog

Each ships as `ant/examples/<name>/` with a page at `/pub/products/<name>/`.

Developer capabilities are embedded in each product that needs them
(oracle + bash grants, scoped per deployment) — not a separate product.

| Spec                                         | Status  | Value prop                                         | Ships now? |
| -------------------------------------------- | ------- | -------------------------------------------------- | ---------- |
| [product-personall.md](product-personall.md) | planned | Branded/personal assistant; the configurable base  | ✓          |
| [product-support.md](product-support.md)     | planned | Customer-facing Q&A via slink; escalates to human  | ✓ (v1)     |
| [product-trip.md](product-trip.md)           | planned | Multi-step travel research → structured itinerary  | ✓          |
| [product-strategy.md](product-strategy.md)   | planned | Domain tracker; weekly synthesis → team briefing   | ✓          |
| [product-creator.md](product-creator.md)     | planned | Curation + draft pipeline; approve before publish  | ✓ (v1)     |
| [product-socials.md](product-socials.md)     | planned | Multi-platform distribution; schedule + engagement | blocked\*  |

\* socials needs HITL firewall + rate limits before production use.

## Arizuko features required per product

| Feature (shipped ✓ / unshipped ✗) | Persona | Support | Trip  | Strategy | Creator | Socials |
| --------------------------------- | :-----: | :-----: | :---: | :------: | :-----: | :-----: |
| slink widget ✓                    |    –    |  **✓**  |   –   |    –     |    –    |    –    |
| onbod / user reg ✓                |    –    |  **✓**  |   –   |    –     |    –    |    –    |
| oracle ✓                          |    –    |    –    | **✓** |  **✓**   |  **✓**  |    –    |
| davd ✓                            |    –    |    –    | **✓** |  **✓**   |  **✓**  |    –    |
| timed ✓                           |    –    |    –    |   –   |  **✓**   |  **✓**  |  **✓**  |
| social adapters ✓                 |    –    |    –    |   –   |    –     |  **✓**  |  **✓**  |
| send_file ✓                       |    –    |    –    | **✓** |  **✓**   |    –    |    –    |
| rate limits ✗                     |    –    |    ✗    |   –   |    –     |    –    |    ✗    |
| HITL firewall ✗                   |    –    |    –    |   –   |    –     |    ✗    |    ✗    |

## Future products (not in current scope)

- developer — coding assistant with codebase access via davd
- ops — DevOps/SRE with runbooks + scoped bash
- companion — personal companion with proactive check-ins

These share skills with the catalog above; specced in this directory
but not in the active shipping queue.
