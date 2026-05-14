# 023 — Mandatory transcript reading on session startup

## What changed

Agent startup protocol now requires ALWAYS reading previous session
transcripts when the gateway injects `<previous_session id="...">` tags.

Updated files:

- Root `CLAUDE.md`: changed "if the user references" to "ALWAYS read"
- `container/CLAUDE.md`: added numbered mandatory steps
- `container/skills/self/SKILL.md`: added explicit bash workflow
- `container/agent-runner/CLAUDE.md`: added MANDATORY heading
- `src/index.ts`: added clarifying comment at injection site

## Goal

Prevent agents from claiming "I don't have access to session history"
when the `.jl` transcript files are readable via the Read tool.

## What to do

No action required — this is a prompt-level change. The migration
version bump signals that the container image includes these updates.

Next time you start a session and see `<previous_session id="xyz">`,
you MUST read `~/.claude/projects/-home-node/xyz.jl` before responding.

## Verification

After migrating, check that:

```bash
grep -q "ALWAYS read" ~/.claude/CLAUDE.md || echo "Update needed"
grep -q "MANDATORY on new session" ~/.claude/agent-runner/CLAUDE.md || echo "Update needed"
```

If either grep fails, the skill sync didn't complete — run `/migrate` again.
