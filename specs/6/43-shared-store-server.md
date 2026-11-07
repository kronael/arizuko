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

External enrollment supplies each instance's account, policies and keys.
Generation must require enrollment before it writes runtime output.
Private service manifests, host-port synthesis and the split-address branch
are absent. `compose/compose_test.go` covers the explicit region requirement;
`cmd/arizuko/store_test.go` covers enrollment and snapshot copying.
Setup, systemd, backup and Kubernetes instructions use the external server
and scoped enrollment. Generation refuses invalid configuration before output.
Host and snapshot readers refuse differing operator endpoint or CA overrides.

## Remaining

- Exercise owner-readable enrollment, a second enrollment without new keys, snapshot copy and stopped-source startup on an isolated external server (`cmd/arizuko/store_test.go`, `cmd/arizuko/create_target_test.go`).
- Prove enrolled daemon policy enforcement before closing the shared L318 gate (`cmd/arizuko/store_test.go`, `specs/6/42-dynamo-over-postgres.md`).

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

The following map names the deletion boundary.

```yaml
- path: template/services/extenddb/arizuko.yml (removed)
  job: delete the private package manifest
  names: [extenddb, STORE_DB_PASSWORD, STORE_APP_PASSWORD, STORE_HOST_PORT]
- path: template/services/extenddb/extenddb.yml (removed)
  job: delete both private services and their mounts and published port
  names: [extenddb, extenddb_db]
- path: template/env.example (changed)
  job: replace the STORE_HOST_PORT comment with required external URL, CA and region
  names: [DYNAMODB_URL, STORE_CA, AWS_REGION]
- path: compose/compose.go (changed)
  job: delete host-port synthesis; require external configuration before generating runtime output
  names:
    [
      Generate,
      ValidateStore,
      writeOperatorEnv,
      storeHostPortKey,
      WriteOperatorEnv,
      mergeStoreCredentials,
      warnPartialEnrolment,
    ]
- path: compose/store_env_test.go (changed)
  job: replace private URL and port tests; retain scoped key preservation and configuration refusal tests
  names:
    [
      TestStoreRefusal,
      TestInstalledAdapterRequiresCredential,
      TestStoreEndpointReachesEveryDaemon,
      TestEachDaemonKeepsItsOwnStoreKey,
    ]
- path: compose/swallow_test.go (changed)
  job: remove private operator-address cleanup tests
  names: [TestWriteOperatorEnvRefusesAnUnparseableStoreURL]
- path: core/config.go (changed)
  job: validate operator endpoint overrides before merging scoped credentials from instance files
  names: [LoadInstanceEnv, ReadInstanceEnv]
- path: core/config_test.go (changed)
  job: check the shared endpoint and scoped operator credential
  names:
    [
      TestSharedStoreAddressAndOperatorCredential,
      TestOperatorCredentialLoadsFromItsOwnFile,
    ]
- path: core/instance_env_test.go (changed)
  job: refuse differing operator URL and CA before changing the process environment
  names: [TestOperatorCannotRedirectStore]
- path: tests/webdocs/components_test.go (changed)
  job: delete the extenddb exception at line 25 when the private package goes
  names: [TestEveryServiceKindHasAComponentPage]
- path: specs/6/42-dynamo-over-postgres.md (changed)
  job: replace the private-compose and instance-database backup claims in section 7 with the external topology
- path: cmd/arizuko/store.go (changed)
  job: read the merged instance configuration through core.ReadInstanceEnv
  names: [view]
- path: cmd/arizuko/main.go (changed)
  job: validate enrollment before relink, docs and runtime output; replace create's next-command hint
  names: [cmdCreate, cmdRun, generateCompose]
- path: cmd/arizuko/create_target_test.go (changed)
  job: keep file creation and owner-boot assertions; correct the private-volume explanation
  names: [TestAFreshInstanceIsBornWithMain]
- path: cmd/arizuko/sync_test.go (changed)
  job: replace ExtendDB fixtures; retain collision, forced replacement and service-containment assertions
  names: [TestPackageRefusals, TestPackageServiceContainment]
- path: INSTALL.md (changed)
  job: replace private bootstrap, unit, backup and restore instructions
  names:
    [
      'bootstrap lines 186-207 and 221-223',
      'The store server',
      'The per-daemon store keys',
      'Share one store server between instances',
      'Move an instance onto the shared store server',
    ]
- path: cmd/arizuko/README.md (changed)
  job: correct create's no-server premise and require configure, enroll, run
  names: [create, generate, run, 'store enroll']
- path: ARCHITECTURE.md (changed)
  job: qualify Data ownership as an external shared server
  names: ['Data ownership']
- path: CLAUDE.md (changed)
  job: delete the STORE_HOST_PORT example
  names: ['NEVER hand-edit an installed']
- path: template/web/pub/arizuko/howto/index.html (changed)
  job: replace private bootstrap and store directory claims
  names: ['Every daemon keeps its tables on one store server']
- path: template/web/pub/arizuko/index.html (changed)
  job: delete extenddb and extenddb_db from the instance tree
- path: template/web/pub/arizuko/concepts/store.html (changed)
  job: replace the private PostgreSQL tar claim
  names: ['A backup is a tar of the stopped instance']
- path: template/web/pub/arizuko/reference/env.html (changed)
  job: delete private passwords and URL defaults; replace manual key minting with enroll
  names: [STORE_DB_PASSWORD, STORE_APP_PASSWORD, DYNAMODB_URL, STORE_CA]
- path: template/web/pub/arizuko/howto/operate.html (changed)
  job: delete STORE_HOST_PORT advice and replace manual key setup with enroll
- path: template/web/pub/arizuko/components/index.html (changed)
  job: replace the claim that the extenddb package adds the server
- path: template/web/pub/arizuko/components/authd.html (changed)
  job: replace the in-network DYNAMODB_URL example
- path: template/web/pub/arizuko/components/gitd.html (changed)
  job: replace the in-network DYNAMODB_URL example
- path: template/web/pub/arizuko/components/slakd.html (changed)
  job: replace the in-network DYNAMODB_URL example
- path: template/web/pub/arizuko/howto/kubernetes.html (changed)
  job: replace private-pair and manual-init instructions with enrollment
- path: deploy/helm/arizuko/README.md (changed)
  job: replace private-pair and manual-key setup with the enrolled account
- path: deploy/helm/arizuko/values.yaml (changed)
  job: replace private-pair and init comments
  names: [store.url, store.ca, secrets.daemons]
- path: deploy/helm/arizuko/templates/_helpers.tpl (changed)
  job: replace the extenddb-init credential hint
- path: deploy/helm/arizuko/templates/NOTES.txt (changed)
  job: replace the extenddb-init instruction
```

