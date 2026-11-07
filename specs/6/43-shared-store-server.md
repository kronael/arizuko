---
status: partial
depends:
  [
    42-dynamo-over-postgres,
    37-store-model,
    40-store-surface,
    ../5/14-credentials,
    ../5/33-paths-roles,
  ]
---

# specs/6/43 — the external shared server is the only store

Every arizuko deployment gets its tables from an external shared ExtendDB server over PostgreSQL.
Each instance is one account on that server.
The server runs outside every instance's Compose project and systemd unit, including when it shares the host.

## Private provisioning duplicates the shared server

At `bb3c3f39a37291bc86413a4cba1619ea8223fcb7`, `store enroll` provisions the shared account, policies and keys.
`INSTALL.md:186-207` also builds a private PostgreSQL pair, certificate and account for each instance.
`template/services/extenddb/arizuko.yml:17-27` supplies the private URL and published port.
These paths make the shared server optional.
Delete private provisioning and require enrollment before the first `run` or `generate`.
Keep `status: partial` until those deletions and setup checks ship.

The owner reports that sloth and krons moved with `store enroll` on 2026-11-06.
That report is not a fresh inspection of live policies or data.

## One endpoint serves the host and containers

```text
┌─────────────────────┐                     ┌─────────────────────┐
│ CLI and daemons     │ endpoint (changed)  │ external ExtendDB   │
│ (changed)           ├────────────────────►│ account per instance│
└─────────────────────┘                     └──────────┬──────────┘
                                                       │
┌─────────────────────┐                     ┌──────────▼──────────┐
│ private store pair  │                     │ PostgreSQL          │
│ (removed)           │                     │ catalog and data DBs│
└─────────────────────┘                     └─────────────────────┘
```

The server owns its databases, catalog settings, TLS certificate and administrator credential.
Instances hold no PostgreSQL credentials.
Keep the external provisioning procedure in `INSTALL.md` under "Share one store server between instances".
It configures the catalog connection and the `encryption_key`, `data_database_name`,
`data_database_connection_string` and `default_account_id` settings.
The server's backup covers both databases and its configuration.
An instance archive holds files and credentials, never the server's rows.

The following map names the deletion boundary. LOC estimates count source lines, including comments, at the baseline commit above.
Documentation estimates count the private instructions to remove or replace, not entire files.

