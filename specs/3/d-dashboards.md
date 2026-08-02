---
status: superseded-in-part
superseded-in-part: [7/1-cockpit-index]
---

# Dashboards — the original tile model

> **Historical input.** This spec designed six read-only dashboards
> (Portal, Status, Tasks, Activity, Groups, Memory). `dashd` has since
> grown well past it — ~20 pages including mutations (`POST`/`PUT`/
> `DELETE` on routes, tasks, groups, grants, memory, secrets,
> connections), which this spec explicitly put out of scope. The live
> surface is [`../7/1-cockpit-index.md`](../7/1-cockpit-index.md); read
> `dashd/main.go` for the route table. Kept for the decisions below,
> which still hold.

## Decisions that held

**Separate daemon, own HTTP port.** An operator view that can hang or
crash the routing plane is not an operator view. The original design
also opened the DB `?mode=ro`; that half did not survive (see below).

**Server-rendered Go templates + HTMX, no frontend build.** Every page
has an `x/` sibling endpoint returning the inner fragment only
(`/dash/tasks/x/list`, `/dash/activity/x/recent`). Polling beat
WebSocket/SSE: the data is seconds-fresh at best, and a second transport
would have to be kept alive for no perceptible gain.

**URL convention** — `/dash/<name>/` page, `/dash/<name>/x/<fragment>`
partial, `/dash/<name>/api/<path>` JSON.

**Health is a three-state dot per tile**, computed per dashboard rather
than globally: ok / warn / error, where warn means "degraded but
serving" (failures > 0, container ceiling reached) and error means "a
dependency is down" (channel disconnected, circuit breaker tripped). A
single global health number hides which subsystem moved.

**Text truncation is privacy, not layout.** The activity view caps
message previews at 80 chars so an operator can trace routing without
reading conversations.

**Memory-browser path safety** — allow-list, reject `..`, absolute
paths, and symlink escapes from the group root, via
`groupfolder.Resolve()`. Detail:
[`../4/Q-dash-memory.md`](../4/Q-dash-memory.md).

## What the spec got wrong

"Not in scope: mutations" did not survive contact with operators — kill,
retry, pause, edit and create all shipped, and with them the `?mode=ro`
handle. `dashd` now holds a read/write handle on `routd.db`
(`dash.adminDB()`) and writes owned tables directly, which is the
settled split write-discipline for FS-mounted daemons: dashd, onbod and
the CLI write directly; non-mounted daemons (slakd, timed) go through
the owner's HTTP API with a service token.
