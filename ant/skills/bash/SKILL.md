---
name: bash
description: Bash/shell scripting patterns.
when_to_use: Use when writing .sh files, entrypoints, or shell scripts.
---

# Bash Style

## Structure

- `set -e` at top (fail fast)
- `do`/`then`/`else` on own line, never after `;` or `&&`
- `[ ]` not `[[ ]]` (POSIX portable)
- Implicit relative paths (`cfg/` not `./cfg/`)
- No `basename $0`, no `dirname` tricks

## Variables

- `"${VAR:-default}"` for optional, `"${VAR:?msg}"` for required
- Always quote: `"$VAR"` not `$VAR`
- Uppercase for env/config, lowercase for locals

## Conditionals

```bash
if [ -f "$path" ]
then
  ...
fi
```

## Fallback pattern

```bash
SEED="/preferred/path"
[ -d "$SEED" ] || SEED="fallback/path"
```

## Heredocs

- `<<EOF` for multi-line output
- `<<'EOF'` to disable expansion

## Anti-patterns

- NEVER inline `; then` or `; do`
- NEVER unquoted variables
- NEVER `sudo` (ask user for privileged ops)
