# Post-deploy gates — arizuko

Run this file after every deploy, and read § "Bringing `anteval` up" first on
an instance whose eval folder does not exist yet.

## Post-deploy gates

**Every deploy runs gates 1-4, in order. Gate 5 runs `anteval` against a
throwaway instance only, never against krons or another live instance
(`specs/6/50`), and only when the owner starts it by hand; until that ships,
gate 5 is not run.** Gates 1-3 are free and catch everything mechanical; 4 is a
minute; 5 is the only one that proves a live agent still works, and it is
the only one that costs money (~$5).

```bash
I=<instance>; INSTANCE=$I   # `sq` is in "Reaching the store"

# 1. Build + test, BEFORE the image. Go and TS are separate suites.
make lint && go test ./... -count=1 -short && (cd whapd && bun run test)

# 2. Ship the image, restart, wait for health to settle.
sudo make images && sudo systemctl restart arizuko_$I
until [ "$(sudo docker ps --format '{{.Names}} {{.Status}}' | grep $I | grep -c 'health: starting')" -eq 0 ]; do sleep 10; done
sudo docker ps --format "{{.Names}} {{.Status}}" | grep $I

# 3. The store actually serves what the daemons declare. A store that answers
#    nothing looks healthy.
sudo docker exec -e PGUSER=arizuko_$I arizuko_extenddb_db_$I psql -d extenddb_catalog -At -c \
  "select split_part(table_name,'.',1), count(*) from tables group by 1 order by 1;"
#    routd 33, onbod 6, authd 7, runed 4, one prefix per adapter that keeps tables.

# 4. Error census — the checks above, or their core three:
sudo journalctl -u arizuko_$I --since "15 min ago" --no-pager \
  | grep -E '"level":"ERROR"' | grep -oE '"msg":"[^"]*"' | sort | uniq -c | sort -rn
sq routd.chats "select count(*) from e where attrs->'errored'->>'S' = '1'"   # new ones only
sudo docker exec arizuko_runed_$I sh -c 'env | grep -qE "^(ANTHROPIC_API_KEY|CLAUDE_CODE_OAUTH_TOKEN)="'
```

**5. `anteval --smoke` — the live-agent gate, on a throwaway instance only.** The commands below and the setup section target that instance; `$I` is never a live one. Nothing above proves an
agent can still operate the platform; every check up to here passes on a
fleet whose agents answer nothing (BUGS `L149` shipped green through all of
them). Run it on every release, and after any deploy touching routd, runed,
ipc, ant or an adapter:

```bash
sudo ./arizuko token -i $I issue bearer eval \
  --scope messages:write,messages:read,cost:read --ttl 2h    # token: line
GW=$(sudo docker network inspect arizuko_${I}_default \
  --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}')      # NOT a hardcoded IP
cd anteval && ./anteval run https://<host> --api http://localhost:49201 \
  --token "$TOK" --chat web:eval --folder eval \
  --sink-addr $GW:9099 --sink http://$GW:9099 --smoke
```

**Wait for the migration storm before you run it.** A release bumps
`MIGRATION_VERSION`, and routd then queues one `auto-migrate-<folder>` turn
for every folder behind it — 55 of them on krons for v0.69.0. Those turns
compete for the same run slots, so the eval measures the queue, not the
release. Measured on 2026-09-19: the same build scored **5/9 during the storm
and 8/9 after it drained**, and every one of the three failures was the agent
answering correctly but past the harness's wall — `rest-roundtrip` replied 5
minutes after a 94s cap. Drain first:

```bash
# A turn is a KVM GUEST, not a container (spec 6/41), so counting containers here
# answers 0 forever and the wait returns at once — the storm then runs under the
# eval. Count the guests, and count them by process NAME, `qemu-system-x86`:
#
#   - the name is TRUNCATED to 15 characters in `comm`, so `qemu-system-x86_64`
#     matches nothing and `pgrep -x` says so out loud;
#   - matching the name and not the command line is what keeps the check off ITSELF:
#     `pgrep -f qemu-system-x86_64` matches the shell running it and never reaches 0
#     (BUGS reference `pgrep_self_match`).
#
# Measured 2026-09-26 with one guest live: `pgrep -c qemu-system-x86` is 1 and
# `pgrep -fc qemu-system-x86_64` is 2. Host-wide is the right scope — one host runs
# both instances and they share the run slots the eval is measuring.
until [ "$(pgrep -c qemu-system-x86 || true)" -eq 0 ]; do sleep 30; done
```

Then check no `/migrate` turn has arrived for five minutes. A run started
before that is not a gate, and reading it as one blames the release for the
deploy.