Keep `cmd/arizuko/store.go` and `cmd/arizuko/extenddb.go`, including `storeEnroll`, `policy`, `newManager` and the snapshot copy.
Keep `store.StoreReach` as the only policy declaration.
Keep `compose.WriteStoreKey` and `mergeStoreCredentials` for scoped credentials.
Delete `WriteOperatorEnv`, `dropGeneratedStoreLine`, `storeLineAt`, `isGeneratedStoreLine`, `chmodOperatorEnv` and `storeService`.
Delete `storeHostPortKey` and private address synthesis.
The generator never repairs or strips operator address settings silently.
The host and containers read the same URL and CA from `.env`.
Refuse other differing URL or CA overrides in `env/operator.env`; name the file for operator correction.
The operator removes differing URL and CA overrides from `env/operator.env` explicitly.

Keep `cmdCreate`'s `store/` directory, `chownStore` and their ownership test.
`template/services/teled/teled.yml:11`, `template/services/slakd/slakd.yml:12` and `template/services/whapd/whapd.yml:12-13` use that tree for adapter files.
Keep the generic package installer, containment rules and Compose include renderer.
Keep dyntest and `Makefile`'s test endpoint: neither provisions a deployment.
Keep historical changelog entries.

## Fresh setup requires enrollment

```text
create ──► URL, CA, region (changed) ──► store enroll ──► owner boot ──► run
```

`create` writes instance files without contacting the store.
The operator sets `DYNAMODB_URL` to the external HTTPS endpoint, `STORE_CA` to its base64 PEM certificate, and `AWS_REGION` in `.env`.
The endpoint must be reachable from the host and containers and covered by the certificate's SAN.
The operator supplies the server administrator password only to enrollment through `EXTENDDB_PASSWORD`.
The instance never keeps that password or `env/store-admin.env`.
Store settings and access keys remain infrastructure credentials under `5/14-credentials`.

