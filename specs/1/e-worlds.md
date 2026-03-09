# Worlds

JIDs are URIs. Group folders can nest. Router maps JIDs
to groups via exact match or glob.

## JID format

```
channel:id
channel:id:subid
```

`:` separator (URI scheme). First segment = full channel
name. Depth varies by platform.

| Channel  | Flat                | Hierarchical                |
| -------- | ------------------- | --------------------------- |
| telegram | `telegram:chatid`   | `telegram:chatid:threadid`  |
| discord  | `discord:channelid` | `discord:guildid:channelid` |
| whatsapp | `whatsapp:groupjid` | (flat)                      |
| email    | `email:threadid`    | `email:domain:threadid`     |
| web      | `web:slinkid`       | (flat)                      |

Flat JIDs remain valid. `:` is standard URI scheme
separator. Channel IDs never contain `:`.

Each channel constructs its own JIDs. Gateway only matches
strings against registered groups.

---

## Phase 1 (implement now)

### 1a. Prefix expansion (`tg:` -> `telegram:`, wrap WhatsApp)

One-time DB migration:

```sql
UPDATE chats SET jid = REPLACE(jid, 'tg:', 'telegram:')
  WHERE jid LIKE 'tg:%';
UPDATE chats SET jid = 'whatsapp:' || jid
  WHERE jid LIKE '%@g.us' OR jid LIKE '%@s.whatsapp.net';
-- same for messages, registered_groups, scheduled_tasks
```

Code: `startsWith('tg:')` -> `startsWith('telegram:')` etc.
WhatsApp: wrap native JIDs with `whatsapp:` prefix.

### 1b. isRoot replaces isMain

```typescript
// config.ts
function isRoot(folder: string): boolean {
  return !folder.includes('/');
}
```

`main` has no `/` -> root. Any single-segment folder is
root. Nested folders (`acme/ops`) are non-root. `isMain`
removed from `ContainerInput`.

#### Changes (gateway)

| File                | Change                                                |
| ------------------- | ----------------------------------------------------- |
| config.ts           | `isRoot()` replaces MAIN_GROUP_FOLDER                 |
| container-runner.ts | Remove isMain param, compute inside; NANOCLAW_IS_ROOT |
| index.ts            | `isRoot(group.folder)` everywhere                     |
| ipc.ts              | `isRoot(src)` for auth checks                         |
| task-scheduler.ts   | `isRoot(folder)` check                                |
| mount-security.ts   | Rename param to `root`                                |

#### Changes (agent runner)

| File             | Change                    |
| ---------------- | ------------------------- |
| index.ts         | Derive from groupFolder   |
| ipc-mcp-stdio.ts | `NANOCLAW_IS_ROOT` checks |

#### Changes (tests + docs)

All test files: remove isMain from inputs, update mocks.
Docs: ARCHITECTURE.md, SKILL.md, CHANGELOG.md.

### 1c. global/ -> share/

`groups/global/` -> `groups/<world>/share/`. For flat
folders, world = folder: `groups/main/share/`.

**Root** (rw): `/workspace/share/ <- groups/main/share/`
**Child** (ro): `/workspace/share/ <- groups/acme/share/`

World = `folder.split('/')[0]`.

Instance migration:

```bash
mkdir -p groups/main/share
mv groups/global/* groups/main/share/ 2>/dev/null
```

### 1d. Folder validation

```typescript
const SEGMENT = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
const RESERVED = new Set(['share']);

function isValidGroupFolder(folder: string): boolean {
  if (!folder || folder !== folder.trim()) return false;
  if (folder.includes('..') || folder.includes('\\')) return false;
  const segs = folder.split('/');
  return (
    segs.every((s) => SEGMENT.test(s)) &&
    !segs.some((s) => RESERVED.has(s.toLowerCase()))
  );
}
```

### 1e. Router with glob matching

```typescript
function findGroup(
  jid: string,
  groups: Record<string, RegisteredGroup>,
): RegisteredGroup | undefined {
  if (groups[jid]) return groups[jid]; // O(1)
  let best: RegisteredGroup | undefined;
  let bestLen = -1;
  for (const [pat, g] of Object.entries(groups)) {
    if (!pat.includes('*')) continue;
    if (minimatch(jid, pat) && pat.length > bestLen) {
      best = g;
      bestLen = pat.length;
    }
  }
  return best;
}
```

### Implementation order

1. DB migration (`tg:` → `telegram:`, wrap WhatsApp)
2. Channel ownsJid(), JID construction
3. config.ts: `isRoot()`
4. group-folder.ts: allow `/`, reserve `share`
5. container-runner.ts: remove isMain, share/ mount
6. index.ts, ipc.ts, task-scheduler.ts: isRoot
7. mount-security.ts: rename param
8. Agent runner: NANOCLAW_IS_ROOT, share/ paths
9. New router with `findGroup()`
10. Wire into message loop
11. CLI: glob JIDs, hierarchical folders
12. Tests, docs, CHANGELOG, migration skill
13. Rebuild images, run DB migration

---

## Phase 2: Hierarchical JIDs (future)

Extend flat URIs with `:` sub-segments:

- Discord: `discord:guildId:channelId`, threads
  `discord:guildId:channelId:threadId`,
  DMs as `discord:dm:channelId`
- Email: `email:domain:threadid`
- Telegram: `telegram:chatid:threadid` (forum topics)

Migrate existing flat JIDs. Glob matching (`discord:*`)
already supports prefix patterns.

## Phase 3: World tree mount (future)

`/workspace/world/ <- groups/acme/ ro` for non-root.
Deferred until concrete use case.

## Phase 4: IPC auth generalization (future)

Tree-scoped: `acme/ops` can target `acme/ops/alerts`
but not `acme/dev`. Deferred until nested groups exist.

---

## Open

- WhatsApp native JIDs (`@g.us`) wrapped as `whatsapp:id@g.us`
  — the `@g.us` suffix is ugly but harmless inside the URI
- Email threading uses table, not JID hierarchy
- Cross-world: use additionalMounts
- Max depth: filesystem path length limit
- Auto-create intermediate dirs: mkdir -p semantics
