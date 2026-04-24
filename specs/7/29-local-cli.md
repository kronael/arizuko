---
status: unshipped
---

# Local CLI

A CLI tool for local programs/scripts to send messages to groups.
Complements the web/platform channels with a local unix interface.

## Use cases

- Cron jobs reporting results to a group
- CI/CD pipelines notifying a group
- Scripts piping output to the agent
- Local dev tools interacting with groups
- Monitoring alerts routed to operator groups

## Basic shape

```bash
# send a message
arizuko send <group> "deploy completed successfully"

# pipe stdin
tail -f /var/log/app.log | arizuko send <group> --stdin

# send a file
arizuko send <group> --file ./report.pdf

# read recent messages
arizuko read <group> --last 10

# interactive session
arizuko chat <group>
```

## Open questions

1. **Auth**: how does the CLI authenticate? Options:
   - Unix socket (same host = trusted, like docker.sock)
   - API token from `.env` or `~/.config/arizuko/token`
   - Reuse existing `CHANNEL_SECRET` as local bearer token
   - No auth (localhost-only API, bind 127.0.0.1)

2. **Transport**: HTTP to gated API, or direct SQLite write, or
   unix socket to a local daemon?
   - HTTP to gated: simplest, works remotely too, but needs auth
   - Direct SQLite: fast, no daemon needed, but coupling
   - Unix socket: clean, local-only by design

3. **JID format**: what does the sender look like?
   - `local:<username>` or `cli:<hostname>`?
   - Should it go through routing or bypass directly to the group?

4. **Scope**: just send/read, or also manage (routes, groups, users)?
   The `arizuko` binary already has `group list|add|rm` and `status`.
   Should messaging be added there or be a separate tool?

5. **Streaming**: should `arizuko chat` stream responses via SSE
   (like slink) or poll? Or use the existing webd SSE endpoint?

6. **Multi-group**: can you send to multiple groups at once?
   `arizuko send alice/ bob/ "message"` or topic-based fan-out?
