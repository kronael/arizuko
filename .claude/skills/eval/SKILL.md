# Eval Skill — arizuko

Checks operational health of running arizuko instances.
Run after deploys, on suspicion of a stuck agent, or periodically.

## Usage

```
/eval [instance]           # e.g. /eval krons  or  /eval  (checks all)
/eval <instance> routing   # single criterion
```

## Instances

Discover all: `sudo ls /srv/data/ | grep arizuko_`.
Data dir: `/srv/data/arizuko_<instance>/`.
Groups: `sudo ls /srv/data/arizuko_<instance>/groups/`.

---

## Checks (run in order; each is independent)

### 1. Service health

```bash
INSTANCE=krons
sudo systemctl is-active arizuko_${INSTANCE}
sudo docker ps --filter "name=arizuko_${INSTANCE}" --format "{{.Names}} {{.Status}}"
```

**Pass**: systemd active + all containers Up.
**Fail**: any container Exited or missing.

---

### 2. Startup sequence (last 5 min)

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "5 min ago" --no-pager \
  | grep "gated" | tail -20
```

**Pass**: see `"arizuko running"` and `"channel connected"` (once per restart).
**Fail**: no log activity at all for > 2 min (gated may be hung or polling stopped).

Red flags: `"error in message loop"`, `"circuit breaker open"`, `"failed to start MCP server"`.

---

### 3. Channel registration

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "10 min ago" --no-pager \
  | grep -E "channel.registered|channel.connected|channel.disconnected"
```

**Pass**: each adapter (`telegram`, `discord`, etc.) shows `"channel registered"` after
each gated restart.
**Fail**: no `"channel registered"` after a gated restart → adapter lost connection.
Fix: `sudo docker restart arizuko_teled_${INSTANCE}` (or whichever adapter).

---

### 4. Message routing (cursor state)

```bash
DB=/srv/data/arizuko_${INSTANCE}/store/messages.db

# Agent cursors vs latest messages + errored flag
sudo sqlite3 $DB "
  SELECT c.jid, c.errored, c.agent_cursor,
    (SELECT r.target FROM routes r
     WHERE r.match LIKE '%room=' || substr(c.jid, instr(c.jid, ':') + 1) || '%'
     ORDER BY r.seq LIMIT 1) AS group_folder,
    (SELECT MAX(m.timestamp) FROM messages m WHERE m.chat_jid = c.jid
     AND m.is_bot_message = 0 AND m.sender NOT LIKE 'arizuko%')
    AS latest_user_msg
  FROM chats c
  ORDER BY c.jid;
"
```

**Pass**: `agent_cursor` is NULL or within a few minutes of `latest_user_msg`; `errored = 0`.
**Fail**: cursor many hours behind → message stuck; `errored = 1` → group won't auto-recover.

If cursor is stalled, show the pending messages:
```bash
sudo sqlite3 $DB "
  SELECT timestamp, sender, is_bot_message, substr(content,1,120) as content
  FROM messages WHERE chat_jid = '<jid>'
  ORDER BY timestamp DESC LIMIT 10;
"
```

If `errored = 1` and gated is healthy, clear it to unblock recovery:
```bash
sudo sqlite3 $DB "UPDATE chats SET errored = 0 WHERE jid = '<jid>';"
sudo systemctl restart arizuko_${INSTANCE}
```

---

### 5. Container lifecycle (last hour)

```bash
sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep "gated" \
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
DB=/srv/data/arizuko_${INSTANCE}/store/messages.db

# Tasks and their next run
sudo sqlite3 $DB "
  SELECT id, folder, cron, next_run, status
  FROM scheduled_tasks
  WHERE status = 'active'
  ORDER BY next_run
  LIMIT 20;
"

# Recent task fires (last 24h)
sudo sqlite3 $DB "
  SELECT task_id, run_at, status
  FROM task_run_logs
  WHERE run_at > datetime('now', '-24 hours')
  ORDER BY run_at DESC
  LIMIT 20;
"
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
ls -la /srv/data/arizuko_${INSTANCE}/data/ipc/*/gated.sock 2>/dev/null || \
  echo "No MCP sockets found"
```

**Pass**: socket files present for each active group.
**Fail**: no sockets → IPC server not running; agents will fail on tool calls.
Note: sockets are created when a container starts, cleaned up after.
A missing socket during an active container run is an error.

---

### 9. Auth / proxyd health

```bash
# Proxyd health endpoint (change port as needed)
curl -sf http://localhost:8095/health 2>/dev/null || \
  echo "proxyd not responding on 8095"

# Check proxyd logs for errors
sudo journalctl -u arizuko_${INSTANCE} --since "1 hour ago" --no-pager \
  | grep "proxyd" | grep '"status":5' | tail -10
```

**Pass**: `/health` returns `{"ok":true}`; no 5xx in proxyd logs.
**Fail**: proxyd not responding → web UI down; 5xx → upstream error.

---

### 10. Schema migration version

```bash
DB=/srv/data/arizuko_${INSTANCE}/store/messages.db
sudo sqlite3 $DB "PRAGMA user_version;"

# Expected version (check store/migrations/ for latest)
ls /home/onvos/app/arizuko/store/migrations/ | sort | tail -3
```

**Pass**: DB version ≥ latest migration number in `store/migrations/`.
**Fail**: DB behind → migration not applied; new features may silently not work.

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
| schema version | pass/fail | ... |
| diary (episodic) | pass/fail | ... |
| facts (knowledge) | pass/fail | ... |
| user profiles | pass/fail | ... |
| conversation archives | pass/fail | ... |
| memory coverage | pass/fail | ... |
| error log | pass/fail | ... |

**Summary**: <one line>
```

If a pattern of failures is found across multiple runs, write or update
`.ship/critique-<TOPIC>.md` with:
- What is failing
- How often / under what conditions
- Reproduction steps
- Proposed fix (do NOT apply it — user decides)

**Never**: auto-fix, auto-commit, or create duplicate critique files.

---

## Known failure modes (from production)

| Symptom | Root cause | Fix |
|---------|-----------|-----|
| Entire stack crash-loops every ~15s | Service in compose references missing binary | Check `docker ps` for short-lived containers; read journalctl for `exec: "<name>": executable file not found` |
| Stalled typing indicator | Stack crashed mid-agent-run; `Typing(false)` never sent to teled | Fix crash loop; typing expires naturally in Telegram |
| Agent cursor stuck, no new container | `SendMessage` to dying container, `pendingMessages` not set | Fixed in d75f8b1 — rebuild + restart gated |
| Channel not registering after gated restart | Adapter holds stale connection | `sudo docker restart arizuko_teled_${INSTANCE}` |
| Circuit breaker stuck open | 3+ consecutive container failures | Send a new message to the group to reset; check container logs |
| Agent responds "let me fix this now" then stops | Container killed mid-task by 5s idle timer after final output | User must re-send to trigger another run |
| `errored = 1` on chat | Container timed out with no output, or stack crashed mid-run | Clear with `UPDATE chats SET errored = 0 WHERE jid = '...'` then restart |
| Migration version mismatch | New migration not applied to instance | Run migration manually or `arizuko run` to regenerate compose + restart |
| gated "connecting channels: count=0" | Adapters not yet registered | Wait 10s; if still 0, restart adapters |
