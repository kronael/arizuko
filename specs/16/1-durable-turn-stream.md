---
status: draft
depends: [5/E-routd]
---

# Durable turn stream

A client watching a turn — a teammate following along in slink, an
external MCP caller, a dashd live view — must be able to **disconnect
and reconnect without losing the result**. arizuko's stream today
(`webd/README.md` § Live frames: the SSE hub) can't: `webd/hub.go` is an in-memory fan-out that buffers ≤16
frames per subscriber and _drops slow clients silently_. A dropped
client never learns the turn finished; a client that connects after the
agent already replied sees nothing.

## What we steal

Centaur's `GET /agent/threads/{thread_key}/events?after_event_id=&execution_id=`:
a durable per-execution event log, replayed from a cursor on reconnect,
terminated by an `execution_state` snapshot when the run already
finished and no rows remain. "Consumers tail durable events for one
execution; on disconnect, reconnect with the last seen event id."

We do **not** steal Centaur's broader durable-workflow engine
(`workflow_engine.py`, `ctx.wait_for_event`) — that overlaps
`specs/13/6-workflows.md` and pulls in a checkpoint/resume runtime
arizuko doesn't want. Only the _reconnectable stream over a durable log_.

## arizuko-shaped design

routd is the sole message appender and owns the orchestration loop
(`5/E`). It already writes `messages` rows and `turn_results`. Add a
monotonic per-turn event sequence and let the existing sinks replay
from it.

- routd writes turn events to its existing store with a monotonic
  `event_seq` (autoincrement rowid is enough) scoped to `turn_id`:
  the user frame, each assistant frame, and a terminal
  `{kind:"turn_done", outcome}` row. These are the same frames the loop
  already produces — this adds the durable row + sequence, not a new
  producer (one renderer, many sinks).
- The SSE stream gains `&after_event_seq=<n>`: webd's hub, on subscribe,
  first drains routd-stored events with `seq > n` for the (folder,
  topic)'s active turn, then attaches live. The in-memory buffer stays
  as the live path; the durable log is the replay path.
- The blocking-poll dual (`POST /chat/<token>?wait=`) returns the
  assistant frame already; add `last_event_seq` to its result and accept
  `after_event_seq` so a polling client resumes exactly. (The `get_round`
  MCP tool this bullet named is gone — one MCP vocabulary, BUGS L139.)
- A turn that finished before the client connected → webd replays the
  stored frames ending in `turn_done`; no hang.

No new daemon, no new table family — routd's event log is the one new
shape, keyed on `turn_id`, read by both sinks.

## Out of scope

- General durable-workflow checkpoint/resume (`13/6` territory).
- Cross-turn / cross-topic replay — one execution (turn) at a time,
  matching Centaur's per-`execution_id` cursor.
- Retrying the channel-side delivery — that is the pending-row sweep (`chanlib/README.md` § Delivery is claimed before the platform call).
- Retention/GC policy for the event log beyond reusing routd's existing
  message-retention.
