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
| [5-authoring-product.md](5-authoring-product.md) | deferred | Authoring agent design (superseded by product-writer.md once HITL ships).  |

## Product catalog

Each product ships as `ant/examples/<name>/` and has a page at `/pub/products/<name>/`.

| Spec                                           | Status  | Tagline                                           |
| ---------------------------------------------- | ------- | ------------------------------------------------- |
| [product-assistant.md](product-assistant.md)   | planned | General-purpose; memory-enabled; any channel      |
| [product-developer.md](product-developer.md)   | planned | Coding assistant with codebase access via davd    |
| [product-researcher.md](product-researcher.md) | planned | Research + synthesis; cites sources; structured   |
| [product-writer.md](product-writer.md)         | planned | Drafts content; operator approves before publish  |
| [product-ops.md](product-ops.md)               | planned | DevOps/SRE; runbooks in facts/; daily digest      |
| [product-support.md](product-support.md)       | planned | Customer support via slink widget; escalates      |
| [product-companion.md](product-companion.md)   | planned | Personal companion; proactive check-ins via timed |
