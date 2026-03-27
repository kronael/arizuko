---
status: draft
---

# CLI Chat Mode Spec

`arizuko chat` — run an agent group interactively from the terminal,
bypassing the full gateway/adapter stack.

---

## 1. Dockbox Summary

Dockbox (`/home/onvos/app/tools/dockbox/`) is a ~180-line bash script that
wraps `docker run -it --rm` to give Claude Code a clean, isolated container
for working on host projects.

**What it does:**

- Mounts the project dir(s) at exact host paths (so absolute paths work
  inside and outside the container identically)
- Mounts `~/.claude` (rw), `.gitconfig` (ro), gpg agent socket, shell
  history, `/etc/localtime`
- Injects `settings.local.json` with `{"defaultMode":"bypassPermissions"}`
  to skip all permission prompts — overmounts the file so the container
  user sees it but can't persist a different value
- Passes `TERM` and optionally `SSH_AUTH_SOCK` into the container
- Reads extra docker flags from `~/.dockboxrc` and `.dockboxrc` in the
  project dir; overmounts the project `.dockboxrc` with `/dev/null` so the
  container can't modify it
- Container name derived from project dir name (`dockbox-<project>`)
- Management subcommands: `ls`, `rm [pattern]`, `prune [hours]`
- `exec docker run -it` — fully interactive PTY, no wrapper process

**Design decisions worth borrowing:**

1. **Credentials via mount, not env**: `~/.claude` mounted rw gives the
   container full credential access without injecting secrets as env vars.
   Arizuko already does the opposite (secrets injected as JSON on stdin)
   which is actually more secure for the multitenant case — worth keeping.

2. **Exact-path mounts**: project dirs mounted at their real host paths.
   Arizuko mounts the group dir at `/home/node` (the container HOME).
   For CLI chat the same convention works; the "project" IS the group dir.

3. **Session naming**: `dockbox-<project>` from the working dir basename.
   Simple, predictable, unique enough for interactive use.

4. **No daemon, no DB**: dockbox is purely `exec docker run`. No polling,
   no persisted state beyond what's in the mounted dirs. This is the model
   to replicate for `arizuko chat`.

5. **`-it` flag**: full PTY — `claude` runs with real terminal support,
   colors, readline, etc. Critical for interactive chat.

---

## 2. Current Arizuko Container Launch

### What `container.Run()` does

1. Resolves group path via `groupfolder.Resolver`
2. Calls `BuildMounts()` — assembles volume mount list
3. `seedSettings()` — writes `settings.json` to session `.claude/` dir
4. `seedSkills()` — copies `container/skills/` on first run; seeds
   `.claude.json` if missing (SDK silently returns 0 messages without it)
5. Starts sidecars (if configured)
6. Starts MCP unix socket server via `ipc.ServeMCP(sockPath, ...)`
7. `docker run -i --rm` (no `-t`, not interactive — stdin is a pipe)
8. Marshals `Input` struct to JSON, writes to container stdin, closes
9. Reads stdout, parses output between `---ARIZUKO_OUTPUT_START---` /
   `---ARIZUKO_OUTPUT_END---` markers
10. Container exits when query loop ends (no more IPC messages or `_close`
    sentinel dropped)
11. Stops MCP server and sidecars

### Volume mounts (all required)

| Mount          | Host path                     | Container path           | Notes                              |
| -------------- | ----------------------------- | ------------------------ | ---------------------------------- |
| Group home     | `groups/<folder>/`            | `/home/node`             | rw — agent's $HOME                 |
| Session state  | `groups/<folder>/.claude/`    | `/home/node/.claude`     | rw — settings.json, skills, memory |
| App source     | `cfg.HostAppDir`              | `/workspace/self`        | ro — agent can read own code       |
| Share          | `groups/<world>/share/`       | `/workspace/share`       | rw for root, ro for non-root       |
| IPC dir        | `data/ipc/<folder>/`          | `/workspace/ipc`         | rw — MCP socket + IPC file drop    |
| Groups dir     | `data/groups/`                | `/workspace/data/groups` | root group only                    |
| Web dir        | `cfg.WebDir`                  | `/workspace/web`         | if exists                          |
| Dev runner src | `container/agent-runner/src/` | `/app/src`               | only when `ARIZUKO_DEV=1`          |

