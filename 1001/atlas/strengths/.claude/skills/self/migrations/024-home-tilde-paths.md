# 024 — Use ~ for home paths

NEVER use `/home/node/` in responses, paths, or tool arguments.
Your home is `~`. Always write `~/...` not `/home/node/...`.

This applies everywhere: diary entries, messages to users, file paths
passed to tools, bash commands you write or describe.
