# Gateway Actions

Atomic operations. Action registry is the single source of
truth — MCP tools, commands, and IPC dispatch all reference it.

## Model

```
  /new command ──▶ Action Registry ◀── MCP tool (via IPC)
                   (gateway-side)
                   action(input) → output
```

- **Commands** — call actions directly (in-process)
- **MCP tools** — agent calls MCP tool in container, which
  writes IPC request, gateway dispatches, writes reply
- **IPC dispatch** — request-response over files

## Action interface

```typescript
interface Action {
  name: string;
  description: string;
  input: ZodSchema;
  handler(input: unknown, ctx: ActionContext): Promise<unknown>;
  command?: string; // registers as /command
  mcp?: boolean; // auto-exposed as MCP tool (default: true)
}

interface ActionContext {
  sourceGroup: string;
  isRoot: boolean;
  sendMessage(jid: string, text: string): Promise<void>;
  sendDocument(jid: string, path: string, name?: string): Promise<void>;
  registeredGroups(): Record<string, RegisteredGroup>;
  registerGroup(jid: string, group: RegisteredGroup): void;
  syncGroupMetadata(force: boolean): Promise<void>;
  getAvailableGroups(): AvailableGroup[];
  writeGroupsSnapshot(
    folder: string,
    groups: AvailableGroup[],
    jids: Set<string>,
  ): void;
  clearSession(folder: string): void;
  delegateToChild(
    childFolder: string,
    prompt: string,
    originJid: string,
    depth: number,
  ): Promise<void>;
}
```

## IPC: request-response over files

Current IPC is fire-and-forget (agent writes file, gateway
picks it up, no response). New model: request-response.

### Flow

```
Agent MCP tool call
  → ipc-mcp-stdio writes /workspace/ipc/requests/<id>.json
  → polls /workspace/ipc/replies/<id>.json
  → reads reply, returns to agent

Gateway
  → fs.watch requests/
  → looks up action by type
  → validates input (Zod)
  → calls handler
  → writes /workspace/ipc/replies/<id>.json
```

### Request format

```json
{
  "id": "1709693200000-abc123",
  "type": "send_message",
  "chatJid": "tg:-100123456",
  "text": "hello"
}
```

### Reply format

```json
{
  "id": "1709693200000-abc123",
  "ok": true,
  "result": { ... }
}
```

```json
{
  "id": "1709693200000-abc123",
  "ok": false,
  "error": "unauthorized"
}
```

### Tool discovery

Agent requests available actions at startup:

```json
{ "id": "...", "type": "list_actions" }
```

Gateway replies with action manifest:

```json
{
  "id": "...",
  "ok": true,
  "result": [
    {
      "name": "send_message",
      "description": "Send text to a channel",
      "input": { ... }
    }
  ]
}
```

Agent-runner reads the manifest and auto-registers MCP tools.
Each tool writes a request file and waits for the reply.

### Agent-side MCP stub (generic)

`ipc-mcp-stdio.ts` becomes a generic proxy — no per-action
code. One loop generates all tools from the manifest:

```typescript
for (const action of manifest) {
  server.tool(action.name, action.description, action.input, async (args) => {
    const id = `${Date.now()}-${rand()}`;
    writeIpcFile(REQUESTS_DIR, { id, type: action.name, ...args });
    const reply = await waitForReply(id, REPLIES_DIR);
    if (!reply.ok)
      return { content: [{ type: 'text', text: reply.error }], isError: true };
    return { content: [{ type: 'text', text: JSON.stringify(reply.result) }] };
  });
}
```

### Timeout

Agent-side polls with 100ms interval, 30s timeout. If no
reply, return error to agent. Gateway cleans stale replies
on next drain.

### Migration from fire-and-forget

1. Gateway adds request watcher + reply writer alongside
   existing message/task watchers
2. Agent-runner checks for manifest, falls back to hardcoded
   tools if missing (backwards compat during rollout)
3. Remove hardcoded tools once stable

## Current actions

### Messaging

