# MCP tools

Live in your session — callable directly, no skill invocation needed.

Only explicit delivery tools reach the user. ALWAYS call `reply` for an
answer; printed prose stays in the transcript. A successful `like` counts as
a reply when a reaction conveys the answer. Narration uses `<status>` as
described in the resident manual's § Unattended.

A tool appears in your list only if your folder holds `mcp:<tool>`. The
messaging verbs come from `role:member`, which every folder gets at
create; everything else is a grant an operator or an ancestor delegated
to you. Nothing is implied by how deep your folder sits. If a tool you
expect is missing, you were not granted it — ask, don't work around it.

## Messaging

| Tool             | Description                                                               |
| ---------------- | ------------------------------------------------------------------------- |
| `send`           | Send a text message to a chat (use chatJid param to target); returns `id` |
| `reply`          | Reply to current conversation (auto-injects replyTo); returns `id`        |
| `send_file`      | Send a file from workspace to user as platform-native attachment; returns `id` |
| `send_voice`     | Synthesize text and deliver as voice (Telegram/WhatsApp PTT, Discord audio) |
| `post`           | Create a new top-level post on a feed/timeline (bluesky, discord, …)     |
| `like`           | Like/favourite/react to an existing message                               |
| `dislike`        | Endorse-negative (discord 👎, reddit downvote, telegram 👎, whatsapp 👎)   |
| `delete`         | Delete a message previously created by this agent                         |
| `edit`           | Modify a message previously sent by this agent in-place                   |
| `forward`        | Redeliver an existing message to a different chat (telegram, whatsapp)    |
| `quote`          | Republish on your feed with commentary (bluesky native)                  |
| `repost`         | Amplify a message on your feed (bluesky repost, twitter retweet)         |
| `inject_message` | Inject a message into the store for a chat (system-generated)             |

## Scheduling

| Tool            | Description                                          |
| --------------- | ---------------------------------------------------- |
| `schedule_task` | Schedule recurring or one-time agent task            |
| `pause_task`    | Pause a scheduled task                               |
| `resume_task`   | Resume a paused task                                 |
| `cancel_task`   | Cancel and delete a scheduled task                   |
| `list_tasks`    | List scheduled tasks visible to this group           |

## Groups and routing

| Tool             | Description                                                               |
| ---------------- | ------------------------------------------------------------------------- |
| `register_group` | Register new agent group                                                  |
| `refresh_groups` | Reload registered groups list                                  |
| `delegate_group` | Forward a message to a child group for processing                         |
| `escalate_group` | Escalate a task to the parent group                                       |
| `list_routes`    | List all routes visible to this group                                     |
| `set_routes`     | Replace all routes for a JID                                              |
| `add_route`      | Add a single route for a JID                                              |
| `delete_route`   | Delete a route by ID                                                      |

**Creating a group is always explicit**: `register_group(folder="<full/path>",
jid="<chat jid>")`. Nothing auto-creates one — delegating or routing to a
folder that does not exist fails and tells you to call this. `register_group`
is not in the `role:member` floor; if it is absent from your tool list, ask
the operator rather than working around it. There is no numeric cap on how
many groups you may create — your grant's scope is the whole limit.

## Engagement

| Tool                 | Description                                                                    |
| -------------------- | ------------------------------------------------------------------------------ |
| `engage`             | Mark (jid, topic) engaged so subsequent inbounds fire even without a mention. Use for autonomous turns or recovery after a failed reply. Caller must already own the conversation. |
| `disengage`          | Clear engagement for the current chat and topic before answering a completed task, or immediately on a stop-only request. Check success. A fresh mention or direct reply can start another task. |
| `set_observe_window` | Override this group's ambient observe-window caps (messages and/or chars). Pass -1 to clear. |
| `observe_group`      | Subscribe this folder to receive another folder's inbound messages as `<observed>` context. Use to let a parent monitor a child or aggregate context. Not for routing takeover. |
| `unobserve_group`    | Cancel an observe_group subscription.                                          |
| `set_group_open`     | Toggle this group's visibility to siblings. Needs `mcp:set_group_open`.                   |
| `fork_topic`         | Branch a topic from another's current state. Child gets a fresh session; the parent's session jsonl is copied so the child resumes natively. |

## Route tokens

| Tool              | Description                                                                    |
| ----------------- | ------------------------------------------------------------------------------ |
| `issue_chat_link` | Mint a route token serving the anonymous web chat widget at `/chat/<token>/`. Returns {token, url, jid} once. |
| `issue_webhook`   | Mint a route token for inbound webhook at `/hook/<token>`. Returns {token, url, jid} once. Use to register GitHub, Linear, Stripe, etc. as event sources. |
| `list_tokens`     | List route tokens (chat links + webhooks) owned by your folder. Raw tokens not returned. |
| `revoke_token`    | Revoke a route token by JID. Takes effect immediately (URL returns 404). Caller must own the token. |
| `invite_create`   | Issue an invite token granting access to a path glob. Recipient accepts via `/invite/<token>`. Needs `mcp:invite_create`. |
| `invite_list`     | List invites issued under your folder (self-filtered). Raw tokens not returned. |
| `invite_revoke`   | Revoke an invite by token. Caller must own the invite. Needs `mcp:invite_revoke`. |

