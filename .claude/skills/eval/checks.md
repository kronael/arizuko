# Eval checks — arizuko

The twenty-one health checks, the report shape they fill, and the failure modes
production has already shown. `INSTANCE` and the `sq` helper come from
`SKILL.md` § "Reaching the store" — source it before the first query.

## Checks (run in order; each is independent)

### 1. Service health

```bash
INSTANCE=krons
sudo systemctl is-active arizuko_${INSTANCE}
# Split topology: containers are arizuko_<daemon>_<instance> (routd, runed, authd,
# onbod, timed, proxyd, webd, dashd, davd, + adapters). Filter on the _<instance>
# suffix — "name=arizuko_${INSTANCE}" matches NOTHING (no bare arizuko_krons container).
sudo docker ps --filter "name=_${INSTANCE}" --format "{{.Names}} {{.Status}}"
```

**Pass**: systemd active + all containers Up.
**Fail**: any container Exited or missing.

---

### 2. Startup sequence (last 5 min)

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "5 min ago" --no-pager | tail -30
```

**Pass**: see `"running"` (routd/runed) and `"channel registered"` (adapters) once
per restart. (Post-split there is no `gated` daemon — do NOT grep for it; that
filter returns nothing and looks like a dead stack.)
**Fail**: no log activity at all for > 2 min (routd may be hung or polling stopped).

Red flags: `"error in message loop"`, `"circuit breaker open"`, `"failed to start MCP server"`.

---

### 3. Channel registration

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "10 min ago" --no-pager \
  | grep -E "channel.registered|channel.connected|channel.disconnected"
```

**Pass**: each adapter (`telegram`, `discord`, etc.) shows `"channel registered"` after
each restart.
**Fail**: no `"channel registered"` after a stack restart → adapter lost connection.
Fix: `sudo docker restart arizuko_teled_${INSTANCE}` (or whichever adapter).

---

### 4. Message routing (cursor state)

```bash
# A chat's jid is its sort key; a message's position is its sort key, whose
# first 30 bytes are the instant. `sq` is in "Reaching the store" above.
sq routd.chats "select sk_s as jid, attrs->'agent_cursor'->>'S' as agent_cursor
                from e order by sk_s"

# Per chat: errored messages, and the newest message a PERSON sent.
sq routd.messages "select pk as jid,
    count(*) filter (where attrs->'errored'->>'S' = '1') as errored_msgs,
    max(substr(sk_s,1,30)) filter (where not attrs ? 'is_bot_message'
      and attrs->'sender'->>'S' not like 'arizuko%') as latest_user_msg
  from e group by pk order by pk"
```

**Pass**: `agent_cursor` is NULL or within a few minutes of `latest_user_msg`; `errored_msgs = 0`.
**Fail**: cursor many hours behind → message stuck; `errored_msgs > 0` → those turns erred and won't auto-recover until cleared.

If cursor is stalled, show the pending messages:
```bash
sq routd.messages "select substr(sk_s,1,30) as timestamp,
         attrs->'sender'->>'S' as sender,
         attrs->'is_bot_message'->>'S' as is_bot_message,
         substr(attrs->'content'->>'S',1,120) as content
  from e where pk = '<jid>' order by sk_s desc limit 10"
```

If `errored_msgs > 0` and routd is healthy, clear the errored messages to unblock recovery:
```bash
# The store is the daemons' own, so a delete here is a delete they see. Name the
# chat; never widen it.
sq routd.messages "delete from e where pk = '<jid>' and attrs->'errored'->>'S' = '1'"
sudo systemctl restart arizuko_${INSTANCE}
```

---

### 5. Container lifecycle (last hour)

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep -E "spawning|exited|timeout|circuit.breaker|agent.error" | tail -30
```

**Pass**: containers spawn and exit cleanly (`"container exited"` with `"hadOutput":true`).
**Fail patterns**:
- `"container timed out with no output"` → agent hung (check agent logs below)
- `"container exited","code":1` → crash (check container log)
- `"circuit breaker open"` → 3+ consecutive failures; group stuck until new message

---

### 6. Container logs (per group, last run)

```bash
# Find groups with logs
ls /srv/data/arizuko_${INSTANCE}/groups/

# Check latest log for a group
FOLDER=main
ls -lt /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/logs/ | head -3
tail -30 /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/logs/$(ls -t /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/logs/ | head -1)
```

**Pass**: last log ends with `{"status":"success",...}`.
**Fail**: log ends with error JSON, timeout marker, or empty content.

---

### 7. Task scheduler (timed)

```bash
# Tasks and their next run
sq routd.scheduled_tasks "select sk_s as id,
         attrs->'owner'->>'S' as owner,
         attrs->'cron'->>'S' as cron,
         attrs->'next_run'->>'S' as next_run,
         attrs->'status'->>'S' as status
  from e where attrs->'status'->>'S' = 'active' order by 4 limit 20"