```yaml
- path: template/services/extenddb/arizuko.yml (removed)
  job: delete the private package manifest, 27 LOC
  names: [extenddb, STORE_DB_PASSWORD, STORE_APP_PASSWORD, STORE_HOST_PORT]
  never: supply an instance store URL or PostgreSQL credentials
- path: template/services/extenddb/extenddb.yml (removed)
  job: delete both private services and their mounts and published port, 83 LOC
  names: [extenddb, extenddb_db]
  never: install a store through the instance catalog
- path: template/env.example (changed)
  job: replace the STORE_HOST_PORT comment with required external URL, CA and region, about 3 LOC removed
  names: [DYNAMODB_URL, STORE_CA, AWS_REGION]
  never: publish an instance store port
- path: compose/compose.go (changed)
  job: delete host-port synthesis, about 45 LOC; require external configuration before generating runtime output
  names:
    [
      Generate,
      writeOperatorEnv,
      storeHostPortKey,
      WriteOperatorEnv,
      mergeStoreCredentials,
      warnPartialEnrolment,
    ]
  never: turn an in-network URL into loopback or accept missing store configuration
- path: compose/store_env_test.go (changed)
  job: replace private URL and port tests, about 140 LOC; retain key preservation and shared-endpoint cleanup tests
  names:
    [
      TestOperatorEnvFollowsAMovedStorePort,
      TestOperatorEnvReplacesTheInNetworkName,
      TestOperatorEnvFollowsThePublishedPort,
      TestStoreFragmentPublishesTheDeclaredPort,
    ]
  never: require a private fragment or a synthesized address
- path: compose/swallow_test.go (changed)
  job: replace the absent-store success assertion with configuration refusal, about 15 LOC
  names: [TestWriteOperatorEnvSkipsWhenNoStoreServer]
  never: treat missing runtime configuration as a deployment without tables
- path: cmd/arizuko/main.go (changed)
  job: require enrollment for run and generate; replace create's next-command hint, about 5 LOC replaced
  names: [cmdCreate, cmdRun, generateCompose]
  never: provision or start a store server
- path: cmd/arizuko/create_target_test.go (changed)
  job: keep file creation and owner-boot assertions; correct the private-volume explanation, about 5 LOC replaced
  names: [TestAFreshInstanceIsBornWithMain]
  never: require a private database for create
- path: cmd/arizuko/sync_test.go (changed)
  job: replace ExtendDB fixtures with a test-local service and dependency, about 15 LOC replaced
  names: [TestPackageRefusals, TestPackageServiceContainment]
  never: lose the collision, forced replacement or service-containment assertions
- path: INSTALL.md (changed)
  job: replace private bootstrap, unit, backup and restore instructions, about 100 LOC
  names:
    [
      'bootstrap lines 186-207 and 221-223',
      'The store server',
      'The per-daemon store keys',
      'Share one store server between instances',
      'Move an instance onto the shared store server',
    ]
  never: offer private init, catalog:extenddb or a second setup path
- path: cmd/arizuko/README.md (changed)
  job: correct create's no-server premise and require configure, enroll, run, about 3 long lines replaced
  names: [create, generate, run, 'store enroll']
  never: remove the enrollment or snapshot-copy reference
- path: ARCHITECTURE.md (changed)
  job: qualify Data ownership as an external shared server, about 2 LOC replaced
  names: ['Data ownership']
  never: imply one server process per instance; no private provisioning text was found here
- path: CLAUDE.md (changed)
  job: delete the STORE_HOST_PORT example, about 3 LOC
  names: ['NEVER hand-edit an installed']
  never: teach a deleted setting
- path: template/web/pub/arizuko/howto/index.html (changed)
  job: replace private bootstrap and store directory claims, about 40 LOC
  names: ['Every daemon keeps its tables on one store server']
  never: install catalog:extenddb or retain env/store-admin.env
- path: template/web/pub/arizuko/index.html (changed)
  job: delete extenddb and extenddb_db from the instance tree, about 3 LOC
  never: put shared PostgreSQL data inside the instance
- path: template/web/pub/arizuko/concepts/store.html (changed)
  job: replace the private PostgreSQL tar claim, about 2 LOC
  names: ['A backup is a tar of the stopped instance']
  never: claim an instance tar backs up shared rows
- path: template/web/pub/arizuko/reference/env.html (changed)
  job: delete private passwords and URL defaults; replace manual key minting with enroll, about 20 LOC
  names: [STORE_DB_PASSWORD, STORE_APP_PASSWORD, DYNAMODB_URL, STORE_CA]
  never: declare a private URL default
- path: template/web/pub/arizuko/howto/operate.html (changed)
  job: delete STORE_HOST_PORT advice and replace manual key setup with enroll, about 3 LOC
  never: teach a deleted setting
- path: template/web/pub/arizuko/components/index.html (changed)
  job: replace the claim that the extenddb package adds the server, about 1 LOC
  never: offer private provisioning
- path: template/web/pub/arizuko/components/authd.html (changed)
  job: replace the in-network DYNAMODB_URL example, about 1 LOC
  never: require an instance store service
- path: template/web/pub/arizuko/components/gitd.html (changed)
  job: replace the in-network DYNAMODB_URL example, about 1 LOC
  never: require an instance store service
- path: template/web/pub/arizuko/components/slakd.html (changed)
  job: replace the in-network DYNAMODB_URL example, about 1 LOC
  never: require an instance store service
- path: template/web/pub/arizuko/howto/kubernetes.html (changed)
  job: replace private-pair and manual-init instructions with enrollment, about 8 LOC
  never: offer another store provisioning path
- path: deploy/helm/arizuko/README.md (changed)
  job: replace private-pair and manual-key setup with the enrolled account, about 15 LOC
  never: install a store server in the chart
- path: deploy/helm/arizuko/values.yaml (changed)
  job: replace private-pair and init comments, about 8 LOC
  names: [store.url, store.ca, secrets.daemons]
  never: add a second enrollment format
- path: deploy/helm/arizuko/templates/_helpers.tpl (changed)
  job: replace the extenddb-init credential hint, about 1 LOC
  never: mint keys
- path: deploy/helm/arizuko/templates/NOTES.txt (changed)
  job: replace the extenddb-init instruction, about 2 LOC
  never: require private provisioning
```

