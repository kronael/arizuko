---
name: migrate
description: >
  Per-group self-migrate. Sync THIS group's own skills and
  `~/.claude/CLAUDE.md` from the platform source with 3-way conflict
  resolution, run pending migrations, apply template overlays, then
  announce the release to this group's routes. USE when asked to
  "migrate", "sync skills", "update skills", "run migrations", OR after
  pulling a new agent image / observing a bumped MIGRATION_VERSION. NOT
  for routine prompts, fresh sessions, or messages unrelated to release
  plumbing.
user-invocable: true
---

# Migrate

Sync YOUR OWN skills and `~/.claude/CLAUDE.md` against the platform
source via a real 3-way merge with the `.merge-base/` snapshot Go laid
down at seed-time. This is a **per-group self-migrate**: every group
runs `/migrate` on its own `~` — there is no root, no fan-out, no
walking other groups. Custom skills and the operator-owned `~/CLAUDE.md`
are never touched. Then run pending migrations, apply template overlays,
announce the release.

## Run discipline — do NOT bail on a remembered failure

If your session memory says "/migrate is blocked", "root-only", or
"`/workspace` not mounted", that memory is **stale**. The source lives
at `/opt/arizuko/ant/` (read-only mount every group has); there is no
root gate and no `/var/lib/groups` walk any more. ALWAYS verify the LIVE
layout THIS TURN before concluding anything — never echo a past
conclusion:

```bash
ls /opt/arizuko/ant/skills | head     # MUST list skills (the source is here)
cat ~/.claude/skills/self/MIGRATION_VERSION   # your current version
```

If `/opt/arizuko/ant/skills` lists skills, migrate CAN run — work
through EVERY step below to completion. Reporting "blocked" without
first running those two commands this turn is a contract break.

## Self-heal — missing `.merge-base` or stock skills

A group that predates `.merge-base/` (or a whole skill) has no base to
merge against. That is NOT a failure — the outcome table's "base
missing → first sync" rule copies `theirs → ours` and creates `base`.
So a fresh or long-stale group converges on the first run. Do not skip
a stock skill just because `~/.claude/.merge-base/<name>/` is absent:
treat an absent base as "first sync", copy it in, and write the base.
After the run, every stock skill under `/opt/arizuko/ant/skills/` must
exist under `~/.claude/skills/` (unless `.disabled`).

## How sync actually works

Per file: `base = ~/.claude/.merge-base/<path>`,
`ours = ~/.claude/<path>`, `theirs = /opt/arizuko/ant/<path>`.
Standard 3-way outcome table — see step (a). Operator changes survive
upstream rewrites; direct conflicts resolve to upstream. After every
write to `ours`, `base` is overwritten by `theirs` so the next sync's
diff is honest. A `.disabled` sentinel in a skill dir opts that skill
out of seeding and merging.

## Container paths

- `/opt/arizuko/` — app source (read-only), skills at `ant/skills/`
- `/home/node/` — YOUR group's home (`~`); the only tree you migrate

## a) Sync stock skills + CLAUDE.md (3-way merge)

Walk every file under `/opt/arizuko/ant/` that has (or should have) a
counterpart under `~/.claude/`. The merge base is the snapshot Go laid
down at seed-time (or at the last successful merge):
`~/.claude/.merge-base/<path>`.

Custom skills — those NOT present under `/opt/arizuko/ant/skills/` —
are never touched. `~/CLAUDE.md` (operator overlay) is also never
touched; only `~/.claude/CLAUDE.md` is merged.

For each candidate file:

```
base   = ~/.claude/.merge-base/<path>
ours   = ~/.claude/<path>
theirs = /opt/arizuko/ant/<path>
```

Skip the whole skill dir if `~/.claude/skills/<name>/.disabled` exists.

Outcomes:

- `base` missing → first sync: `cp theirs ours; cp theirs base`.
- `theirs` missing → upstream deleted the file: leave `ours` alone,
  record the deletion in the announce notes.
- `ours == base` → only upstream changed: `cp theirs ours; cp theirs base`.
- `theirs == base` → only operator changed: no-op (operator edits preserved).
- both differ → 3-way merge inline (see below), then `cp theirs base`.

Inline 3-way merge (no Task subagent — do it in this turn):

1. Read all three files with the `Read` tool.
2. Produce a merged result favoring operator changes that don't conflict
   with upstream, and upstream on direct conflict. Be conservative: when
   in doubt, keep both with a clear ordering. For SKILL.md frontmatter,
   prefer upstream `description`. For CLAUDE.md, prefer section-additive
   merges.
3. Write the merged file with `Write` (overwrites `ours`).
4. `cp theirs base` so the next sync's diff is correct.

Driver — enumerate YOUR OWN candidate files:

```bash
set -euo pipefail
shopt -s nullglob

src_root=/opt/arizuko/ant
ours_root="$HOME/.claude"

# CLAUDE.md (the operator's ~/CLAUDE.md is OFF-LIMITS)
echo "$src_root/CLAUDE.md"

# Stock skills only (those present upstream)
for sk in "$src_root/skills"/*/; do
  name=$(basename "$sk")
  if [ -f "$ours_root/skills/$name/.disabled" ]; then
    echo "skip $name"
    continue
  fi
  find "$sk" -type f -print0 \
    | while IFS= read -r -d '' f; do echo "$f"; done
done
```

