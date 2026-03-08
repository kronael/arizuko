---
name: migrate
description: Intelligently sync skills and files across groups with conflict resolution. Root group only. Use when asked to "migrate", "sync skills", "update skills", or "run migrations".
---

# Migrate

Intelligent migration system that merges changes while preserving local modifications.

## Root-only check

```bash
if [ "$NANOCLAW_IS_ROOT" != "1" ]; then
  echo "ERROR: migrate is root-group only"
  exit 1
fi
```

## Migration strategy

NEVER use simple cp/rsync for migrations. ALWAYS use intelligent agent-driven merging:

1. **Detect changes**: Compare source vs destination
2. **Classify conflicts**: Identify local modifications vs upstream changes
3. **Intelligent merge**:
   - New files → copy
   - Unchanged files → skip
   - Upstream-only changes → update
   - Local-only changes → preserve
   - Both changed → agent-driven 3-way merge
4. **Respect markers**: Honor .migration-exclude and local customizations

## Implementation

Use Task tool with general-purpose agent to perform migration for each group:

```
For each session in /workspace/data/sessions/*/:
  - Spawn agent with migration task
  - Agent reads source: /workspace/self/container/
  - Agent reads dest: /workspace/data/sessions/{group}/
  - Agent performs intelligent merge:
    * Skills: Compare SKILL.md files, merge if both changed
    * Web files: Respect .migration-exclude, preserve local edits
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

- Check for .migration-exclude marker
- If file listed in exclusion → skip entirely
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

Report summary of groups updated and migrations run.
