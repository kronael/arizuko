---
name: migrate
description: Intelligently sync skills and files across groups with conflict resolution. Root group only. Use when asked to "migrate", "sync skills", "update skills", or "run migrations".
---

# Migrate

Sync skills and config across groups. Merges upstream changes, preserves local edits.

## Root-only check

```bash
if [ "$ARIZUKO_IS_ROOT" != "1" ]; then
  echo "ERROR: migrate is root-group only"
  exit 1
fi
```

## Migration strategy

- NEVER use simple cp/rsync
- ALWAYS agent-driven merge: detect, classify, resolve

Merge rules:

- New in source → copy
- Unchanged → skip
- Upstream-only changes → update
- Local-only changes → preserve
- Both changed → agent-driven 3-way merge

## Implementation

Use Task tool with general-purpose agent to perform migration for each group:

```
For each session in /workspace/data/sessions/*/:
  - Spawn agent with migration task
  - Agent reads source: /workspace/self/container/
  - Agent reads dest: /workspace/data/sessions/{group}/
  - Agent performs intelligent merge:
    * Skills: Compare SKILL.md files, merge if both changed
    * Web files: Preserve local edits
    * Config: Merge CLAUDE.md sections intelligently
  - Agent reports: files updated, conflicts resolved, files preserved
```

## Agent prompt template

```
Migrate {source} to {dest} intelligently:

1. List all files in source and dest
2. For each file:
   - New in source? → Copy
   - Deleted in source but modified in dest? → Keep and warn
   - Changed in source only? → Update
   - Changed in dest only? → Preserve
   - Changed in both? → 3-way merge or ask user
3. Report summary:
   - Copied: {count}
   - Updated: {count}
   - Preserved: {count}
   - Conflicts: {list}
```

## Conflict resolution rules

When both source and dest have changes:

**SKILL.md files**:

- If description/frontmatter changed: merge YAML frontmatter, prefer source description
- If content rules changed: add new rules from source, preserve local additions
- If examples changed: merge examples, prefer more comprehensive version

**Web files**:

- If file has local customizations → preserve (detect by comparing with previous version if available)
- Otherwise → update from source

**CLAUDE.md**:

- Merge sections additively
- Preserve local project-specific sections
- Update global wisdom sections from source

**Code files (main, scripts)**:

- If dest has been modified → preserve and warn
- If dest is unchanged → update from source
- NEVER overwrite local code changes

## b) Run pending migrations

For each group session, check MIGRATION_VERSION and run missing migrations.

```bash
src=/workspace/self/container/skills/self/migrations

for session in /workspace/data/sessions/*/; do
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

## c) Re-read CLAUDE.md

After migrations that update `~/.claude/CLAUDE.md`, re-read it to apply
changes in the current session:

```bash
cat ~/.claude/CLAUDE.md
```

Read the output and follow any new instructions immediately.

## d) Apply template overlays

For each group with `~/.claude/skills/self/TEMPLATES`, apply named overlays
from `/workspace/self/templates/<name>/`.

```bash
src_templates=/workspace/self/templates

for session in /workspace/data/sessions/*/; do
  self_dir="$session/.claude/skills/self"
  tfile="$self_dir/TEMPLATES"
  test -f "$tfile" || continue
  group=$(basename "$session")

  while IFS= read -r name || [ -n "$name" ]; do
    name=$(echo "$name" | tr -d '[:space:]')
    [ -z "$name" ] && continue
    tdir="$src_templates/$name"
    if [ ! -d "$tdir" ]; then
      echo "  $group: warning: template '$name' not found, skipping"
      continue
    fi

    [ -f "$tdir/SOUL.md" ]   && cp "$tdir/SOUL.md"   "$session/SOUL.md"   && echo "$group: $name: SOUL.md"
    [ -f "$tdir/SYSTEM.md" ] && cp "$tdir/SYSTEM.md" "$session/SYSTEM.md" && echo "$group: $name: SYSTEM.md"

    if [ -f "$tdir/CLAUDE.md" ]; then
      target="$session/.claude/CLAUDE.md"
      python3 -c "
import re
src = open('$tdir/CLAUDE.md').read()
tgt = open('$target').read() if __import__('os').path.exists('$target') else ''
parts = re.split(r'(?=^## )', src, flags=re.M)
with open('$target', 'a') as f:
    for p in parts:
        h = re.match(r'^(## [^\n]+)', p)
        if h and h.group(1) not in tgt:
            f.write(('\n' if tgt.rstrip() else '') + p)
            tgt += p
"
      echo "$group: $name: CLAUDE.md merged"
    fi

    if [ -d "$tdir/.claude/skills" ]; then
      for skill_dir in "$tdir/.claude/skills/"/*/; do
        sname=$(basename "$skill_dir")
        dest="$session/.claude/skills/$sname"
        grep -qE "^(disabled: true|managed: local)" "$dest/SKILL.md" 2>/dev/null && continue
        cp -r "$skill_dir" "$dest" && echo "$group: $name: skills/$sname"
      done
    fi

    if [ -d "$tdir/.claude/output-styles" ]; then
      mkdir -p "$session/.claude/output-styles/"
      cp "$tdir/.claude/output-styles/"* "$session/.claude/output-styles/" 2>/dev/null
      echo "$group: $name: output-styles"
    fi
  done < "$tfile"

  date -u +%Y-%m-%dT%H:%M:%SZ > "$self_dir/TEMPLATES.applied"
  echo "$group: overlays done"
done
```

Report summary of groups updated and migrations run.
