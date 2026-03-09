# Channel Actions — Dynamic Registration and Filtered Manifest

**Status**: open

Each social channel registers its outbound actions on connect.
The gateway filters the manifest per group so agents only see
tools they can use. Agent-runner becomes a generic proxy.

See `1/0-actions.md` for action registry and manifest protocol.
See `T-social-actions.md` for social action catalog.

## Problem

Agent-runner (`ipc-mcp-stdio.ts`) hardcodes 13 MCP tools with
duplicated schemas, descriptions, and validation. Adding 25+
social actions this way is untenable. The manifest protocol is
specced in `0-actions.md` but not implemented.

Additionally, all tools are registered for all agents. A
telegram-only group sees reddit moderation tools — context
pollution that degrades agent performance.

## Channel structure

Each social channel is a directory under `src/channels/`:

```
src/channels/mastodon/
  client.ts    — API client (auth, reconnect, rate limit)
  watcher.ts   — Channel interface (inbound events)
  actions.ts   — Action[] (outbound operations)
  index.ts     — exports Channel + Action[]
```

The client is shared between watcher and actions. One instance
per channel, created on connect.

### client.ts

Platform API wrapper. Handles auth, reconnect, rate limiting.
Stateful (holds auth tokens, websocket connections). Shared
between watcher (reads) and actions (writes).

```typescript
// Each channel exports a client class or factory
export function createClient(config: MastodonConfig): MastodonClient;
```

### watcher.ts

Implements the existing `Channel` interface (`src/types.ts`).
Polls or streams inbound events, converts to `InboundEvent`
(see `S-social-events.md`).

### actions.ts

Exports action handlers. Social actions are registered once
(not per-channel) since they switch on platform internally.
Each channel contributes its client to a shared registry:

```typescript
// src/actions/social.ts
const clients: Map<Platform, PlatformClient> = new Map();

export function registerClient(p: Platform, c: PlatformClient) {
  clients.set(p, c);
}

export const postAction: Action = {
  name: 'post',
  description: 'Create new content',
  input: z.object({
    jid: z.string(),
    content: z.string(),
    media: z.array(z.string()).optional(),
  }),
  platforms: ['reddit', 'mastodon', 'bluesky', 'twitter'],
  async handler(raw, ctx) {
    const input = PostInput.parse(raw);
    assertAuthorized(input.jid, ctx);
    const platform = platformFromJid(input.jid);
    const client = clients.get(platform);
    if (!client) throw new Error(`${platform} not connected`);
    return client.post(input.content, input.media);
  },
};
```

## MCP tool naming

All actions are generic verbs: `post`, `reply`, `ban`, `pin`.
The handler switches on `platformFromJid(jid)`:

```typescript
{
  name: 'ban',
  handler(raw, ctx) {
    const platform = platformFromJid(input.jid);
    switch (platform) {
      case 'discord': return discordClient.ban(...);
      case 'reddit': return redditClient.ban(...);
      case 'mastodon': return mastodonClient.ban(...);
      default: throw new Error(`${platform} doesn't support ban`);
    }
  }
}
```

The agent doesn't need platform knowledge — it uses the JID
it received. The gateway resolves platform and dispatches.

If an action isn't supported on the target platform, the
handler returns an error. The manifest hides actions entirely
if the agent's group has no platforms that support them.

## Dynamic client registration

Channels register their client on `connect()`, unregister on
`disconnect()`. Social actions are registered once at startup
— they dispatch to whichever clients are connected:

```typescript
// src/channels/mastodon/index.ts
export class MastodonChannel implements Channel {
  private client: MastodonClient;

  async connect() {
    this.client = createClient(this.config);
    await this.client.connect();
    registerClient('mastodon', this.client);
  }