Keep `cmd/arizuko/store.go` and `cmd/arizuko/extenddb.go`, including `storeEnroll`, `policy`, `newManager` and the snapshot copy.
Keep `store.StoreReach` as the only policy declaration.
Keep `compose.WriteStoreKey`, `mergeStoreCredentials` and `operatorEnvPath` for scoped credentials.
Keep `WriteOperatorEnv`, `dropGeneratedStoreLine`, `storeLineAt`, `isGeneratedStoreLine` and `chmodOperatorEnv` for stale-address cleanup during a move.
`storeService` remains only as the old address recognized by that cleanup.
Delete `storeHostPortKey` and the in-network branch of `writeOperatorEnv` that constructs and writes `onHost`.
The host and containers read the same URL and CA from `.env`.
Refuse other differing URL or CA overrides in `env/operator.env`; name the file for operator correction.
The move procedure removes any stale `STORE_CA` override from `env/operator.env` explicitly.

Keep `cmdCreate`'s `store/` directory, `chownStore` and their ownership test.
`template/services/teled/teled.yml:11`, `template/services/slakd/slakd.yml:12` and `template/services/whapd/whapd.yml:12-13` use that tree for adapter files.
Keep the generic package installer, containment rules and Compose include renderer.
Keep dyntest and `Makefile`'s test endpoint: neither provisions a deployment.
Keep historical changelog entries.

## Fresh setup requires enrollment

```text
create ──► URL and CA (changed) ──► store enroll ──► owner boot ──► run
```

`create` writes instance files without contacting the store.
The operator sets `DYNAMODB_URL` to the external HTTPS endpoint, `STORE_CA` to its base64 PEM certificate, and `AWS_REGION` in `.env`.
The endpoint must be reachable from the host and containers and covered by the certificate's SAN.
The operator supplies the server administrator password only to enrollment through `EXTENDDB_PASSWORD`.
The instance never keeps that password or `env/store-admin.env`.
Store settings and access keys remain infrastructure credentials under `5/14-credentials`.

Current private bootstrap, `INSTALL.md:186-195`, abridged:

```bash
store_db_password=$(openssl rand -hex 24); store_app_password=$(openssl rand -hex 24)
# .env and compose.env receive private URL, port and PostgreSQL passwords.
# docker compose starts extenddb_db, then runs extenddb init.
```

Proposed setup after `arizuko create -i <name>`:

```bash
# Set DYNAMODB_URL, STORE_CA and AWS_REGION in the instance .env.
sudo --preserve-env=EXTENDDB_PASSWORD ./arizuko store enroll -i <name>
```

Keep the existing owner boot before the first package operation: `generateCompose` calls `syncProxydRoutes`, which needs the owner's tables.
Use the external URL and CA for those owner processes too.
Then install packages, generate and run using the same enrolled credentials.
`Generate` must refuse a missing or invalid HTTPS URL, missing or invalid CA, or incomplete required key pairs before runtime output.
Check keys for installed store users and the operator through `StoreReach`, without minting keys in the generator.
Replace `warnPartialEnrolment`'s warning-only success for those users with that refusal.
`DecodeCA` accepts an empty value today; retain that general driver behavior and require the CA at the deployment boundary.
Do not add flags, fallback endpoints, a private package or another bootstrap command.
Kubernetes consumes the same URL, CA and enrolled pairs through the chart's existing values.

