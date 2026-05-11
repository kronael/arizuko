---
name: cli
description: >
  CLI tool patterns — argparse/click/clap, --help text, exit codes,
  SIGTERM/SIGINT, config precedence. USE for "build a CLI tool",
  "add a --flag", entrypoints, argparse/click/clap files, interactive
  prompts. NOT for web APIs (use service).
user-invocable: true
---

# CLI Style

## Arguments

- Short flags for common ops: `-c`, `-v`, `-h`
- Repeat flag for multiples (`-e a -e b`) or comma: `--hosts=a,b`
- Positional: required `<identity>`, optional `[branch]`

## Config precedence

CLI flags > env vars > config files > defaults. Fail fast on invalid config.

## Exit codes

0 success, 1 config error, 2 runtime (retryable), 3 fatal, 130 interrupted.

## Output

- stdout = results, stderr = errors
- `--json` for machine parsing, `--quiet` for scripts
- Error messages MUST be actionable (show got + fix)

## Modes

- `--yes` for non-interactive (CI/automation)
- `--dry-run` for destructive operations

## Rules

- NEVER write secrets to logs
- ALWAYS validate config on load, BEFORE any operations
- Fixture data in `cfg/test/`, never production configs in tests
