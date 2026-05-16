---
name: arizuko
description: >
  Look up info in this deployment's published arizuko docs at
  `/workspace/web/pub/` — concepts, reference, howto, products.
  USE for "how do I X", "where in the docs", "what does <concept>
  mean", "explain <feature>", "check the docs", "how does arizuko do
  Y". NOT for code search (use find), NOT for stored facts (use
  recall-memories), NOT for performing product setup (link the user
  to the setup page).
user-invocable: true
arg: <question or keyword>
---

# Arizuko

The deployment's operator-facing arizuko docs live at
`/workspace/web/pub/` — a mirror of `template/web/pub/` baked in at
container spawn. Public URL: `https://$WEB_HOST/pub/...`.

## Layout

| Subtree              | Contents                                          |
| -------------------- | ------------------------------------------------- |
| `pub/concepts/`      | Narrative — what things are and how they relate   |
| `pub/reference/`     | Lookup tables — env vars, schema, MCP tools       |
| `pub/howto/`         | Guided procedures, product-branded                |
| `pub/products/<s>/`  | Per-product intro + `setup.html`                  |

## Where to look first

| User asks                       | Page                                      |
| ------------------------------- | ----------------------------------------- |
| "How do I set up X"             | `pub/products/<slug>/setup.html` or `pub/howto/<feature>.html` |
| "What does X mean"              | `pub/concepts/<x>.html`                   |
| "Where's the schema for X"      | `pub/reference/schema.html`               |
| "What env vars"                 | `pub/reference/env.html`                  |
| "What MCP tools"                | `pub/reference/mcp.html`                  |
| "What is this product"          | `pub/products/<slug>/index.html`          |

## Protocol

1. Grep for candidates:
   ```bash
   grep -rli '<keyword>' /workspace/web/pub/
   ```
2. `Read` the one or two best matches. Do not read the whole site.
3. Answer from the page.
4. Cite the source. Format: `Source: /pub/concepts/routing.html`.
   On web chat, prefer the public URL — `echo "https://$WEB_HOST/pub/concepts/routing.html"`.

## Setup pages are user-facing

Product setup happens off-bot. When the user asks "set this up for
me", point them at `https://$WEB_HOST/pub/products/<slug>/setup.html`
— do not walk them through the steps in chat.

## When the docs are silent

Say so plainly. Don't invent. Suggest:

- `/find <topic>` if the topic warrants fresh research
- `/recall-memories <topic>` if the operator might have notes