### Env vars injected into container

Set via `seedSettings()` writing `settings.json` `env` block — the SDK
injects these into the container environment at startup:

- `WEB_HOST` — web proxy URL
- `ARIZUKO_ASSISTANT_NAME` — agent's name
- `ARIZUKO_IS_ROOT` — "1" if root group, "" otherwise
- `ARIZUKO_DELEGATE_DEPTH` — delegation depth counter
- `SLINK_TOKEN` — slink token if present
- `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` — load extra CLAUDE.md
- `CLAUDE_CODE_DISABLE_AUTO_MEMORY=0`

Direct docker env args:

- `TZ` — timezone from config
- `HOME=/home/node` — if host UID != 1000

### MCP server (IPC)

Host starts a unix socket server at `data/ipc/<folder>/router.sock` before
`docker run`. The socket is bridged into the container via socat:

```
container: socat STDIO UNIX-CONNECT:/workspace/ipc/router.sock
```

The `arizuko` MCP server config is written into `settings.json` so the SDK
picks it up automatically. This gives the agent tools: `send_message`,
`send_file`, `reset_session`, `schedule_task`, `get_routes`, etc.

In interactive chat mode, most of these tools have no useful target (no
channel adapter is listening). The `send_message` and `send_file` tools
would need stubs or the MCP server needs to be replaced with a minimal
no-op version.

### Input JSON (written to container stdin)

```typescript
interface ContainerInput {
  prompt: string; // initial message
  sessionId?: string; // resume existing session
  groupFolder: string; // e.g. "main"
  chatJid: string; // used for context only inside container
  isScheduledTask?: bool;
  assistantName?: string;
  secrets?: Record<string, string>; // ANTHROPIC_API_KEY etc.
  soul?: string; // SOUL.md content
  systemMd?: string; // SYSTEM.md content
}
```

### Output protocol

Container writes to stdout between marker lines:

```
---ARIZUKO_OUTPUT_START---
{"status":"success","result":"...", "newSessionId":"uuid"}
---ARIZUKO_OUTPUT_END---
```

Multiple outputs during a session (streaming mode). Container stays alive
in a poll loop: after each query completes it checks
`/workspace/ipc/input/*.json` for follow-up messages. It exits when the
input dir is empty or `_close` sentinel is written.

---

## 3. CLI Chat Design

### Command interface

```
arizuko chat [group] [flags]
  group          group folder name (default: "main")
  --new          force new session (ignore stored session ID)
  --instance     instance name (default: basename of $PWD or "local")
  --data         data dir override (default: /srv/data/arizuko_<instance>)
  --no-ipc       skip MCP server (no gateway tools)
```

Or as a standalone binary `ant`:

```
ant [group] [flags]
```

`ant` is just `arizuko chat` under a short name.

### What gets stripped vs kept

**Stripped (not needed for CLI chat):**

- SQLite store — no DB, no persistence of messages between turns
- Gateway polling loop — no `messageLoop`, no `pollOnce`
- Channel adapters — no HTTP API, no inbound adapter registration
- `queue.GroupQueue` — no concurrency management needed (single user)
- `router.FormatMessages` — no XML message formatting; prompt goes direct
- Output callback routing — no `ch.Send()` to channel adapters; output
  goes to stdout directly
- Session cursor tracking in DB
- `impulseGate` batching
- Prefix dispatch / routing rules
- `.diary` annotation in prompt prefix
- `WriteTasksSnapshot`, `WriteGroupsSnapshot` — no queue watching snapshot

**Kept:**

