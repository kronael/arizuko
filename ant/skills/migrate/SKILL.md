---
name: migrate
description: >
  Root group only. Sync skills and files across groups with conflict resolution.
  Use when asked to "migrate", "sync skills", "update skills", or "run
  migrations", OR after pulling a new agent image / observing a bumped
  MIGRATION_VERSION. Do NOT invoke on routine prompts, fresh sessions, or
  messages unrelated to release plumbing.
user-invocable: true
---

# Migrate

Sync skills and config across groups. Merges upstream changes, preserves
local edits. Then runs pending migrations, applies template overlays,
announces the release.

## Container paths

- `/workspace/self/` — app source (read-only), skills at `ant/skills/`
- `/workspace/data/groups/` — all group directories (root only)
- `/home/node/` — current group's home (`~`)

## Root-only

```bash
[ "$ARIZUKO_IS_ROOT" = "1" ] || { echo "ERROR: root-only"; exit 1; }
```

## a) Sync skills

Call `refresh_groups` for the group list. Each group lives at
`/workspace/data/groups/<folder>/`.

Per group, spawn a Task agent to merge `/workspace/self/ant/` into the
group dir. Rules:

- New in source → copy
- Unchanged → skip
- Upstream-only changes → update
- Local-only changes → preserve
- Both changed → 3-way merge

For conflicts:

- **SKILL.md** — merge YAML (prefer source description), add new rules,
  preserve local additions.
- **CLAUDE.md** — merge sections additively, preserve local sections.
- **Web / code files** — preserve local mods, update unchanged files.

Never `cp -r` or `rsync` blindly.

## b) Run pending migrations

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
    echo "$group: up to date ($current)"
    continue
  fi
  for n in $pending; do
    f=$(ls "$src"/$(printf '%03d' $n)-*.md | head -1)
    echo "$group: migration $n: $f"
    cat "$f"
    echo "$n" > "$skills_dir/MIGRATION_VERSION"
  done
done
```

## c) Re-read CLAUDE.md

If migrations updated `~/.claude/CLAUDE.md`, re-read it and follow new
instructions immediately.

```bash
cat ~/.claude/CLAUDE.md
```

## d) Apply template overlays

For each group with `~/.claude/skills/self/TEMPLATES`, apply named
overlays from `/workspace/self/template/<name>/`.

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

    [ -f "$tdir/SOUL.md" ]   && cp "$tdir/SOUL.md"   "$session/SOUL.md"
    [ -f "$tdir/SYSTEM.md" ] && cp "$tdir/SYSTEM.md" "$session/SYSTEM.md"

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
    fi

    if [ -d "$tdir/.claude/skills" ]; then
      for skill_dir in "$tdir/.claude/skills/"/*/; do
        sname=$(basename "$skill_dir")
        dest="$session/.claude/skills/$sname"
        grep -qE "^(disabled: true|managed: local)" "$dest/SKILL.md" 2>/dev/null && continue
        cp -r "$skill_dir" "$dest"
      done
    fi

    if [ -d "$tdir/.claude/output-styles" ]; then
      mkdir -p "$session/.claude/output-styles/"
      cp "$tdir/.claude/output-styles/"* "$session/.claude/output-styles/" 2>/dev/null
    fi
  done < "$tfile"

  date -u +%Y-%m-%dT%H:%M:%SZ > "$self_dir/TEMPLATES.applied"
done
```

## e) Announce the release

Broadcast the latest `CHANGELOG.md` entry to every registered group so
users on Telegram / Discord / WhatsApp see what changed.

Claim the version BEFORE the fan-out so a mid-broadcast restart cannot
re-announce:

```bash
latest=$(awk '/^## \[v/{print $2; exit}' /workspace/self/CHANGELOG.md \
  | tr -d '[]')
last=$(cat ~/.announced-version 2>/dev/null || echo "")
test "$latest" = "$last" && { echo "already announced $latest"; exit 0; }
echo "$latest" > ~/.announced-version
```

Read the changelog entry for the message. Each release entry begins
with a `>` blockquote — that's the user-facing summary. Extract
ONLY the version header + the blockquote; skip the dev sections
(`### Added/Changed/Fixed`) which are too noisy for chats.

```bash
header=$(awk '/^## \[v/{print; exit}' /workspace/self/CHANGELOG.md)
summary=$(awk '/^## \[v/{n++} n==1 && /^> /{sub(/^> ?/, ""); print} n==1 && /^### /{exit}' \
  /workspace/self/CHANGELOG.md)

MSG="$header

$summary"
```

This produces a short user-friendly note like:

```
## [v0.33.0] — 2026-05-02

arizuko v0.33.0 — 2 May 2026

• Voice replies (`send_voice`) — Telegram/WhatsApp PTT, Discord audio
• Thread-scoped history (`get_thread` MCP)
• External agents drive groups via `/slink/<token>/mcp`
• OAuth account linking + collision UX (`/dash/profile`)
• Typed JID routing (`telegram:group/*` instead of sign-bit guess)

Full notes: github.com/kronael/arizuko/blob/main/CHANGELOG.md
```

Format spec: see "## Announcing" in root `CLAUDE.md`. The blockquote
is the broadcast verbatim; ≤ 9 lines; 3–6 bullets; user benefit before
internal detail; close with the canonical changelog link.

If a version block has no blockquote (older entries pre-dating this
convention), the summary is empty — fall back to a one-line
"v0.32.x deployed" message and link the changelog URL.

One message per release, not per migration.

The repo is `github.com/kronael/arizuko`. Always cite this exact URL
if the broadcast references upstream/source — never "krons labs",
"kron labs", or any other made-up org name.

Fan out via `refresh_groups` → resolve each folder's primary JID from
`inspect_routing` (routes with `match` prefix `room=` point at a JID) →
`send_message`:

```bash
mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT

routes=$(mcpc @s tools-call inspect_routing)

mcpc @s tools-call refresh_groups | jq -r '.groups[] | .folder' \
  | while read folder; do
    jid=$(echo "$routes" | jq -r --arg f "$folder" '
      .routes[] | select(.target == $f)
                | .match | select(startswith("room=")) | sub("^room=";"")
    ' | head -1)
    test -n "$jid" && mcpc @s tools-call send_message \
      jid:="$jid" text:="$MSG"
  done
```

Per-group retries are fine; do NOT re-write `~/.announced-version`.
Send once; let the channel queue handle offline groups.