For each printed file, compute base/ours/theirs paths by string
substitution (`/opt/arizuko/ant/` → `$HOME/.claude/` for ours,
→ `$HOME/.claude/.merge-base/` for base) and apply the outcomes table.

## b) Run pending migrations

Apply every migration newer than your stored version, in order, then
stamp the new version — YOUR self dir only:

```bash
src=/opt/arizuko/ant/skills/self/migrations
skills_dir="$HOME/.claude/skills/self"
current=$(cat "$skills_dir/MIGRATION_VERSION")
pending=$(ls "$src"/*.md \
  | grep -oP '/(\d+)-' | grep -oP '\d+' | sort -n \
  | awk -v v="$current" '$1 > v')
if test -z "$pending"; then
  echo "up to date ($current)"
else
  for n in $pending; do
    f=$(ls "$src"/$(printf '%03d' $n)-*.md | head -1)
    echo "migration $n: $f"
    cat "$f"
    echo "$n" > "$skills_dir/MIGRATION_VERSION"
  done
fi
```

## c) Re-read CLAUDE.md

If migrations updated `~/.claude/CLAUDE.md`, re-read it and follow new
instructions immediately.

```bash
cat ~/.claude/CLAUDE.md
```

## d) Apply template overlays

If `~/.claude/skills/self/TEMPLATES` exists, apply its named overlays
from `/opt/arizuko/template/<name>/` onto YOUR OWN home:

```bash
src_templates=/opt/arizuko/template
self_dir="$HOME/.claude/skills/self"
tfile="$self_dir/TEMPLATES"
test -f "$tfile" || exit 0

while IFS= read -r name || [ -n "$name" ]; do
  name=$(echo "$name" | tr -d '[:space:]')
  [ -z "$name" ] && continue
  tdir="$src_templates/$name"

  [ -f "$tdir/PERSONA.md" ] && cp "$tdir/PERSONA.md" "$HOME/PERSONA.md"
  [ -f "$tdir/SYSTEM.md" ]  && cp "$tdir/SYSTEM.md"  "$HOME/SYSTEM.md"

  if [ -f "$tdir/CLAUDE.md" ]; then
    target="$HOME/.claude/CLAUDE.md"
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
      dest="$HOME/.claude/skills/$sname"
      grep -qE "^(disabled: true|managed: local)" "$dest/SKILL.md" 2>/dev/null && continue
      cp -r "$skill_dir" "$dest"
    done
  fi

  if [ -d "$tdir/.claude/output-styles" ]; then
    mkdir -p "$HOME/.claude/output-styles/"
    cp "$tdir/.claude/output-styles/"* "$HOME/.claude/output-styles/" 2>/dev/null
  fi
done < "$tfile"

date -u +%Y-%m-%dT%H:%M:%SZ > "$self_dir/TEMPLATES.applied"
```

## e) Announce the release

Announce ONCE to THIS group's own routes. **One short message. Run the
script, send verbatim. No additions.**

Format (three lines, blank between bullet and link):

```
version — most impactful user-facing change
<blank>
https://github.com/kronael/arizuko/blob/main/CHANGELOG.md
```

```bash
latest=$(awk '/^## \[v/{print $2; exit}' /opt/arizuko/CHANGELOG.md | tr -d '[]')
guard=~/.announced-version
last=$(cat "$guard" 2>/dev/null || echo "")
if [ "$latest" = "$last" ]; then echo "SKIP"; exit 0; fi
echo "$latest" > "$guard"
# First bullet under the > blockquote — the top user-facing change
bullet=$(awk '/^## \[v/{n++} n==1 && /^> •/{sub(/^> • /,""); print; exit}' \
  /opt/arizuko/CHANGELOG.md)
printf '%s — %s\n\nhttps://github.com/kronael/arizuko/blob/main/CHANGELOG.md\n' "$latest" "$bullet"
```

The script prints `SKIP` (stop) or the exact message. Send verbatim via
`send`. Plain text — no markdown bold, no blockquote, no extra bullets.
The link on its own line so chat clients render it as a preview card.

`inspect_routing` returns YOUR group's routes. Send one message per
unique destination; skip web/slink/wildcard routes (no announcements):

- **Telegram** (`room=`): one message per unique JID
- **Slack** (`chat_jid=slack:`): one message per workspace (team ID)
- **Discord** (`chat_jid=discord:`): one message per server (guild ID)
- **web:, slink:, wildcards (`*`)**: skip

```bash
MSG="<output of script above>"
[ "$MSG" = "SKIP" ] && exit 0

mcpc connect "socat UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -" @s
trap 'mcpc @s close' EXIT

mcpc @s tools-call inspect_routing | jq -r '
  .routes[] | .match
  | if startswith("room=") then sub("^room=";"")
    elif startswith("chat_jid=slack:") or startswith("chat_jid=discord:") then
      sub("^chat_jid=";"")
    else empty end
  | select(contains("*") | not)
' \
  | awk '
    /^slack:/ { key="slack:"substr($0,8,index(substr($0,8),"/")-1) }
    /^discord:/ { key="discord:"substr($0,9,index(substr($0,9),"/")-1) }
    !/^slack:|^discord:/ { key=$0 }
    !seen[key]++ { print }
  ' \
  | while read jid; do
    test -n "$jid" && mcpc @s tools-call send chatJid:="$jid" text:="$MSG"
  done
```

The `~/.announced-version` guard makes this idempotent — a retry does
NOT re-announce. Do NOT re-write the guard on per-message retries.