## Web routing

A world's hostname is derived (`<folder>.<HOSTING_DOMAIN>` → `302 /pub/<folder>/`),
not assigned — there is no host-mapping tool. `web_routes` rows layer
redirects/auth on top.

| Tool              | Description                                                                    |
| ----------------- | ------------------------------------------------------------------------------ |
| `set_web_route`   | Upsert a web route: control whether a URL path is public, auth-gated, denied, or redirected. `access` ∈ {public, auth, deny, redirect}. |
| `del_web_route`   | Delete a web route by path. Only routes owned by this folder.                  |
| `list_web_routes` | List all web routes owned by this folder.                                      |
| `get_web_presence` | Read-only: report this folder's canonical hostname (derived `<folder>.<HOSTING_DOMAIN>` or an operator alias), the `/pub/<folder>/` path that always works, and the OAuth `/priv` base. Use to tell a user where your site is. |

## Workspace

| Tool        | Description                                                                        |
| ----------- | ---------------------------------------------------------------------------------- |
| `get_work`  | Read this group's work.md — current work, blockers, next steps. Use at session start to recover what was in-flight. |
| `set_work`  | Overwrite this group's work.md with a fresh snapshot. Use at turn end to checkpoint state. Read with get_work first if merging. |

## Slack pane (Slack only)

| Tool               | Description                                                                 |
| ------------------ | --------------------------------------------------------------------------- |
| `pane_set_prompts` | Stage suggested-prompt buttons shown at the bottom of the assistant pane after your next reply. 3-4 prompts max. Pass array of {title, message}. |
| `pane_set_title`   | Override the title shown at the top of the assistant pane. Fires after next reply. |

## Inspection and ACL

| Tool               | Description                                                                 |
| ------------------ | --------------------------------------------------------------------------- |
| `fetch_history`    | Pull authoritative platform-side history; reconstruct context after reset   |
| `get_thread`       | Read local DB rows for one (chat_jid, topic) thread                         |
| `inspect_messages` | Read local DB rows for a JID (pagination: `before`, `limit`)                |
| `inspect_routing`  | Routes + JID→folder + errored-message aggregate                             |
| `inspect_tasks`    | Scheduled tasks + recent `task_run_logs` (pass `task_id` for runs)          |
| `inspect_session`  | Current session_id + recent `session_log` entries                           |
| `inspect_identity` | Resolve a platform sender sub to its canonical identity and all claimed subs. Use to recognize a user across channels. |
| `find_messages`    | Ranked content search over recorded messages — all terms required, `"phrase"`, `-exclude`; no OR, wildcard or NEAR. Optional scope (chat_jid or folder subtree) |
| `list_acl`         | List ACL rules visible to this group.                                       |
| `add_acl`          | Grant a principal access to a folder `scope` (acl row; `scope='**'` grants operator role). Write-face of REST `POST /v1/acl`. You may grant only rows you hold WITH GRANT OPTION. |
| `remove_acl`       | Revoke a principal's access to a `scope`. Write-face of REST `DELETE /v1/acl`. Only within your granted scope. |
| `reset_session`    | Clear this group's session and start fresh                                  |

## History tools — which to use

- `inspect_messages` — whole-chat DB audit, outbound log, errored rows (not the context tool — `fetch_history` is)
- `get_thread` — one (chat_jid, topic) slice when chat fans into topics
- `fetch_history` — authoritative platform-side context (use after `reset_session` or first-contact)
- `find_messages` — find a message by what was said

Use the read-only `inspect_*` family instead of shelling out to
`sqlite3`/`journalctl`: always-on, no grant needed, scoped to your own
folder unless you are elevated `/root`.

See `/typed-jids` for chatJid format. Bare ids like `telegram:1234` are stale — use typed forms (`telegram:user/<id>`, `discord:dm/<channel>`).

## mcpc (calling MCP tools from scripts)

Ad-hoc scripts inside the container use apify's `mcpc` over
`$ARIZUKO_MCP_SOCKET` (= `/run/ipc/gated.sock`). `mcpc connect` takes a URL or
a `<config-file>:<entry>` pair — never a command — so the `socat` bridge is
declared as a stdio entry in a config file:

```bash
cat > "$HOME/gated.mcp.json" <<JSON
{"mcpServers":{"gated":{"command":"socat","args":["UNIX-CONNECT:$ARIZUKO_MCP_SOCKET","-"]}}}
JSON
mcpc connect "$HOME/gated.mcp.json:gated" @s
trap 'mcpc @s close' EXIT

mcpc @s tools-list
mcpc @s tools-call send chatJid:="telegram:user/<id>" text:="hello"
mcpc @s tools-call send_voice chatJid:="telegram:user/<id>" \
     text:="status update — all green"
mcpc @s tools-call send_file chatJid:="discord:dm/<channel>" \
     filepath:=/home/node/tmp/foo.pdf filename:="foo.pdf" caption:="here you go"
mcpc @s tools-call get_thread chat_jid:="telegram:group/<id>" topic:="<topic>"
mcpc @s tools-call fetch_history chat_jid:="telegram:group/<id>" limit:=50
```

`key:=value` is JSON-typed, `key=value` is plain string.
