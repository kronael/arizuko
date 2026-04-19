---
status: deferred
---

# History Backfill

Question reopened 2026-04-19: the agent already has `get_history` (see
`ipc/ipc.go` + `webd/mcp.go`) that reads the **local** message DB.
Missing piece is only when history predates the local DB — the
traditional answer is "backfill on startup". Alternative below may be
better.

## Option A — Startup backfill (traditional)

Each adapter persists a high-water mark (HWM); on startup it fetches
from platform API `after=HWM`, feeds gateway via normal `POST
/v1/messages`. Per-platform table kept as reference:

| Platform | Method                                  | Depth            |
| -------- | --------------------------------------- | ---------------- |
| Telegram | `getUpdates` offset resume              | 24h (API limit)  |
| Discord  | `GET /channels/{id}/messages?after=HWM` | unlimited        |
| Reddit   | listing pagination with `after`         | ~1000 items      |
| Bluesky  | cursor-based `listNotifications`        | no known cap     |
| Mastodon | `/notifications?since_id=`              | server-dependent |
| Email    | `SEARCH SINCE <date>`                   | unlimited        |
| WhatsApp | Baileys sync unreliable — exception     | —                |

Cost: per-adapter state files, dedup logic, 6 different API shapes.

## Option B — Agent pulls from backend on demand

Don't backfill. Keep the local DB as a cache of the live stream only.
Expose a new MCP tool `fetch_platform_history(jid, before, limit)`
per adapter; the agent calls it when `get_history` from local DB runs
dry. Adapter hits platform API at call time, writes results to local
DB (so next call is cached), returns them.

Tradeoffs vs A:

- **No startup loop** — instances restart fast.
- **No blind backfill** — platform API only hit when an agent actually
  asks. Cheaper per month.
- **Slower first query** per platform per chat, then cached.
- **Requires one new MCP tool per adapter** — mostly thin wrappers.
- **Storage bounded** — the local DB trims old rows on a schedule, since
  the agent can always re-pull from backend when it wants them.

Option B feels more in line with "agent is the reasoning layer; let
it decide what history to load." Option A is a batch-era solution.

## Decision

Deferred. Probably go with B but need to prove the pattern on one
adapter (Discord — has clean API, no auth-refresh gotchas) before
templating. No code change until then.

## Non-goals

- Media re-download on backfill (use existing media enricher).
- Cross-platform history merge. One JID, one platform.
