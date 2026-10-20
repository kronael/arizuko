# dashd-playwright

End-to-end Playwright coverage for the operator dashboard (`dashd`).
Drives the real HTML routes/groups/memory/activity/status surface — no
mocks, no proxyd in front.

## What it covers

Every `/dash/` surface dashd mounts. One spec per surface:

| spec                     | surface                                                                                     |
| ------------------------ | ------------------------------------------------------------------------------------------- | ----- | ----- | -------- |
| `read-only.spec.ts`      | portal, status, activity, memory (browse + PUT/DELETE `MEMORY.md`)                          |
| `nav.spec.ts`            | the top nav and the portal tiles — that every page is _reachable_, not just that it renders |
| `routes.spec.ts`         | `/dash/routes/` list, add, PATCH, delete, 404                                               |
| `groups.spec.ts`         | list, create, settings render/save/reload, delete                                           |
| `grants.spec.ts`         | per-group grants list, add, revoke                                                          |
| `tools.spec.ts`          | `/dash/groups/{folder}/tools` MCP tool browser                                              |
| `tasks.spec.ts`          | `/dash/tasks/` list, create, partial refresh                                                |
| `invites.spec.ts`        | one-time link on create, ref-only on read, revoke                                           |
| `tokens.spec.ts`         | route-token list + revoke (dashd does not mint)                                             |
| `approvals.spec.ts`      | `/dash/approvals/` HITL queue, verdict forwarding, `?id=` deep link                         |
| `operator-reads.spec.ts` | audit, usage, packages, proactive                                                           |
| `engagement.spec.ts`     | engaged windows + force-disengage                                                           |
| `control-planes.spec.ts` | `/dash/routd                                                                                | runed | authd | proxyd/` |
| `services.spec.ts`       | the services hub and its built/unbuilt tile rules                                           |
| `secrets.spec.ts`        | `/dash/me/secrets` CRUD                                                                     |
| `env.spec.ts`            | `/dash/me/env` model-key CRUD + the allow-list                                              |
| `connections.spec.ts`    | `/dash/me/connections`                                                                      |
| `profile.spec.ts`        | `/dash/profile/`                                                                            |
| `davd.spec.ts`           | the `/dav/{folder}/` links                                                                  |
| `auth.spec.ts`           | unauth 401, signed non-admin 403, signed admin 200/303                                      |

Not covered, and deliberately: `/dash/onbod|timed|webd/` have no handler
(`Built=false` in `dashd/services.go`) — `services.spec.ts` asserts they render
as text, not links.

## How auth works in tests

dashd trusts the `X-User-Sub` header — proxyd is the upstream signer in
production. Tests inject the header directly via Playwright's
`extraHTTPHeaders`. The seed binary writes a single operator grant
(`testadmin` → `admin` on scope `**`); any other sub (e.g. `testuser`)
is signed-in-but-unauthorised.

## Run

```bash
cd tests/dashd-playwright
npm install                                # one-time
npx playwright install --with-deps chromium # one-time
npx playwright test                        # headless
npx playwright test --headed               # debug
```

Or, from the repo root: `make test-dash`.

## Infra

`global-setup.ts` does the heavy lifting per `npx playwright test` run:

1. Builds `./tmp/dashd-seed` (from `seed/main.go`) and `./tmp/dashd-bin`
   if missing — incremental builds are fast.
2. `mkdtemp` a throwaway `DATA_DIR` under `$TMPDIR`.
3. Runs the seed binary: applies all `store/migrations/`, inserts the
   `inbox` group + a sample route + a sample message + a `MEMORY.md`,
   grants `testadmin` operator (`acl.scope = '**'`).
4. Spawns the dashd binary with `DATA_DIR`, `HOST_DATA_DIR`,
   `HOST_APP_DIR=<repo>`, `ARIZUKO_DEV=true`, `DASH_PORT=:<free>`.
5. Polls `/health` until ready, stashes pid+port+dataDir in
   `.test-state.json`.

`global-teardown.ts` reads the state file, SIGTERMs dashd, deletes the
temp `DATA_DIR`, removes the state file.

Tests run serial (`workers: 1`) — they share one dashd + sqlite, and
each writes/cleans its own rows.

## Known limits

- Folder names with `/` (e.g. `solo/inbox`) can't be addressed via the
  `/dash/groups/{folder}/...` handlers because Go's mux path-value
  matches a single segment. The suite uses flat `inbox` for that reason.
- `confirm()` browser dialogs are auto-accepted in delete flows
  (`page.on('dialog', d => d.accept())`).
- **The transit guard is off.** `AUTHD_URL` is left unset on purpose: setting it
  builds dashd's key set, which turns `guard` on, and every request would then
  need a real `service:proxyd` ES256 bearer. So the suite exercises dashd's own
  authz (X-User-Sub / X-User-Groups) but not proxyd's transit proof, and
  `/dash/authd/` is covered on its unreachable path only. Closing this means
  minting a transit bearer in the harness — tracked as BUGS `L98`.