- `container.Run()` — the core docker launch logic, reused mostly unchanged
- `container.BuildMounts()` — all mounts are still needed
- `container.seedSettings()` — still needed; settings.json controls SDK env
- `container.seedSkills()` — still needed; agent needs its skills
- `ipc.ServeMCP()` — still needed (see IPC section below)
- `container.SanitizeFolder()`, `groupfolder.Resolver`
- Auth/grants derivation — still want grant-controlled tools
- `diary.Read()` — can include diary context in initial prompt (optional)

### How output streams

In gateway mode, `container.Run()` receives an `OnOutput` callback and
calls it each time a result arrives. The callback routes text through
`router.FormatOutbound()` and then to `ch.Send()`.

In chat mode, the `OnOutput` callback simply writes to stdout:

```go
onOutput := func(result, status string) {
    if result == "" {
        return
    }
    clean := router.FormatOutbound(result)
    if clean != "" {
        fmt.Println(clean)
    }
}
```

No DB writes, no channel send. `router.FormatOutbound` strips `<internal>`
tags and trims whitespace — still useful for clean output.

### How follow-up messages work

The container's IPC poll loop already handles multi-turn: after the first
query finishes it polls `/workspace/ipc/input/*.json` for the next message.

The CLI host simply reads stdin and writes follow-up messages as IPC files:

```go
// after container starts, in a goroutine:
scanner := bufio.NewScanner(os.Stdin)
for scanner.Scan() {
    text := scanner.Text()
    if text == "" {
        continue
    }
    data, _ := json.Marshal(map[string]string{"type": "message", "text": text})
    os.WriteFile(filepath.Join(ipcInputDir, timestamp()+".json"), data, 0o644)
    // optionally send SIGUSR1 to container process for immediate wakeup
}
// on EOF, write _close sentinel
os.WriteFile(filepath.Join(ipcInputDir, "_close"), nil, 0o644)
```

The container polls every 500ms by default, so there's a small latency.
If SIGUSR1 is sent to the node process inside the container, it wakes
immediately. Getting the container PID for SIGUSR1 requires `docker top`
or reading `/proc` — optional optimization.

The host stdin read loop and the container stdout parsing loop run in
parallel goroutines. This is the same pattern as `container.Run()` today.

### Session selection

On startup:

1. If `--new` flag: ignore any stored session, start fresh
2. Otherwise: look for a `sessions.json` (or similar lightweight file)
   in `groups/<folder>/` that stores the last session ID
3. If found, pass `sessionId` in the input JSON → SDK resumes conversation
4. If not found, start fresh; save the `newSessionId` from first output

The gateway uses SQLite (`store.GetSession`, `store.SetSession`). For CLI
chat mode, a plain JSON file is simpler and avoids the DB dependency:

```
groups/<folder>/cli-session.json
{"sessionId": "uuid", "updatedAt": "..."}
```

This file is written by the CLI after the first `newSessionId` arrives
in output, and read at next invocation for resume.

### Group and session directory selection

Default group: `"main"` (same as `arizuko create` default).

If no data dir flag, the CLI infers it from:

1. `$ARIZUKO_DATA_DIR` env var
2. `/srv/data/arizuko_<instance>` where instance defaults to `"local"`

The group dir must exist before launching. The CLI should call `os.MkdirAll`
on it (including the `.claude/` subdir), same as `container.Run()` does today.

### IPC / MCP in chat mode

The unix socket MCP server should still start. This gives the agent:

- `reset_session` — useful interactively
- `list_tasks`, route tools — still functional if the backing DB is present

The question is which `GatedFns` to wire up. Options:

**Option A — no-op stubs**: provide `GatedFns` with stub functions that
return errors for network-dependent tools (`send_message`, `send_file`,
`inject_message`). The agent sees those tools in its manifest but they
fail gracefully. Grants are still enforced.

**Option B — no MCP server**: pass `--no-ipc` flag to skip the unix socket.
The agent still works but loses all MCP tools including `reset_session`.
Also means `settings.json` must not include the `arizuko` MCP server config.

