---
name: eval
description: Operational health and post-deploy gates for a running arizuko instance — the 20 checks, the error census, and the live-agent `anteval --smoke` gate. NOT for code review (use /code-review), NOT for logging a bug you already found (use /bugs).
when_to_use: /eval, eval krons, eval sloth, check the instance, is the fleet healthy, agent stuck, cursor stalled, circuit breaker open, container timed out, errored messages, no reply from the agent, after a deploy, after a restart, before a tag, post-deploy gates, anteval, anteval --smoke, agent capability eval, error census, skill seeding, dispatch discovery, solve wiring, store serves what the daemons declare
---

# Eval Skill — arizuko

Checks operational health of running arizuko instances.
Run after deploys, on suspicion of a stuck agent, or periodically.
The 20 checks live in `checks.md`; the five post-deploy gates — build, ship,
schema, census, `anteval --smoke` — live in `gates.md`.

## Method: causes, not symptoms

Eval hunts ALL errors and maps each to its CAUSE — not just pass/fail health.
For every ERROR/WARN across journald, `audit_log`, and container logs:

1. **Census** — collect every distinct error with count + first/last seen. A
   rare error still counts; a swallowed one (see check 16) counts double.
2. **Cause** — trace each to its root, not the symptom line: which boundary
   swallowed it, which precondition wasn't gated, which contract drifted.
3. **Redesign** — propose the change that makes the bad state impossible-by-
   construction (`~/.claude/CLAUDE.md` System-change discipline): fail loud to
   the user, retry only transient (remote/DB-busy) errors, amend the ORIGINAL
   mechanism — never a symptom patch, never a parallel path.
4. **Record + sign-off** — write each proposal to `BUGS.md` (`/bugs` format);
   the user signs off BEFORE any ship. NEVER auto-fix or auto-commit.
5. **Track over time** — error counts ARE the metric. A good redesign drives a
   whole class to zero across successive evals; re-run to confirm it's gone.

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

## Reaching the store

Every owner's tables live on the instance's store server — ExtendDB over
PostgreSQL, one per instance (`specs/6/42`). There is no file to open, and the
`store/<owner>/` directories a pre-v0.68.0 instance left behind are FROZEN: a
query against one answers what was true before the flip, which reads like a
healthy instance that stopped working (BUGS `L284` is this class).

Source this once per session. `sq` takes an entity and SQL over the view `e`,
which is `(pk, sk_s, attrs)`:

```bash
INSTANCE=krons
sq() {  # sq <owner.entity> <sql over e(pk, sk_s, attrs)>
  local ent="$1"; shift
  local id
  id=$(sudo docker exec -e PGUSER=arizuko_${INSTANCE} arizuko_extenddb_db_${INSTANCE} \
        psql -d extenddb_catalog -At -c "select table_id from tables where table_name='${ent}';")
  [ -n "$id" ] || { echo "no such entity: ${ent}" >&2; return 1; }
  sudo docker exec -e PGUSER=arizuko_${INSTANCE} arizuko_extenddb_db_${INSTANCE} \
        psql -d extenddb -At -F'|' -c \
        "with e as (select pk, sk_s, item_data as attrs from \"_ddb_${id}\") $*"
}
```

An attribute is a DynamoDB-typed value, so a string field reads
`attrs->'<name>'->>'S'`. `sk_s` is the sort key: for `messages` and `audit_log`
its first 30 bytes are the instant, so a time window is a string comparison.
A flag is `"1"` when set and ABSENT when not — `= '1'` is the test, and there is
no stored zero.

```bash
sq routd.groups   "select count(*) from e"
sq routd.chats    "select sk_s from e where attrs->'errored'->>'S' = '1'"
sq routd.audit_log "select attrs->'outcome'->>'S', count(*) from e
                    where sk_s > '2026-09-15' group by 1 order by 2 desc"
```

## Where the rest lives

ALWAYS read the file the task needs; NEVER work from this page alone.

| Task | Read |
|------|------|
| Health of a running instance — the 20 checks, the report table, the known failure modes | `checks.md` |
| After a deploy or a tag — the five gates, `anteval --smoke` | `gates.md` |
| First `anteval` run on an instance whose eval folder does not exist | `gates.md` § "Bringing `anteval` up on an instance that has never run it" |

**Never**: auto-fix, auto-commit. Every proposed fix goes to `BUGS.md` per
**Method** step 4 and waits for the user.
