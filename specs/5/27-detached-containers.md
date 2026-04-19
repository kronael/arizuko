---
status: unshipped
---

# Detached containers

File-based output in `ipc/<folder>/output/<ts>-<uuid>.json` (atomic
write, SIGUSR2 to gateway PID from `gateway.pid`). Gateway watches dir,
drains on reconnect after restart. Reclaims idle containers via
`docker ps` on startup.

Rationale: gated restart currently loses in-flight responses and kills
idle containers unnecessarily — output is coupled to docker exec stdout.

Unblockers: implement file writer in agent-runner, watcher +
reclaim path in `container/runner.go`, make `state.process` optional
in GroupQueue (kill via `docker kill <name>`).
