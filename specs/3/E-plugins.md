# Plugin Specification -- open

**Status**: not implemented.

## Overview

Main group can extend kanipi (new skills, gateway patches,
MCP sidecars) without operator shell access. Security:
**agent proposes, operator approves**.

## Trust Boundary

| Actor         | Propose | Approve | Deploy |
| ------------- | ------- | ------- | ------ |
| Agent (main)  | yes     | no      | no     |
| Operator      | no      | yes     | yes    |
| Agent (other) | no      | no      | no     |

## Plugin Types

### 1. Skills

Dir under `container/skills/<name>/` with `SKILL.md`.
Agent writes to `/workspace/group/plugins/<name>/`,
emits `plugin-propose` with `type=skill`.
See `extend-skills.md` for seeding/updates.

### 2. Gateway patches

Unified diff applied as git commit after operator review.
Agent writes `.patch` file, emits `plugin-propose` with
`type=patch`.

### 3. MCP sidecars

New MCP server binary. Agent writes config + binary,
emits `plugin-propose` with `kind=mcp`. Requires gateway
restart.

### 4. Config extensions

New `.env` keys or `config.ts` changes. Always paired
with a gateway patch.

## IPC Protocol

```json
{
  "type": "plugin-propose",
  "plugin": "name",
  "kind": "skill | patch | config",
  "description": "one-line summary",
  "path": "/workspace/group/plugins/<name>"
}
```

Gateway copies to `plugins/pending/<name>/`, notifies
operator. Operator replies "approve" or "reject".

## Approval Flow

```
Agent writes proposal -> IPC plugin-propose
  -> Gateway notifies operator
  -> Operator approves -> deploy hook runs
```

## Deploy Hook

`deploy-plugin.sh` on gateway host:

```bash
case $(cat "$DIR/kind") in
  skill) cp -r "$DIR/files/" container/skills/${NAME}/ ;;
  patch) git apply "$DIR/files/${NAME}.patch" && make lint ;;
esac
```

Skill-only deploys take effect on next spawn. Patches
need `make image && docker restart`.

## Security

- Agent cannot write outside `/workspace/group/` and
  `/workspace/ipc/`
- No auto-deploy
- Patches validated with `make lint`
- Shadow check: can't override built-in skill names
- All proposals/approvals logged to `plugins/log.jsonl`
