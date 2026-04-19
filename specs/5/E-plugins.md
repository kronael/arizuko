---
status: unshipped
---

# Plugin proposals

Main group extends arizuko (skill, gateway patch, MCP server, config)
via **agent proposes, operator approves**. Agent writes files to
`/workspace/group/plugins/<name>/`, emits IPC
`{type: "plugin-propose", plugin, kind: skill|patch|config|mcp}`.
Gateway copies to `plugins/pending/<name>/`; operator approves or
rejects; `deploy-plugin.sh` applies.

Rationale: let the main agent extend the system without operator shell
access, with a hard review gate.

Unblockers: `plugin-propose` IPC type, operator approval UI, deploy
hook, audit log at `plugins/log.jsonl`. Related:
[6-extend-gateway-self.md](6-extend-gateway-self.md),
[0-agent-code-modification.md](0-agent-code-modification.md).