### The unit of an instance on the shared store server

Current `INSTALL.md:944-946`, abridged:

```ini
ExecStartPre=docker compose ... down --remove-orphans
ExecStartPre=docker compose ... up -d --wait extenddb_db extenddb
ExecStartPre=docker run ... --network arizuko_<name>_default -e DYNAMODB_URL=https://extenddb:18443 ... arizuko generate -i <name>
```

Proposed:

```ini
ExecStartPre=docker compose ... down --remove-orphans
ExecStartPre=docker run ... arizuko generate -i <name>
```

Keep the release image, data and Docker socket mounts, foreground Compose start, stop and restart policy.
Delete the private-store start line and the generator's `--network` and `-e DYNAMODB_URL` options.
The generator reads the enrolled configuration without an instance network.
Delete the shared-store exception paragraph at `INSTALL.md:968-971`: that unit is the only unit.
Backup and stopped-apply instructions must leave the external server running.

### The move onto the shared store server

Keep `arizuko store enroll -i <name> <snapshot>` and the corresponding `INSTALL.md` procedure.
The snapshot holds the source `.env` and `env/`; enrollment reads it without changing it.
The source operator pair needs `ListTables`, `DescribeTable` and `Scan` across the source account.

1. Stop the instance and snapshot its configuration with private permissions.
   Keep its source store reachable until copying completes.
2. Set the destination URL and CA in `.env`.
   Remove the old CA override in `env/operator.env`.
3. Run enrollment with the snapshot and the destination administrator password.
   Require every table to report equal counts and a second run to keep valid keys.
4. Detach the old fragment and replace the unit with the unit above.
   A catalog symlink can require explicit detachment when `package rm` refuses it.
   Run `generate` before the next unit `down`, then reload the unit.
5. Stop the old store containers and start the instance.
   Verify daemon health, existing dashboard login and an adapter reply.
   Keep the old store data.

Repeat copying only before the instance writes to the destination: conditional puts preserve existing items but can restore a deleted destination item.
Keep the copy's paged inventory, schema/index/stream refusals, 500-item scans, conditional puts and equal-count check.
A dry run on an isolated copy must pass before an operator moves another deployment.

## Contract

Baseline: `bb3c3f39a37291bc86413a4cba1619ea8223fcb7`.
The private package is 110 LOC across two files.
`StoreReach` has 15 rows, 13 nonempty; `enroll` skips `timed` and `ttsd`.
`policy` at `cmd/arizuko/store.go:125-137` writes `dynamodb:*` only on `arn:aws:dynamodb:*:*:table/<prefix>.*`.
`enroll` at `cmd/arizuko/store.go:214-216` writes that policy through `manager.putPolicy` for each row, including kept keys.
`TestPolicyDocument` checks the literal bounded document; `TestEnrollMintsEveryRowAndConverges` reads each stored policy back.
L318 closes for a clean enrolled account, including the two moves the owner reports.
Enrollment replaces only `tables`; unrelated user/group policies can still widen access.
Live enforcement and extra policies were not inspected here.

L674 does **not** disappear as a general defect.
Removing the private package removes its trigger from fresh setups, but another deleted fragment can still break a stale Compose include.
The move's explicit regeneration handles the store removal; generic include handling stays outside this deletion.

The coding worker must prove these boundaries:

- Remove the private catalog directory: package listing and fresh setup still work without any store service or published store port.
- Remove URL, CA or a required key pair separately: generation refuses before starting Compose and names the missing setting or file.
- Enroll twice against an isolated external server: the second run mints nothing and every policy matches `StoreReach`.
- Copy from a snapshot, regenerate and start with the source stopped: counts match and source data stays untouched.
- Delete the private URL/port tests, retain their shared credential assertions, and replace the ExtendDB package fixtures without weakening containment tests.

The spec-only change runs `go test ./tests/specrefs/... ./tests/bugsindex/... -count=1` and `make lint`.
Implementation also needs the affected compose, CLI and enrollment suites against an isolated endpoint.