| Action         | Cmd | MCP | Input                              |
| -------------- | --- | --- | ---------------------------------- |
| `send_message` | --  | yes | `{ chatJid, text, sender? }`       |
| `send_file`    | --  | yes | `{ chatJid, filepath, filename? }` |

### Session

| Action          | Cmd       | MCP | Input |
| --------------- | --------- | --- | ----- |
| `reset_session` | `/new`    | yes | --    |
| `ping`          | `/ping`   | --  | --    |
| `chatid`        | `/chatid` | --  | --    |

### Tasks

| Action          | MCP | Input                                                                 |
| --------------- | --- | --------------------------------------------------------------------- |
| `schedule_task` | yes | `{ targetJid, prompt, schedule_type, schedule_value, context_mode? }` |
| `pause_task`    | yes | `{ taskId }`                                                          |
| `resume_task`   | yes | `{ taskId }`                                                          |
| `cancel_task`   | yes | `{ taskId }`                                                          |

`schedule_type`: `'cron' | 'interval' | 'once'`.
`context_mode`: `'group' | 'isolated'` (default `'isolated'`).
`list_tasks` is not yet implemented.

### Groups

| Action              | MCP | Input                                                                                        |
| ------------------- | --- | -------------------------------------------------------------------------------------------- |
| `refresh_groups`    | yes | --                                                                                           |
| `register_group`    | yes | `{ jid, name, folder, trigger, requiresTrigger?, containerConfig?, parent?, routingRules? }` |
| `delegate_group`    | yes | `{ group, prompt, chatJid, depth? }`                                                         |
| `set_routing_rules` | yes | `{ folder, rules }`                                                                          |

`register_group` requires root. `delegate_group` is authorized
by `isAuthorizedRoutingTarget(sourceGroup, group)`; `depth` is
injected by the gateway on recursive delegation (max 3).

`rules` is an array of `RoutingRule`:

```typescript
type RoutingRule =
  | { type: 'command'; trigger: string; target: string }
  | { type: 'pattern'; pattern: string; target: string } // regex
  | { type: 'keyword'; keyword: string; target: string }
  | { type: 'sender'; pattern: string; target: string } // regex on sender JID
  | { type: 'default'; target: string };
```

`target` is a group folder name. Evaluation order:
command → pattern → keyword → sender → default.

### Sidecars (specced, not yet implemented)

| Action              | MCP | Input                                            |
| ------------------- | --- | ------------------------------------------------ |
| `configure_sidecar` | yes | `{ name, image, env?, allowedTools?, network? }` |
| `request_sidecar`   | yes | `{ name, image, env?, allowedTools? }`           |
| `stop_sidecar`      | yes | `{ name }`                                       |
| `list_sidecars`     | yes | --                                               |

`configure_sidecar` persists sidecar config to `registered_groups.container_config`;
takes effect on next agent spawn. `request_sidecar` starts a sidecar immediately
for the current session. Both require root.

See [sidecars.md](sidecars.md) for lifecycle and socket transport details.

### Future

| Action           | Cmd     | MCP | Input                       |
| ---------------- | ------- | --- | --------------------------- |
| `edit_message`   | --      | yes | `{ chatJid, msgId, text }`  |
| `react`          | --      | yes | `{ chatJid, msgId, emoji }` |
| `delete_message` | --      | yes | `{ chatJid, msgId }`        |
| `pin_message`    | --      | yes | `{ chatJid, msgId }`        |
| `help`           | `/help` | --  | --                          |

## Command dispatch

Commands call actions directly — no IPC, no bus. Thin
wrappers that parse user input and call the action.

## Authorization

- Root group can target any JID
- Non-root can only target their own
- Enforced in action handlers

## Implementation

- `src/action-registry.ts` — registry, validation
- `src/actions/` — one file per action or by domain
- `src/ipc.ts` — request watcher, reply writer
- `src/commands/` — thin wrappers calling actions
- `container/agent-runner/src/ipc-mcp-stdio.ts` — generic
  proxy from manifest

## Open

- Action middleware (logging, rate limiting) — v2 via
  MCP proxy hooks (see `v2/ipc-mcp-proxy.md`)
