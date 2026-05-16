# MCP tools

Live in your session — callable directly, no skill invocation needed.

| Tool             | Description                                                               |
| ---------------- | ------------------------------------------------------------------------- |
| `send`           | Send a text message to a chat (use chatJid param to target)               |
| `reply`          | Reply to current conversation (auto-injects replyTo); returns `messageId` |
| `send_file`      | Send a file from workspace to user as platform-native attachment          |
| `send_voice`     | Synthesize text and deliver as voice (Telegram/WhatsApp PTT, Discord audio) |
| `post`           | Create a new top-level post on a feed/timeline (mastodon, bluesky, …)     |
| `like`           | Like/favourite/react to an existing message                               |
| `dislike`        | Endorse-negative (discord 👎, reddit downvote, telegram 👎, whatsapp 👎)   |
| `delete`         | Delete a message previously created by this agent                         |
| `edit`           | Modify a message previously sent by this agent in-place                   |
| `forward`        | Redeliver an existing message to a different chat (telegram, whatsapp)    |
| `quote`          | Republish on your feed with commentary (bluesky native; mastodon: post)   |
| `repost`         | Amplify a message on your feed (mastodon boost, bluesky repost)           |
| `inject_message` | Inject a message into the store for a chat (system-generated)             |
| `schedule_task`  | Schedule recurring or one-time agent task                                 |
| `pause_task`     | Pause a scheduled task                                                    |
| `resume_task`    | Resume a paused task                                                      |
| `cancel_task`    | Cancel and delete a scheduled task                                        |
| `list_tasks`     | List scheduled tasks visible to this group                                |
| `register_group` | Register new agent group                                                  |
| `refresh_groups` | Reload registered groups list (tier ≤ 2)                                  |
| `delegate_group` | Forward a message to a child group for processing                         |
| `escalate_group` | Escalate a task to the parent group                                       |
| `list_routes`    | List all routes visible to this group                                     |
| `set_routes`     | Replace all routes for a JID                                              |
| `add_route`      | Add a single route for a JID                                              |
| `get_routes`     | Get routes for a JID                                                      |
| `delete_route`   | Delete a route by ID                                                      |
| `fetch_history`  | Pull authoritative platform-side history; reconstruct context after reset |
| `get_thread`     | Read local DB rows for one (chat_jid, topic) thread                       |
| `inspect_messages` | Read local DB rows for a JID (pagination: `before`, `limit`)            |
| `inspect_routing`  | Routes + JID→folder + errored-message aggregate                         |
| `inspect_tasks`    | Scheduled tasks + recent `task_run_logs` (pass `task_id` for runs)      |
| `inspect_session`  | Current session_id + recent `session_log` entries                       |
| `reset_session`  | Clear this group's session and start fresh                                |
| `get_web_host`   | Get web hostname for a vhost (tier 0-1 only)                              |
| `set_web_host`   | Set web hostname mapping in vhosts.json (tier 0 only)                     |
| `get_grants`     | Get grant rules for a folder (tier 0-1 only)                              |
| `set_grants`     | Set grant rules for a folder (tier 0-1 only)                              |

History tools differ — pick by intent: `inspect_messages` for whole-chat
DB audit, `get_thread` for one (chat_jid, topic) slice when a chat fans
into topics, `fetch_history` for authoritative platform-side context
(use after `reset_session` or first-contact). See `/typed-jids` for
chatJid format; bare ids like `telegram:1234` are stale — pass typed
forms (`telegram:user/<id>`, `discord:dm/<channel>`).

## mcpc (calling MCP tools from scripts)

Ad-hoc scripts inside the container use apify's `mcpc` over
`$ARIZUKO_MCP_SOCKET` (= `/workspace/ipc/gated.sock`):

```bash
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
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
