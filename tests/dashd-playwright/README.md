# dashd-playwright

End-to-end Playwright coverage for the operator dashboard (`dashd`).
Drives the real HTML routes/groups/memory/activity/status surface — no
mocks, no proxyd in front.

## What it covers

- `routes.spec.ts` — `/dash/routes/` list, add via form, PATCH via JSON
  API, delete via inline form, 404 on unknown id.
- `groups.spec.ts` — list, create-via-form, settings render + save +
  reload, delete via confirm form.
- `read-only.spec.ts` — portal, status, activity, memory (browse and
  PUT/DELETE the `MEMORY.md` JSON endpoint).
- `auth.spec.ts` — unauth → 401, signed non-admin → 403, signed admin
  → 200/303.

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
  Fixing this in dashd is out-of-scope for the test task; track as a
  bug in `bugs.md` if it bites.
- `confirm()` browser dialogs are auto-accepted in delete flows
  (`page.on('dialog', d => d.accept())`).
