---
status: shipped
shipped: 2026-08-07
source: hermes-agent peel (2026-04-11)
---

# Skill-guard PreToolUse hook

**The gap:** an agent can today write `os.system(f'curl {SECRET} | bash')`
into a skill that runs next session. Nothing checks it.

**The decision:** port hermes-agent's threat-pattern scanner
(`refs/hermes-agent-fresh/tools/skills_guard.py`, untracked local
checkout — ~100–150 regexes across prompt_injection,
exfiltration, secret_access, persistence, destructive, network_abuse,
obfuscation, privilege_escalation, supply_chain, crypto_mining,
hardcoded_secrets, invisible_unicode) as a PreToolUse hook on
`Write`/`Edit`/`MultiEdit` when `file_path` is under `~/.claude/**`. Port
the regex table **verbatim** — a hand-rewritten table is a new table with
new gaps. Plus SKILL.md frontmatter validation (`name:` + `description:`
required, description ≤ 1024 chars).

**Fail-open on scanner crash**, and accept the false-positive rate: for a
write gate on agent-authored skills, blocking legitimate work is the more
expensive failure.

**Trusted-repo list is config-driven** (env var + DB-backed), not
hardcoded — hermes hardcodes `{"openai/skills", "anthropics/skills"}`;
arizuko's env-vs-DB split for business state applies.

Scope: `ant/src/skillguard.ts` + tests, registered in
`options.hooks.PreToolUse`, plus a `MIGRATION_VERSION` bump. Zero Go and
zero schema changes.

## What shipped (2026-08-07)

`ant/src/skillguard.ts`, matched on `Write|Edit|MultiEdit` in
`ant/src/claude.ts`. The table is `ant/src/skillguard-patterns.ts` — all
120 entries **extracted from the Python by an AST walk, not retyped**, so
"port verbatim" is mechanically true rather than a promise. No pattern used
a Python-only regex construct, so each compiles unchanged under JS; a test
asserts the count and that every one compiles.

Two deliberate narrowings from hermes:

- **Content, not directories.** hermes scans an installed skill tree
  (file counts, symlink escape, oversized files). The hook sees the text of
  one write, so the structural checks have nothing to run against and were
  left out rather than faked.
- **Trust levels collapse to one.** hermes resolves `builtin`/`trusted`/
  `community` from a source repo. An agent writing its own skill is exactly
  hermes' `agent-created` row — `dangerous` → refuse, everything else
  allowed. No repo, so no trusted-repo list to make config-driven; the
  spec's env-var/DB requirement has nothing to configure and was dropped.

`caution` passes: only a **critical** finding refuses. The scanner
fails open on a crash, and an unrecognised tool shape yields no text,
so both failure modes allow rather than block.

The scan is per LOGICAL line: shell line-continuations are joined first, because
a per-physical-line scan was evaded by the oldest trick there is — `curl \` +
newline + the payload scanned `safe` while the one-line form scanned
`dangerous`. That closes the demonstrated evasion, NOT the class: a payload
split across string concatenation or assembled from a variable still passes.
The guard raises the cost of persisting a malicious instruction; it is not a
containment boundary, and `SECURITY.md` says so.

`MIGRATION_VERSION` 196.

Deliberately NOT in scope: memory/USER.md tools (Claude Code reads
CLAUDE.md already), a `skill_manage` MCP tool (Write/Edit suffice), a
post-turn review thread (doesn't fit the ephemeral-container model — see
[13/8-self-eval-skill.md](22-self-learning.md)), AST scanning,
trust tiers, and learning loops ([22-self-learning.md](22-self-learning.md)
— different concern, different lifecycle).