  async disconnect() {
    unregisterClient('mastodon');
    await this.client.disconnect();
  }
}
```

Social actions (`post`, `reply`, `ban`, etc.) are registered
once in `src/actions/social.ts` and imported in `ipc.ts`.

## Filtered manifest

`getManifest()` gains a `sourceGroup` parameter. The gateway
passes group context so the manifest only includes actions
the agent can actually use.

```typescript
export function getManifest(sourceGroup: string): ManifestEntry[] {
  const tier = permissionTier(sourceGroup);
  const groupJids = jidsForGroup(sourceGroup);
  const platforms = groupJids.map(platformFromJid).filter(Boolean);

  return [...actions.values()]
    .filter((a) => a.mcp !== false)
    .filter((a) => actionAvailable(a, tier, platforms))
    .map((a) => ({
      name: a.name,
      description: a.description,
      input: z.toJSONSchema(a.input),
    }));
}
```

### Filter rules

```typescript
function actionAvailable(
  action: Action,
  tier: number,
  groupPlatforms: Platform[],
): boolean {
  if (action.minTier !== undefined && tier > action.minTier) return false;
  if (action.platforms?.length) {
    // show if ANY of the action's platforms are active
    if (!action.platforms.some((p) => groupPlatforms.includes(p))) return false;
  }
  return true;
}
```

New optional fields on `Action`:

```typescript
interface Action {
  name: string;
  description: string;
  input: z.ZodType;
  handler(input: unknown, ctx: ActionContext): Promise<unknown>;
  command?: string;
  mcp?: boolean;
  minTier?: number; // hide from agents above this tier
  platforms?: Platform[]; // show if agent has ANY of these
}
```

### What gets filtered

| Action           | minTier | platforms          | Visible to         |
| ---------------- | ------- | ------------------ | ------------------ |
| `send_message`   | —       | —                  | all agents         |
| `delegate_group` | —       | —                  | all agents         |
| `register_group` | 1       | —                  | root, world        |
| `refresh_groups` | 0       | —                  | root only          |
| `inject_message` | 1       | —                  | root, world        |
| `post`           | —       | reddit,mastodon... | agents with any    |
| `ban`            | —       | reddit,discord,... | agents with any    |
| `set_flair`      | —       | reddit             | agents with reddit |
| `timeout`        | —       | discord,twitch,yt  | agents with any    |

Actions appear if the agent has ANY platform that supports
them. If the agent calls an action on an unsupported platform,
the handler returns an error at runtime.

## Agent-runner: generic proxy

Replace hardcoded tools with manifest-driven registration.
The agent-runner becomes ~50 lines of generic proxy code:

```typescript
// Fetch manifest from gateway
const id = `${Date.now()}-${rand()}`;
writeRequest({ id, type: 'list_actions' });
const manifest = await waitForReply(id);

// Register MCP tools from manifest
for (const action of manifest.result) {
  server.tool(action.name, action.description, action.input, async (args) =>
    callAction(action.name, { ...args, chatJid }),
  );
}
```

### Special cases

Two tools need client-side logic beyond the generic proxy:

1. **`list_tasks`** — reads `current_tasks.json` locally
   (no IPC round-trip). Keep as special case in agent-runner.

2. **`schedule_task`** — client-side cron validation before
   IPC call. Move validation to gateway handler instead.
   Agent-runner becomes generic.

After migration, `ipc-mcp-stdio.ts` shrinks from ~400 lines
to ~80 lines (IPC plumbing + manifest fetch + generic loop +
list_tasks special case).

### Manifest timing

The manifest is fetched once at MCP server startup. Tools
don't change during a container's lifetime — the container
is tied to one group, and channels don't hot-swap.

If the container starts before the gateway has processed
the request (race), the agent-runner retries `list_actions`
3 times with 500ms backoff before falling back to an empty
tool set.

## Migration

### Phase 1: filtered manifest (gateway-side only)

1. Add `minTier`, `platform` to `Action` interface
2. Add `unregisterAction()` to registry
3. Change `getManifest()` → `getManifest(sourceGroup)`
4. Update `drainRequests` to pass sourceGroup to getManifest
5. Annotate existing actions with `minTier` where applicable

No agent-runner changes. Existing hardcoded tools still work.

### Phase 2: generic proxy (agent-runner)

1. Agent-runner fetches manifest on startup
2. Registers tools from manifest
3. Remove hardcoded tool definitions
4. Keep `list_tasks` as special case
5. Move `schedule_task` cron validation to gateway handler

### Phase 3: channel actions

1. Social actions in `src/actions/social.ts` (generic verbs)
2. Channels register client on `connect()`, unregister on `disconnect()`
3. Actions include `platforms` array for manifest filtering
4. Agent sees only tools supported by its group's platforms

## Open

- Rate limit errors — structured error for agent retry?
- Media upload — presigned URL or stream through gateway?