**Option C — minimal MCP** (recommended): start MCP server with no-op
`GatedFns` for channel tools. Wire `ClearSession` to update `groups/<folder>/cli-session.json`. Skip
`StoreFns` entirely (pass empty struct, tools that
need it just fail). The agent can still `reset_session`.

For the initial implementation, **Option A** is simplest — reuse the full
`ipc.ServeMCP()` call with stubs for the channel-side functions.
The agent will see `send_message` in its manifest but the grant rules
for a CLI session should have it denied anyway.

**Grant rules for CLI mode**: derive rules as for a root/tier-0 group, or
simply use `["*"]` for the local user. This is the trusted-user model,
same as dockbox.

### How `container.Run()` is reused

`container.Run()` takes an `Input` struct and `*core.Config`. Both are
filled from CLI flags + data dir config.

Changes needed to `Input` for chat mode:

- `ChatJID`: use `"cli:<folder>"` as a synthetic JID — never matches any
  real channel, but the code paths that use it for routing are not invoked
  in this mode
- `OnOutput`: the stdout-printing callback described above
- `GatedFns` / `StoreFns`: stubs (see IPC section)
- `Prompt`: the first line of stdin (or a placeholder if not piped)

No changes to `container.Run()` itself are needed for the first cut.

The only gap is that `container.Run()` uses `docker run -i` (no `-t`). For
interactive chat we need PTY support so the container's node process gets
`isatty=true`. **This matters** because if the agent-runner detects no TTY
it may behave differently; more practically, color output from claude-code
CLI requires a TTY.

However, the agent-runner (`src/index.ts`) does not use a TTY at all — it
reads JSON from stdin and writes to stdout. The claude-code SDK inside the
container is invoked programmatically, not as a subprocess. So `-t` is not
needed for correctness.

What IS needed for a nice CLI UX is streaming output as the agent produces
it, plus a readline loop on the host side. The current `container.Run()`
already streams output as markers arrive. The host readline loop is the
new piece.

### Container name

Use `arizuko-chat-<folder>-<timestamp_ms>` to keep it distinct from normal
agent containers.

---

## 4. Relationship to Dockbox

Dockbox and the arizuko agent container are solving different problems:

|             | Dockbox                         | Arizuko agent                              |
| ----------- | ------------------------------- | ------------------------------------------ |
| **Runtime** | Claude Code CLI (interactive)   | Claude Agent SDK (programmatic)            |
| **I/O**     | PTY stdin/stdout                | JSON stdin, structured stdout markers      |
| **Session** | Claude Code's own session files | SDK session IDs passed in JSON             |
| **MCP**     | Host ~/.claude/settings.json    | settings.json written by host before spawn |
| **Image**   | `dockbox` (claude + tools)      | `arizuko-agent` (node + agent-runner)      |
| **Mode**    | One-shot PTY                    | Multi-turn via IPC file drop               |

**Should arizuko embed/use dockbox?**

No. The images are incompatible: dockbox runs `claude` CLI directly;
arizuko-agent runs `node /app/index.js` which uses the Agent SDK. The
multi-turn IPC mechanism, settings seeding, and session resumption are
all arizuko-specific and not replicated in dockbox.

**What to borrow from dockbox:**

1. **Settings overmount pattern**: dockbox injects `settings.local.json`
   with `{"defaultMode":"bypassPermissions"}` to prevent the container
   from being blocked by permission dialogs. Arizuko already achieves
   this via `permissionMode: 'bypassPermissions'` in the SDK call. No
   change needed.

2. **RC file pattern**: dockbox reads `~/.dockboxrc` for extra docker flags.
   A similar `~/.arizuko-chatrc` could allow power users to add mounts or
   env vars without modifying source. Optional, low priority.

3. **Container lifecycle management** (`ls`, `rm`, `prune`): worth adding
   `arizuko chat ls` and `arizuko chat rm` subcommands to clean up
   stopped chat containers. Simple `docker container ls` filter on
   `name=^arizuko-chat-`.