# Recent task fires (last 24h)
# A run log's instant is the lead of its `sort`, so the window is a string
# comparison against the same fixed-width layout every stored instant carries.
sq routd.task_run_logs "select attrs->'task_id'->>'S' as task_id,
         substr(sk_s,1,30) as run_at,
         attrs->'status'->>'S' as status
  from e where substr(sk_s,1,19) > to_char(now() at time zone 'utc' - interval '24 hours',
                                           'YYYY-MM-DD\"T\"HH24:MI:SS')
  order by sk_s desc limit 20"
```

**Pass**: tasks with `next_run` values in the future; recent `task_run_logs` with
`status = 'success'`.
**Fail**: tasks with `next_run` in the past and no recent log → timed daemon stuck.
Check: `sudo docker ps --filter name=arizuko_timed_${INSTANCE}`.

Also check timed logs:
```bash
sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep "timed" | grep -E "error|fired|scheduler" | tail -10
```

---

### 8. MCP sockets

```bash
# Sockets live at ipc/<folder>/gated.sock (the file name is legacy — kept in code
# after the gated daemon was removed; routd now hosts the socket). Nested folders
# nest the dir, so use find.
sudo find /srv/data/arizuko_${INSTANCE}/ipc -name 'gated.sock' 2>/dev/null || \
  echo "No MCP sockets found"
```

**Pass**: socket files present for each active group.
**Fail**: no sockets → IPC server not running; agents will fail on tool calls.
Note: sockets are created when a container starts, cleaned up after.
A missing socket during an active container run is an error.

---

### 9. Auth / proxyd health

```bash
# Proxyd's host port is assigned dynamically by compose — resolve it, don't hardcode.
PORT=$(sudo docker port arizuko_proxyd_${INSTANCE} 8080 2>/dev/null | head -1 | sed 's/.*://')
curl -sf http://localhost:${PORT}/health 2>/dev/null || \
  echo "proxyd not responding on ${PORT}"

# Check proxyd logs for errors
sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep "proxyd" | grep '"status":5' | tail -10
```

**Pass**: `/health` returns `{"ok":true}`; no 5xx in proxyd logs.
**Fail**: proxyd not responding → web UI down; 5xx → upstream error.

---

### 9a. Agent runtime authentication

```bash
# Check presence only; never print credential values.
sudo docker exec arizuko_runed_${INSTANCE} sh -c \
  'env | grep -qE "^(ANTHROPIC_API_KEY|CLAUDE_CODE_OAUTH_TOKEN|OPENAI_API_KEY|CODEX_API_KEY)="'

LOGIN_ERRORS=$(sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep -cF "Not logged in · Please run /login" || true)
echo "agent login errors: $LOGIN_ERRORS"
```

**Pass**: `runed` has at least one model credential and `LOGIN_ERRORS=0`.
**Fail**: no credential means generated `env/runed.env` dropped the operator
model credential; any login error means agent calls are failing before user
work begins.

---

### 10. The store serves what the daemons declare

There is no migration chain and no version row: an entity is a table the owner
daemon asks for at boot, so the check is whether every declared entity is THERE.
A store that answers nothing looks healthy.

```bash
# Every table the server holds, by owner.
sudo docker exec -e PGUSER=arizuko_${INSTANCE} arizuko_extenddb_db_${INSTANCE} \
  psql -d extenddb_catalog -At -c \
  "select split_part(table_name,'.',1) as owner, count(*) from tables group by 1 order by 1;"

# What routd declares, to compare against: store/entities.go EntitiesByOwner,
# plus audit_log and resreg_scopes, which every owner carries.
grep -c '^\t\t[A-Z]' /home/onvos/app/arizuko/store/entities.go
```

**Pass**: every owner a daemon runs for has its set — routd 33, onbod 6,
authd 7, runed 4, and one prefix per adapter that keeps tables.
**Fail**: a missing entity means that owner never booted against this endpoint;
its daemon answers `ResourceNotFoundException` on the first read that needs it.

---

### 11. Episodic memory (diary)

```bash
for g in $(sudo ls /srv/data/arizuko_${INSTANCE}/groups/); do
  d=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/diary/ 2>/dev/null | wc -l)
  latest=$(sudo ls -t /srv/data/arizuko_${INSTANCE}/groups/$g/diary/ 2>/dev/null | head -1)
  entries=0
  if [ -n "$latest" ]; then
    entries=$(sudo grep -c "^## " /srv/data/arizuko_${INSTANCE}/groups/$g/diary/$latest 2>/dev/null)
  fi
  echo "$g: files=$d latest=$latest entries_in_latest=$entries"
