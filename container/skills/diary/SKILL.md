---
name: diary
description: If anything worth noting happened since your last diary entry,
  record it in /workspace/group/diary/YYYYMMDD.md.
---

# Diary

Append a short entry to today's diary file. The diary is your
persistent memory across sessions and compactions.

## Path

```
/workspace/group/diary/YYYYMMDD.md
```

Create the file if it doesn't exist. Append to it if it does.

## Format

```markdown
---
summary: |
  Working on kanipi gateway. Alice is the main user.
  - auth: OAuth flow design, provider TBD
  - deploy: REDACTED done
  - ipc: two file-sending bugs open
---

## 10:32

Helped Alice configure Ansible for REDACTED. Vault password
path was wrong — fixed. deploy: done.

## 14:07

Auth flow discussion. Alice wants OAuth not passwords.
auth: provider TBD. New task — ipc: file sending broken,
ENOENT on sendDocument.
```

## Summary

YAML block scalar. Keep it short. First line: project and who
you work with. Then up to 5 bullet points of clearly important
tasks only — if you're unsure whether something belongs, leave
it out. Not every action, not every task. Only what matters
for picking up context cold.

Update the summary every time you write an entry.

## Entries

Each `## HH:MM` entry is a short note (250 chars max).
Entries naturally introduce and update tasks:

- "New task — auth: OAuth flow design"
- "auth: decided on GitHub provider"
- "deploy: done"

Tasks appear and change state through entries. No separate
tracking — the diary IS the task log.

## Rules

- 250 chars max per entry
- Only important, non-obvious things
- Long-running and tedious tasks, not routine operations
- May rewrite/compress earlier entries to save space
- If nothing noteworthy happened, skip — do not write
