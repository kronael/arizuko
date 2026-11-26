# Post-deploy gates — arizuko

Run this file after every deploy.

## Post-deploy gates

**Every deploy runs gates 1-4, in order. Gate 5 runs `anteval` against a
throwaway instance only, never against krons or another live instance
(`anteval/README.md`), and only when the owner starts it
by hand.** Gates 1-3 are free and catch everything mechanical; 4 is a
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

**5. `anteval run --smoke` — the live-agent gate, on an instance it creates and
removes.** Nothing above proves an agent can still operate the platform; every
check up to here passes on a fleet whose agents answer nothing (BUGS `L149`
shipped green through all of them). The owner starts it by hand, from the
release checkout:

```bash
make build && (cd anteval && make build)
sudo env PATH="$PWD:$PATH" anteval/anteval run --env /private/anteval.env \
  --cases anteval/cases --sink http://<host name>:9099 --smoke \
  --md report.md --json report.json
```

`anteval/README.md` § Run names every key of the env file. Three facts decide
whether the run measures the release:

- **The store is a test store.** `DYNAMODB_URL` names a server that serves no
  instance, and `EXTENDDB_PASSWORD` is the password of its `admin` user. The run
  enrolls one account there and removes it.
- **The model credential is spent, not isolated.** Name whose quota the run
  uses before it starts. A credential that a live instance shares makes the run
  compete with that instance; stop on the first burst of HTTP 429.
- **The host has room.** A guest and the daemons share the host's CPUs with the
  live instances. Check `free -m` and `uptime` first; the preflight refuses a
  host without memory for the services and one guest.

`--sink` takes a DNS name that reaches this host from a guest; an IP literal is
refused. A run on the Codex backend has no meter, so every case waits its
180 s settle bound after its check: the nine smoke cases took 43 minutes on
2026-11-25.
The exit code is non-zero when a case fails or when the removal fails; the
removal error names the instance for `arizuko rm -i NAME`.

Red flags: `"error in message loop"`, `"container timeout"`, `"circuit
breaker open"`.

Adapter `/health` returns 503 `{status:"disconnected"}` when the platform
side is down even if the process is up (whapd showing QR, an adapter
stream dropped, …). Check on the host:

```bash
sudo curl -s -o /dev/null -w '%{http_code}\n' http://localhost:<port>/health
```