**Verdict**: Reimplement, don't import. The core docker launch is 5 lines
(`docker run -i --rm` + mounts). Dockbox is bash; arizuko is Go. The
existing `container.Run()` and `buildArgs()` already do what's needed.

---

## 5. Implementation Sketch

```
cmd/arizuko/main.go
  case "chat": cmdChat(os.Args[2:])

chat/chat.go
  func Run(cfg *core.Config, folders *groupfolder.Resolver, folder string, newSession bool)
    - seedSettings + seedSkills (reuse container pkg)
    - start MCP server (stub GatedFns)
    - load or clear session ID
    - build Input{Prompt: readFirstPrompt(), ChatJID: "cli:"+folder, ...}
    - start stdin reader goroutine (writes IPC files)
    - call container.Run(cfg, folders, input)  // reuse unchanged
    - save newSessionId to cli-session.json
    - handle SIGINT: write _close sentinel before exit
```

The stdin reader goroutine and the container.Run() call are the only net-new
code. Everything else is plumbing existing packages.

---

## 6. Open Questions

**Q1: First prompt UX**
Does `arizuko chat` wait for stdin before launching the container, or launch
immediately with an empty/placeholder prompt? Launching first and then piping
user input via IPC avoids a startup delay but means the agent gets no initial
context. Waiting for first line means a blank terminal until the user types.

Likely answer: print a prompt like `> ` and wait for the first message, same
as a normal REPL. Container launches with that as the initial prompt JSON.

**Q2: Output display during tool use**
The agent-runner only emits output at the END of each query (the `result`
message). During tool use (which can take minutes) there is no visible
progress. The heartbeat JSON (`{"heartbeat":true}`) arrives every 30s —
the CLI could use this to show a spinner or elapsed timer.

**Q3: Multiline input**
How does the user enter multiline prompts? Options:

- `\n` escape in single-line input
- Blank line terminates the message, double-blank line sends
- Explicit send key (Ctrl+D or similar)

**Q4: `ant` binary placement**
Should `ant` be a separate `cmd/ant/main.go` that imports a chat package,
or a symlink to `arizuko` that detects `os.Args[0] == "ant"`? The latter
is simpler but slightly surprising. Separate binary is cleaner.

**Q5: MCP tools that write to channel**
The agent's `send_message` / `send_file` tools will fail silently (stub
returns error). Should the CLI hook those to print to stdout instead? This
would mean a clean interactive experience where file deliveries show up in
terminal. Worth doing but not required for MVP.

**Q6: Session resume UX**
When resuming, should the CLI print a brief indicator ("resuming session
from [date]")? The SDK resumes silently — the agent sees the prior context
but the user may not know they're in a resumed session. A one-liner from the
`cli-session.json` `updatedAt` field would be helpful.

**Q7: No-DB dependency**
`container.Run()` today takes `*core.Config` which is loaded from `.env`.
For standalone `ant` usage (no instance created), where does config come
from? Options:

- Require `arizuko create <name>` first (simplest)
- Auto-create a minimal config at `~/.local/share/arizuko/` on first run
- Accept `--image`, `--api-key` etc. as direct flags

The simplest path: `ant` requires either `$ARIZUKO_DATA_DIR` or
`--data <dir>` pointing to a pre-created instance dir. No auto-create.

**Q8: Docker socket**
`container.Run()` assumes `docker` is on `$PATH`. For `ant` on developer
machines this is fine. Document as a requirement, no change needed.

**Q9: `seedSettings` output style**
`seedSettings()` writes `outputStyle` from `in.Channel`. For CLI chat
there's no channel. The output style controls Telegram/Discord markdown
formatting. For terminal output, plain text is correct. The `Channel`
field should be set to `""` (no channel), which leaves `outputStyle`
unset in settings.json, causing the agent to use default formatting.
This is already the correct behavior when `Channel = ""`.