done
```

Per group, check:
- **Has diary files** — active groups should have recent entries (within 7 days)
- **Summary maintained** — latest file has YAML `summary:` with current bullet points
- **Entry quality** — `## HH:MM` entries exist, concise, capture decisions not routine ops

```bash
# Read latest diary summary for a group
FOLDER=main
LATEST=$(sudo ls -t /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/diary/ 2>/dev/null | head -1)
sudo head -15 /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/diary/${LATEST}
```

**Pass**: active groups have diary entries from last 7 days with maintained summaries.
**Fail**: active group has no diary, or latest entry is weeks old, or summary is stale/missing.

---

### 12. Knowledge memory (facts)

```bash
CUTOFF=$(date -d "14 days ago" +%Y-%m-%dT%H:%M:%S)
for g in $(sudo ls /srv/data/arizuko_${INSTANCE}/groups/); do
  total=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/facts/ 2>/dev/null | wc -l)
  stale=0; fresh=0
  for f in $(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/facts/ 2>/dev/null); do
    va=$(sudo grep -m1 'verified_at:' /srv/data/arizuko_${INSTANCE}/groups/$g/facts/$f 2>/dev/null \
      | sed 's/.*verified_at:[[:space:]]*//' | tr -d '"')
    if [ -n "$va" ] && [[ "$va" < "$CUTOFF" ]]; then stale=$((stale+1)); else fresh=$((fresh+1)); fi
  done
  [ "$total" -gt 0 ] && echo "$g: total=$total fresh=$fresh stale=$stale"
done
```

Per fact file, check:
- **Has frontmatter** — `path`, `category`, `topic`, `verified_at`, `header`
- **Staleness** — `verified_at` older than 14 days = stale; should be refreshed via `/facts`
- **No hand-written facts** — facts must come from `/facts` skill (researched + verified)

```bash
# Sample a fact file
sudo head -20 /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/facts/$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/facts/ 2>/dev/null | head -1)
```

**Pass**: facts have proper frontmatter, `verified_at` within 14 days, content is researched.
**Fail**: missing frontmatter, all stale, or hand-written content without verification.

---

### 13. User profiles

```bash
for g in $(sudo ls /srv/data/arizuko_${INSTANCE}/groups/); do
  u=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/users/ 2>/dev/null | wc -l)
  [ "$u" -gt 0 ] && echo "$g: $u user profiles"
done
```

Per user file, check:
- **Has frontmatter** — `name`, `first_seen`, `summary`
- **Reflects real interactions** — `## Recent` section with dated entries
- **Not stale** — recent entries if user is still active

```bash
# Read a user profile
sudo cat /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/users/$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/users/ 2>/dev/null | head -1)
```

**Pass**: active groups with multiple users have profile files; content matches interactions.
**Fail**: zero user profiles in a group with regular multi-user traffic.

---

### 14. Conversation archives

```bash
for g in $(sudo ls /srv/data/arizuko_${INSTANCE}/groups/); do
  c=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/conversations/ 2>/dev/null | wc -l)
  [ "$c" -gt 0 ] && echo "$g: $c archived conversations"
done
```

Archives are written by the PreCompact hook when context window fills.
**Pass**: groups with long sessions have conversation archives.
**Fail**: active group with many sessions but zero archives → PreCompact hook may be broken.

---

### 15. Memory coverage (cross-group)

```bash
for g in $(sudo ls /srv/data/arizuko_${INSTANCE}/groups/); do
  d=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/diary/ 2>/dev/null | wc -l)
  f=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/facts/ 2>/dev/null | wc -l)
  u=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/users/ 2>/dev/null | wc -l)
  c=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/conversations/ 2>/dev/null | wc -l)
  echo "$g: diary=$d facts=$f users=$u convos=$c"
done
```

**Pass**: non-infrastructure groups (not `main`, `root`, `share`) have at least diary entries.
**Fail**: active group with zero memory stores → agent never invoked memory skills.

---

