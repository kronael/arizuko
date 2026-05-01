---
status: unshipped
---

# Gateway self-modification

Root agent modifies the gateway codebase (not just skills/MCP). Two
strawmen:

- **Plugin dir** — `plugins/{actions,handlers,channels}/` loaded at
  startup; `/workspace/self/plugins` mounted rw. Upstream updates don't
  touch.
- **Agent branch + CI** — rw mount of full repo, agent commits to
  `agent/<instance>`, CI tests + builds + deploys; human reviews.

Rationale: root agent runs full Claude Code but can only read gateway
source today; every change needs human `make build && make image`.

Unblockers: pick scope (plugin-only vs full repo), testing story
(staging instance vs hot reload vs unit tests), rollback (immutable
images vs revert), permission model (root-only vs root+approval).
Related: [d-agent-code-modification.md](d-agent-code-modification.md).