**Capture the report to a file, not through a pipe.** `anteval | tail` loses
the whole table when the shell that owns the pipe goes away, and the run still
exits 0 — you get a green exit and no tally. Redirect with `> file 2>&1`, and
find the process by `ss -ltnp | grep 9099` rather than `pgrep -f`, which
matches the shell running it. The sink port stays bound while a previous run
lives, so a second run panics with `address already in use`.

Two things that make this fail for the wrong reason. `--cases` is
relative, so run it from `anteval/` — from the repo root it loads nothing
and reports "no cases match selectors", which reads like a selector typo.
And the sink must be the instance's own default-network gateway: the
per-folder networks are `internal=true`, so an agent reaches the host
bridge but nothing beyond it, and a stale IP makes 8 of the 9 cases burn
their full wall cap for nothing.

Red flags: `"error in message loop"`, `"container timeout"`, `"circuit
breaker open"`.

Adapter `/health` returns 503 `{status:"disconnected"}` when the platform
side is down even if the process is up (whapd showing QR, an adapter
stream dropped, …). Check on the host:

```bash
sudo curl -s -o /dev/null -w '%{http_code}\n' http://localhost:<port>/health
```

## Bringing `anteval` up on an instance that has never run it

Five preconditions. Four of them fail silently, and each one costs a whole
run, because a case that cannot pass burns its full `max_wall_ms`. Measured on
sloth 2026-09-19, where all five were missing.

**1. The operator CLI must reach the store from the host.** `env/operator.env`
carries the HOST address, never the compose service name, and the second
instance on a host publishes a different port:

```bash
I=<instance>
sudo docker port arizuko_extenddb_$I 18443            # e.g. 127.0.0.1:18444
sudo grep DYNAMODB_URL /srv/data/arizuko_$I/env/operator.env
```

`https://extenddb:18443` here kills every `arizuko <cmd> $I` with `lookup
extenddb … server misbehaving`. Write the published port instead, and set mode
0600 — the file holds the store account's secret key (BUGS `L311`).

**2. The eval folder is a group, never a `mkdir`.**

```bash
sudo ./arizuko group -i $I add web:eval eval
```

**3. Sixteen grants.** The `role:member` floor is messaging only, so without
them the run measures the grant table and not the agent:

```bash
for spec in "eval egress" "eval web:publish" "eval/** mcp:add_acl" \
  "eval mcp:delegate_group" "eval/** mcp:delegate_group" \
  "eval mcp:issue_chat_link" "eval/** mcp:issue_chat_link" \
  "eval mcp:issue_chat_token" "eval/** mcp:issue_chat_token" \
  "eval mcp:issue_webhook" "eval/** mcp:issue_webhook" \
  "eval mcp:register_group" "eval/** mcp:register_group" \
  "eval/** mcp:remove_acl" "eval mcp:web_route_set" "eval/** mcp:web_route_set"; do
  set -- $spec; sudo ./arizuko group -i $I grant folder:eval "$1" "$2"
done
sudo ./arizuko group -i $I grants folder:eval
```

Read the grants back with the principal `folder:eval`. `grants eval` prints
"no grants" for a folder that holds all sixteen.

**4. Egress `*`.** The egress proxy matches the allowlist by name and the sink is an
IP, so only a folder `*` rule covers it:

```bash
sudo ./arizuko network -i $I allow eval '*'
```

**5. Prove the sink from the folder's own network, before the run.** Five
seconds, and it replaces the failure where eight of nine cases burn their cap:

```bash
GW=$(sudo docker network inspect arizuko_${I}_default \
     --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}')
sudo docker run --rm --network arizuko_${I}_eval curlimages/curl:latest \
  -s -o /dev/null -w '%{http_code}\n' -m 5 http://$GW:9099/
```

Any HTTP code proves reach; a timeout means the address is wrong. The agent
container sits on `arizuko_<inst>_eval` alone and that network is
`internal=true`, yet the instance's default gateway answers it — measured on
sloth 2026-09-19, `192.168.240.1` from `10.99.16.0/24`. Run the probe while
the sink is bound.

**Then wait, and do not say hello.** Creating the group seeds five
`/compact-memories` tasks that fire at once, about 35 s apart, and every one
of those turns persists a ROOT session (BUGS `L312`). `anteval` refuses to
inject against a root session and aborts the run with `root session <uuid>
survived /new for 1m30s`. A single `arizuko send -i $I eval "hello"` probe leaves
the same root session and costs the same abort. Wait for both:

```bash
# Guests, not containers, and by TRUNCATED process name — see the drain wait above.
until [ "$(pgrep -c qemu-system-x86 || true)" -eq 0 ]; do sleep 20; done
curl -s -H "Authorization: Bearer $TOK" \
  "http://localhost:<routd-port>/v1/topic_sessions?folder=eval&topic="
```

`{"session_id":""}` is the green light.
