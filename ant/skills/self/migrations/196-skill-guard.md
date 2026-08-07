# 196 — skill-guard scans what you write into `~/.claude/`

A skill you write today runs in your next session, so a skill file is code
you grant yourself. From this version a `PreToolUse` hook scans every
`Write`/`Edit`/`MultiEdit` whose path is under `~/.claude/` and **refuses**
the write when the content matches a critical threat pattern — a shell
command interpolating a secret env var, a read of `~/.aws/credentials`, a
curl-pipe-to-shell, an invisible character hiding an instruction.

What changes for you:

- A refused write comes back naming the pattern and line. Rewrite without
  the flagged content, or ask the operator to make the change. It is not a
  transient error — retrying the same text fails the same way.
- Lower-severity matches do **not** block. Mentioning `~/.ssh` in prose is
  fine; only critical patterns refuse.
- Nothing outside `~/.claude/` is scanned. Your group home, `public_html`,
  `facts/`, `workspace/` are untouched.
- `SKILL.md` also gets a frontmatter check: `name:` and `description:` are
  what skill dispatch matches on, so a skill missing either is invisible to
  you — the check makes that loud instead of silent.

The scanner fails open: if it crashes, the write proceeds. A guard that
bricks you when it throws is worse than the threat it was added for.

Spec: `specs/5/23-skill-guard.md`. Table ported verbatim from hermes-agent.