### 16. Errors summary (last hour)

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep -E '"level":"ERROR"' | tail -20
```

**Pass**: no ERROR lines, or only expected transient errors.
**Fail**: repeated same error → systematic issue needing investigation.

---

### 17. Skill seeding (per group)

```bash
SOURCE_COUNT=$(ls /home/onvos/app/arizuko/ant/skills/ | wc -l)
# Only check groups registered in DB (skip orphan filesystem dirs like share/)
# The folder IS the sort key; a row's existence is the group being active.
for g in $(sq routd.groups "select sk_s from e"); do
  gdir=$(echo "$g" | tr '/' '-')  # atlas/content → atlas-content (folder path)
  n=$(sudo ls /srv/data/arizuko_${INSTANCE}/groups/$g/.claude/skills/ 2>/dev/null | wc -l)
  echo "$g: $n skills (expected >= $SOURCE_COUNT)"
done
```

**Pass**: every active group has >= source skill count (currently 37).
**Fail**: group has fewer skills → `migrate` hasn't run, or `SetupGroup` missed it.
Extra skills (> source) are fine — groups may have custom skills.

---

### 18. Dispatch discovery (skill descriptions)

```bash
# Run against a group's skills dir — every skill must produce a description
FOLDER=main
for d in /srv/data/arizuko_${INSTANCE}/groups/${FOLDER}/.claude/skills/*/; do
  name=$(basename "$d")
  desc=$(sudo awk '/^description:/{f=1; sub(/^description:[[:space:]]*/,""); print; next} f && /^[^ ]/{exit} f{print}' "$d/SKILL.md" 2>/dev/null | tr '\n' ' ' | sed 's/^[>[:space:]]*//')
  if [ -z "$desc" ]; then
    echo "BROKEN $name — no parseable description"
  fi
done
echo "done"
```

**Pass**: no BROKEN lines — every skill produces a description for dispatch matching.
**Fail**: BROKEN skill → dispatch can't discover it → never matched → dead skill.
Fix: check the skill's SKILL.md frontmatter `description:` field.

---

### 19. Skill consistency (group vs source)

```bash
SOURCE_DIR=/home/onvos/app/arizuko/ant/skills
# The folder IS the sort key; a row's existence is the group being active.
for g in $(sq routd.groups "select sk_s from e"); do
  missing=""
  for s in $(ls $SOURCE_DIR); do
    if ! sudo test -d /srv/data/arizuko_${INSTANCE}/groups/$g/.claude/skills/$s; then
      missing="$missing $s"
    fi
  done
  [ -n "$missing" ] && echo "$g: MISSING$missing"
done
echo "done"
```

**Pass**: no MISSING lines — all source skills present in every group.
**Fail**: skills missing → `/migrate` hasn't synced them, or `SetupGroup` incomplete.
Fix: trigger `/migrate` in the affected group (each group self-migrates), or manually run `SetupGroup` for it.

---

### 20. Solve wiring

```bash
# Check that group CLAUDE.md has the solve instruction (seeded from ant/CLAUDE.md)
# The folder IS the sort key; a row's existence is the group being active.
for g in $(sq routd.groups "select sk_s from e"); do
  has=$(sudo grep -c "solve" /srv/data/arizuko_${INSTANCE}/groups/$g/.claude/CLAUDE.md 2>/dev/null)
  if [ "$has" -lt 1 ]; then
    echo "$g: MISSING solve instruction in CLAUDE.md"
  fi
done
echo "done"

# Also verify runner injects [solve] nudge into prompts (code check)
grep -q '\[solve\]' /home/onvos/app/arizuko/container/runner.go && \
  echo "runner.go: [solve] nudge present" || \
  echo "runner.go: [solve] nudge MISSING"
```

**Pass**: all groups have solve in CLAUDE.md + runner.go injects nudge.
**Fail**: CLAUDE.md missing solve → `ant/CLAUDE.md` not seeded (seedGroupDir broken).
Runner missing nudge → solve never fires → no skill matching → agent runs blind.

---

### 21. Unanswered chats (humans outran the replies)

Check 4 asks whether the cursor MOVED. A chat can stay current and still answer
nothing: an `#observe` route advances the cursor without running a turn, so the
flag stays clear, the breaker stays closed and the census stays empty while a
tenant waits for days (BUGS `L320`, found on krons after three silent days).
This check asks about the OUTCOME instead — the newest human message against
the newest reply.

```bash
sq routd.messages "select pk as chat,
    max(substr(sk_s,1,30)) filter (where not attrs ? 'is_bot_message'
      and attrs->'sender'->>'S' not like 'arizuko%'
      and attrs->'sender'->>'S' not like 'timed%'
      and attrs->'sender'->>'S' <> 'system') as newest_human,
    max(substr(sk_s,1,30)) filter (where attrs->'is_bot_message'->>'S' = '1') as newest_reply
  from e group by pk
  having max(substr(sk_s,1,30)) filter (where not attrs ? 'is_bot_message'
      and attrs->'sender'->>'S' not like 'arizuko%'
      and attrs->'sender'->>'S' not like 'timed%'
      and attrs->'sender'->>'S' <> 'system') > '$(date -u -d '7 days ago' +%Y-%m-%d)'
  order by 2 desc"
```