Run enrollment as the data directory owner, UID 1000 for the fresh bootstrap in `INSTALL.md:148-155`.
`compose/manifest.go:159-183` writes mode-0600 files owned by the enrollment process.
Use elevated enrollment only for root-owned deployments.

Fresh setup after `arizuko create -i <name>`:

```bash
EXTENDDB_PASSWORD='<admin-password>' ./arizuko store enroll -i <name>
```

Boot the owners before the first package operation: `generateCompose` calls `syncProxydRoutes`, which needs the owner's tables.
Use the external URL and CA for those owner processes too.
Then install packages, generate and run using the same enrolled credentials.
`Generate` must refuse a missing or invalid HTTPS URL, missing or invalid CA, missing `AWS_REGION`, or incomplete required key pairs before runtime output.
`compose.ValidateStore` checks the endpoint, region and required scoped pairs.
Both `Generate` and CLI `generateCompose` call it before output, including relink and web docs.
`core.ReadInstanceEnv` validates operator endpoint overrides before merging current instance files.
Enrollment and snapshot views use that same reader.
Check keys for installed store users and the operator through `StoreReach`, without minting keys in the generator.
Replace `warnPartialEnrolment`'s warning-only success for those users with that refusal.
`DecodeCA` accepts an empty value today; retain that general driver behavior and require the CA at the deployment boundary.
`dynstore.Client` at `objstore/dynstore/dynstore.go:80-83` defaults an empty region to `us-east-1`.
Require an explicit region at the deployment boundary while keeping that driver behavior.
Kubernetes consumes the same URL, CA and enrolled pairs through the chart's existing values.

### The unit of an instance on the shared store server

The instance unit:

```ini
ExecStartPre=docker compose ... down --remove-orphans
ExecStartPre=docker run ... arizuko generate -i <name>
```

Keep the release image, data and Docker socket mounts, foreground Compose start, stop and restart policy.
Delete the private-store start line and the generator's `--network` and `-e DYNAMODB_URL` options.
The generator reads the enrolled configuration without an instance network.
The unit applies to every instance.
Backup and stopped-apply instructions must leave the external server running.

### The move onto the shared store server

Keep `arizuko store enroll -i <name> <snapshot>` and the
[INSTALL move procedure](../../INSTALL.md#move-an-instance-onto-the-shared-store-server).
Keep the copy's paged inventory, schema/index/stream refusals, 500-item scans, conditional puts and equal-count check.
A dry run on an isolated copy must pass before an operator moves another deployment.

## Contract

`policy` at `cmd/arizuko/store.go:125-137` writes `dynamodb:*` only on `arn:aws:dynamodb:*:*:table/<prefix>.*`.
`enroll` at `cmd/arizuko/store.go:214-216` writes that policy through `manager.putPolicy` for each row, including kept keys.
`TestPolicyDocument` checks the literal bounded document; `TestEnrollMintsEveryRowAndConverges` reads each stored policy back.
These tests prove policy provisioning only.
Enrollment replaces only `tables`; unrelated user/group policies can still widen access.
L318 stays open until the enforcement acceptance check below passes.

Acceptance checks:

- Remove the private catalog directory: package listing and fresh setup still work without any store service or published store port.
- Remove URL, CA, `AWS_REGION` or a required key pair separately: generation refuses before runtime output and names the missing setting or file.
- Enroll as the fresh data directory owner: mode-0600 credential files remain readable by the UID-1000 bootstrap owners.
- Enroll twice against an isolated external server: the second run mints nothing and every policy matches `StoreReach`.
- Run the required `TestEnrolledPoliciesEnforceStoreReach` acceptance check in
  [6/42 acceptance](42-dynamo-over-postgres.md#9-acceptance) before closing L318 or shipping `6/42`.
- Copy from a snapshot, regenerate and start with the source stopped: counts match and source data stays untouched.
- Delete the private URL/port tests, retain their shared credential assertions, and replace the ExtendDB package fixtures without weakening containment tests.

Documentation checks run `go test ./tests/specrefs/ ./tests/webdocs/ -count=1`.
Implementation also needs the affected compose, CLI and enrollment suites against an isolated endpoint.
Enrollment convergence, stopped-source startup and L318 enforcement remain unmeasured on an isolated external server.
