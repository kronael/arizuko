# Scheduler Microservice

## Role

A message producer. Polls `scheduled_tasks` for due items,
writes messages to the `messages` table. That's it.

The scheduler does NOT:

- Run containers or agents
- Know about MCP, docker, sessions, or volumes
- Handle authorization beyond task ownership
- Deliver messages to channels
- Manage group configuration

## Current State (coupled)

The scheduler currently depends on gateway callbacks:

- `RunAgentFn` — runs a container directly
- `EnqueueTaskFn` — pushes into the gateway's queue
- `InjectMessageFn` — writes a fake user message
- `SendMessageFn` — delivers output to a channel
- `Groups()` — reads the in-memory group map

This is wrong. The scheduler is not a worker. It should not
know how containers run or how messages are delivered.

## Target State (decoupled)

The scheduler is its own process. It opens `messages.db`,
runs its own migrations, and loops:

```
every poll_interval:
  SELECT * FROM scheduled_tasks
    WHERE status = 'active' AND next_run <= now

  for each due task:
    INSERT INTO messages (id, chat_jid, sender, content, timestamp)
      VALUES (task-{id}-{run}, task.chat_jid, 'scheduler', task.prompt, now)
    UPDATE scheduled_tasks SET next_run = ..., last_run = now
    INSERT INTO task_run_logs (task_id, run_at, ...)
```

The message appears in the bus. The router picks it up, resolves
the route, hands it to the worker. The worker runs the container.
Output flows back through messages. The scheduler never sees any
of this.

## Deps

```go
type Scheduler struct {
  db       *sql.DB
  timezone string
}
```

That's it. No gateway, no queue, no channels, no groups map.

## Tables Owned

- `scheduled_tasks` — task definitions, next_run, status
- `task_run_logs` — execution history

## Tables Written (shared bus)

- `messages` — INSERT only, sender='scheduler'

## Migration Runner

Own `migrations/` directory, own service name in migrations table:

```sql
-- scheduler migration 0001
CREATE TABLE IF NOT EXISTS scheduled_tasks (...);
CREATE TABLE IF NOT EXISTS task_run_logs (...);
```

## Context Modes

- **isolated** (default): scheduler inserts message with a marker
  that tells the worker to use a fresh session.
- **group**: scheduler inserts a normal user message. The worker
  processes it in the existing group session — indistinguishable
  from a human message except for `sender = 'scheduler'`.

The scheduler doesn't need to implement these modes differently.
It always just inserts a message. The worker reads the task's
`context_mode` and decides session strategy.

## Task CRUD

Tasks are created/paused/cancelled via IPC actions (from agents)
or the admin API. The scheduler only reads them — it doesn't
expose any API of its own. CRUD goes directly to the DB.

## Error Handling

The scheduler logs to `task_run_logs` that it fired the message.
Whether the container succeeds or fails is the worker's problem.
The scheduler's job is done once the message is in the table.

If the worker needs to report task results back, it updates
`scheduled_tasks.last_result` directly — it owns that write
path as the executor, even though the scheduler owns the table.
This is the one acceptable cross-service write.
