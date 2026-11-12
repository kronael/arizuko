# timed

Scheduled tasks run inside routd. This source tree contains no timed executable, main function, compose service, store owner, or daemon listener.
Use [routd](../routd/README.md#routd-fires-scheduled-tasks) to configure and debug scheduling.

```text
routd startup and 60 s tick ──► due scheduled_tasks ──► normal message ingress
                                                        │
                                                        ▼
                                                 runed executes the turn
```

## Where each scheduler concern lives

| Concern            | Current owner                                                                                                                   |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| Task polling       | `routd/schedule.go`: once at startup, then every 60 s.                                                                          |
| Persistent rows    | `routd.scheduled_tasks`, `routd.task_run_logs`; routd's store credentials.                                                      |
| REST resource      | `scheduled_tasks`: `GET /v1/tasks`, `GET /v1/tasks/{taskId}`, `PATCH /v1/tasks/{taskId}`, `DELETE /v1/tasks/{taskId}` on routd. |
| Agent tools        | `schedule_task`, `list_tasks`, `pause_task`, `resume_task`, `reschedule_task`, `cancel_task` on routd's per-turn MCP socket.    |
| Health and metrics | routd's `/health`, `/metrics` when enabled, and `/dash/routd/` scheduler state.                                                 |
| Configuration      | routd environment and task rows; there are no `TIMED_*` process reads.                                                          |

A failed one-time enqueue keeps its scheduled time for retry. A failed reschedule can leave a task claimed until routd startup recovery.
Grep routd logs for `claim due tasks`, `enqueue task message`, `reschedule task`, and `task not restored to active`.
The run log records enqueue outcome; it does not prove that an agent replied or an external platform accepted the reply.
Task retries and delivery retries use routd's normal mechanisms.
There are no timed-owned files or resreg registrations, and no separate timed store key in `StoreReach`.

Sources: [scheduler](../routd/schedule.go), [startup recovery](../routd/loop.go),
[resource contract](../resreg/resources/scheduled_tasks.go), [owner reach](../store/owner_reach.go).
