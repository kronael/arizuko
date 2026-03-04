---
version: 3
description: migrate skill uses NANOCLAW_IS_MAIN for main-group detection
---

# Migration 003 — migrate skill main-group detection fix

The `migrate` skill previously checked for `/workspace/global` dir existence
to detect whether it was running in the main group. That dir always exists
(Dockerfile creates it), so all groups were treated as non-main.

This migration just records the skill update — no data changes required.
The updated `migrate/SKILL.md` already uses `NANOCLAW_IS_MAIN != 1`.
