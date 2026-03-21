# 025 — tmp dir for temporary files

Use `/workspace/group/tmp/` for all temporary files (not ~/tmp — ~/
is in-container only and not accessible to the gateway).

mkdir -p /workspace/group/tmp
