---
name: migrate
description: Intelligently sync skills and files across groups with conflict resolution. Root group only. Use when asked to "migrate", "sync skills", "update skills", or "run migrations".
---

# Migrate

Sync skills and config across groups. Merges upstream changes, preserves local edits.

## Container paths

Inside the container, the relevant mounts are:

- `/workspace/self/` — app source (read-only), skills source at `ant/skills/`
- `/workspace/data/groups/` — all group directories (root only)
- `/home/node/` — current group's home directory (`~`)

## Root-only check

```bash
if [ "$ARIZUKO_IS_ROOT" != "1" ]; then
  echo "ERROR: migrate is root-group only"
  exit 1
fi
```

## Group discovery

1. Call `refresh_groups` MCP tool — returns registered groups with jid, folder, name
2. Each group lives at `/workspace/data/groups/<folder>/`

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

Spawn a Task agent per group to merge `/workspace/self/ant/` into
`/workspace/data/groups/{group}/`. Agent compares, merges, reports.

## Conflict resolution rules

When both source and dest have changes:

**SKILL.md**: merge YAML frontmatter (prefer source description), add new rules from source, preserve local additions.

**CLAUDE.md**: merge sections additively, preserve local sections, update global wisdom from source.

**Web / code files**: preserve local modifications, update unchanged files from source.

## b) Run pending migrations

For each group session, check MIGRATION_VERSION and run missing migrations.

```bash
src=/workspace/self/ant/skills/self/migrations

for session in /workspace/data/groups/*/; do
  skills_dir="$session/.claude/skills/self"
  group=$(basename "$session")
  current=$(cat "$skills_dir/MIGRATION_VERSION")
  pending=$(ls "$src"/*.md \
    | grep -oP '/(\d+)-' | grep -oP '\d+' | sort -n \
    | awk -v v="$current" '$1 > v')
  if test -z "$pending"; then
    echo "$group: up to date (version $current)"
    continue
  fi
  echo "$group: running migrations: $pending"
  for n in $pending; do
    f=$(ls "$src"/$(printf '%03d' $n)-*.md | head -1)
    echo "  → migration $n: $f"
    cat "$f"
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
from `/workspace/self/template/<name>/`.

```bash
src_templates=/workspace/self/template

for session in /workspace/data/groups/*/; do
  self_dir="$session/.claude/skills/self"
  tfile="$self_dir/TEMPLATES"
  test -f "$tfile" || continue
  group=$(basename "$session")

  while IFS= read -r name || [ -n "$name" ]; do
    name=$(echo "$name" | tr -d '[:space:]')
    [ -z "$name" ] && continue
    tdir="$src_templates/$name"

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

## e) Announce the release

After migrations apply, broadcast the changelog to each group so users
on the actual channels see what changed. Manual step until
`specs/3/e-migration-announce.md` lands.

### Check what's new

Write the target version BEFORE sending to prevent a mid-broadcast
restart from re-announcing. On per-group send failure, retry that JID
only — do not roll back the file.

```bash
latest=$(awk '/^## \[v/{print $2; exit}' /workspace/self/CHANGELOG.md \
  | tr -d '[]')
last=$(cat ~/.announced-version 2>/dev/null || echo "")
test "$latest" = "$last" && { echo "already announced $latest"; exit 0; }
echo "$latest" > ~/.announced-version  # claim the version first
```

Print the changelog entry to compose the message:

```bash
awk '/^## \[v[0-9]/{if(++n==1){print;next};exit} n==1' \
  /workspace/self/CHANGELOG.md
```

### Compose the message

Keep it short — one screenful. Strip `###` subheadings to plain bullets.
Title names the version. Example:

```
arizuko upgraded — v0.28.0

- token-based web onboarding (chat → auth link → dashboard)
- ACL flip: no user_groups row = no access
- XSS + replay hardening on onbod
- 13 agent skills synced across groups
```

### Fan out

Root agent calls `refresh_groups` to get every registered group, then
`send_message` to each group's primary jid.

```bash
# pseudocode for the mcpc flow
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT

mcpc @s tools-call refresh_groups | jq -r '.groups[] | .folder' \
  | while read folder; do
    # look up primary jid for folder from routes
    jid=$(sqlite3 /workspace/store/messages.db \
      "SELECT substr(match, 6) FROM routes WHERE target = '$folder' \
       AND match LIKE 'room=%' LIMIT 1")
    test -n "$jid" && mcpc @s tools-call send_message \
      jid:="$jid" text:="$MSG"
  done
```

Or the root agent reads the groups list from the MCP result and sends
one message per group in its own turn.

`~/.announced-version` was written above — do NOT write it again. Per-
group retry duplicates are fine; re-announcing the WHOLE release is the
bug this file prevents.

### Scope and etiquette

- Broadcast only to active registered groups (refresh_groups filters).
- Send once; let the channel's own retry/queue handle offline groups.
- One message per release, not per migration. Users care about product
  changes, not internal migration numbers.