**Pass**: every chat with a human message in the window has a `newest_reply`
later than its `newest_human`, or later by one round at most.
**Fail**: `newest_reply` is NULL or older than `newest_human` → that chat takes
messages and answers none.

Read the route before calling it a fault. A NULL reply is correct for a folder
nobody talks to, and for the `#observe` half of a route pair when the operator
meant plain messages to be observed. It is a fault when a person writes and
expects an answer:

```bash
# Which rule answers this chat, and which one only observes it.
sq routd.routes "select split_part(sk_s,chr(31),1)||' | '||split_part(sk_s,chr(31),2)
  ||' | '||split_part(sk_s,chr(31),3) from e where sk_s like '%<room-id>%' order by sk_s"
```

A `verb=mention` rule with an `#observe` twin one seq below means plain messages
are observed by design. Adding a `sender=` rule at the same seq answers one
person without opening the chat to everyone.

## Output pattern

After running checks, append findings to `.diary/YYYYMMDD.md`:

```markdown
## Eval — HH:MM UTC

Instance: <name>
Checked: <what was checked>

| Check | Result | Notes |
|-------|--------|-------|
| service health | pass/fail | ... |
| channel registration | pass/fail | ... |
| routing cursors | pass/fail | ... |
| container lifecycle | pass/fail | ... |
| task scheduler | pass/fail | ... |
| mcp sockets | pass/fail | ... |
| auth/proxyd | pass/fail | ... |
| agent runtime auth | pass/fail | ... |
| schema version | pass/fail | ... |
| diary (episodic) | pass/fail | ... |
| facts (knowledge) | pass/fail | ... |
| user profiles | pass/fail | ... |
| conversation archives | pass/fail | ... |
| memory coverage | pass/fail | ... |
| error log | pass/fail | ... |
| skill seeding | pass/fail | ... |
| dispatch discovery | pass/fail | ... |
| skill consistency | pass/fail | ... |
| solve wiring | pass/fail | ... |

**Summary**: <one line>
```

Route every proposed fix to `BUGS.md` per **Method** step 4 (cause → redesign →
sign-off) — never a symptom patch, never applied without sign-off. For a failure
that recurs across runs, record its count trend in the entry so a redesign can
be judged by whether it drives the class to zero.

**Never**: auto-fix, auto-commit.

---

## Known failure modes (from production)

| Symptom | Root cause | Fix |
|---------|-----------|-----|
| Entire stack crash-loops every ~15s | Service in compose references missing binary | Check `docker ps` for short-lived containers; read journalctl for `exec: "<name>": executable file not found` |
| Stalled typing indicator | Stack crashed mid-agent-run; `Typing(false)` never sent to teled | Fix crash loop; typing expires naturally in Telegram |
| Agent cursor stuck, no new container | `SendMessage` to dying container, `pendingMessages` not set | Fixed in d75f8b1 — rebuild + restart routd |
| Channel not registering after a stack restart | Adapter holds stale connection | `sudo docker restart arizuko_teled_${INSTANCE}` |
| Circuit breaker stuck open | 3+ consecutive container failures | Send a new message to the group to reset; check container logs |
| Agent responds "let me fix this now" then stops | Container killed mid-task by 5s idle timer after final output | User must re-send to trigger another run |
| Errored messages on a chat | Container timed out with no output, or stack crashed mid-run | Clear with `DELETE FROM messages WHERE part = '...' AND json_extract(attrs,'$.errored') = '1'` then restart |
| Migration version mismatch | New migration not applied to instance | Run migration manually or `arizuko run` to regenerate compose + restart |
| routd "connecting channels: count=0" | Adapters not yet registered | Wait 10s; if still 0, restart adapters |
| Agent ignores skills, responds generically | Solve not firing: CLAUDE.md not seeded, or nudge missing from runner | Re-seed group via `SetupGroup`; verify `runner.go` has `[solve]` annotation |
| Skill exists but never matched by dispatch | Broken `description:` in SKILL.md frontmatter — awk can't parse it | Fix the YAML frontmatter: `description: >` followed by indented text on next line |
| Every agent says `Not logged in · Please run /login` | Generated `env/runed.env` omitted the operator model credential | Regenerate compose and recreate `runed`; verify check 9a before retrying turns |

---
