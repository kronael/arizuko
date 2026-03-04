---
name: migrate
description: Sync skills and run pending migrations across all groups. Main group only. Use when asked to "migrate", "sync skills", "update skills", or "run migrations".
---

# Migrate

Main group only. Refuse if `/workspace/global` exists.

```bash
if test -d /workspace/global; then echo "ERROR: migrate is main-group only"; exit 1; fi
echo "main group confirmed"
```

## a) Skill sync

Copy updated skills from source to all group session dirs.

```bash
src=/workspace/self/container/skills

for session in /workspace/self/data/sessions/*/; do
  skills_dir="$session/.claude/skills"
  test -d "$skills_dir" || continue
  group=$(basename "$session")
  updated=""
  for skill in "$src"/*/; do
    name=$(basename "$skill")
    dest="$skills_dir/$name"
    # Compare SKILL.md; copy if missing or changed
    if ! diff -q "$skill/SKILL.md" "$dest/SKILL.md" >/dev/null 2>&1; then
      cp -r "$skill" "$dest"
      updated="$updated $name"
    fi
  done
  test -n "$updated" && echo "$group: updated$updated" || echo "$group: up to date"
done
```

## b) Run pending migrations

For each group session, check MIGRATION_VERSION and run missing migrations.

```bash
src=/workspace/self/container/skills/self/migrations

for session in /workspace/self/data/sessions/*/; do
  skills_dir="$session/.claude/skills/self"
  test -d "$skills_dir" || continue
  group=$(basename "$session")
  current=$(cat "$skills_dir/MIGRATION_VERSION" 2>/dev/null || echo 0)
  pending=$(ls "$src"/*.md 2>/dev/null \
    | grep -oP '/(\d+)-' | grep -oP '\d+' | sort -n \
    | awk -v v="$current" '$1 > v')
  if test -z "$pending"; then
    echo "$group: no pending migrations (version $current)"
    continue
  fi
  echo "$group: running migrations: $pending"
  for n in $pending; do
    f=$(ls "$src"/$(printf '%03d' $n)-*.md 2>/dev/null | head -1)
    test -f "$f" || continue
    echo "  → migration $n: $f"
    # Print migration instructions for the agent to follow
    cat "$f"
    # After agent executes steps, update version:
    echo "$n" > "$skills_dir/MIGRATION_VERSION"
  done
done
```

Report summary of groups updated and migrations run.
