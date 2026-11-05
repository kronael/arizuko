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
defects: [L319]
---

# specs/6/43 — one store server for every deployment

`6/42` settles WHAT the store engine is: a DynamoDB-protocol server over
PostgreSQL, with arizuko as one client of it through the official SDK. It
leaves one question open, because a single instance never has to answer it:
**how many store servers does a host run?**

One. This spec states the topology, the reason the engine splits its own state
across two databases, the boundary that makes one server safe for many
tenants, and the procedure that brings it up.

## 1. The decision

A host **shall** run exactly one store server for every arizuko deployment on
it. The server **shall** hold every deployment's tables. A deployment
**shall** be one _account_ on that server, and **shall not** be a database, a
schema, or a PostgreSQL role.

The alternative — one server per instance — is what the fleet ran until
2026-09-20: `arizuko_extenddb_db_krons` and `arizuko_extenddb_db_sloth`, each a
container, each a PostgreSQL pair, each a port. It works, and it costs one
engine and one connection pool per tenant for a tenancy the engine already
implements. Two instances is an inconvenience; the instance-per-server shape
prices the tenth instance at ten idle engines.

## 2. Why the engine splits catalog from data

The server owns two databases, `<data-db>` and `<data-db>_catalog`. This is
the engine's design, not arizuko's, and it **shall** be preserved: the
deployment procedure of §5 configures the split, it never collapses it.

The split is by **plane**, and the two planes differ in every property that
would otherwise be traded off against each other:

|                           | catalog                                                                                          | data                                                                                                                                               |
| ------------------------- | ------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Holds                     | accounts, IAM users/roles/policies, the `tables` registry, `settings`, metrics, backup manifests | one `_ddb_<uuid>` table per DynamoDB table, plus the engine's queues (`gsi_pending`, `stream_records`, `vector_index_holds`, `idempotency_tokens`) |
| Size                      | small, bounded by tenant count                                                                   | large, bounded by tenant traffic                                                                                                                   |
| Read pattern              | every request, cached in memory (`[auth.cache]`, 60 s)                                           | the request's own keys                                                                                                                             |
| Blast radius of a restore | every tenant's identity                                                                          | one tenant's rows                                                                                                                                  |

A control-plane read **shall not** contend with a tenant's scan. An operator
restoring a tenant's data **shall not** be able to take the account registry
with it. Those two sentences are the whole argument, and each is a property
the split gives for free that a single database would have to be careful to
preserve.

**MEASURED on the shared deployment 2026-09-20:** the catalog holds 26 tables,
the data database 6 — and the 6 are engine machinery, because no arizuko table
had yet been created.

## 3. The catalog is the root of discovery

The `connection_string` in `extenddb.toml` **shall** name the **catalog**
database. The server reads the data database's name and connection string out
of the catalog's own `settings` table.

This is not a detail a deployer may get wrong quietly. **MEASURED
2026-09-20:** with `settings` carrying only the three rows the schema migration
writes, the server started, failed to find `gsi_pending` and
`vector_index_holds` — it was looking for them in the catalog — and then
exited on `Encryption key not found in settings table`.

Four rows carry the whole of the deployment's identity:

| key                               | what it is                                            |
| --------------------------------- | ----------------------------------------------------- |
| `encryption_key`                  | 32 random bytes, base64. Encrypts stored credentials. |
| `data_database_name`              | the data database                                     |
| `data_database_connection_string` | how the server reaches it                             |
| `default_account_id`              | the 12-digit account used when a request names none   |

`encryption_key` **shall** be treated as the deployment's root secret. It
lives in the catalog, it is generated once, and losing it loses every access
key the server has stored.

## 4. Tenancy is the account, not the role

The `tables` registry is keyed `(account_id, table_name)`. Two deployments may
therefore both hold a table called `routd.messages`, and the engine keeps them
apart because the account differs. krons is `614788529278`; sloth is
`293301248123`.

From this the access rules follow, and they **shall** be read as a chain:

1. Each deployment **shall** have its own account.
2. Each deployment **shall** authenticate with a SigV4 access-key pair issued
   under that account. Possession of the key is possession of the account.
3. The engine **shall** resolve every request to the caller's account before
   it reaches a table.
4. The PostgreSQL role is **not** a tenancy boundary. One role (`dynamo`)
   owns both databases and serves every account. A deployment never holds a
   PostgreSQL credential and never opens a PostgreSQL connection.

Point 4 is the one that surprises, so it is stated as a prohibition: an
operator **shall not** attempt to isolate deployments with PostgreSQL grants.
Doing so buys nothing the account boundary does not already give, and it
breaks the engine, which expects to own its own schema.

## 5. Deployment

The role owning the databases needs `CREATEDB` only to let `init` create
them. Where a site's policy withholds `CREATEDB` — the ordinary case for a
shared PostgreSQL — the databases **shall** be created by the operator and the
deployment completed by writing the four rows of §3 directly.

`init` **shall not** be run against an existing database: it refuses, with
`Database '<name>' already exists. Run 'destroy' first, then re-run 'init'`,
and `destroy` drops the databases. There is no `--reuse-existing`.

The procedure, in the order it **shall** be performed:

1. **The databases.** A role with `CREATEDB` creates the pair and hands
   ownership to the application role:
   ```sql
   CREATE ROLE dynamo LOGIN PASSWORD '<pw>';
   CREATE DATABASE dynamo OWNER dynamo;
   CREATE DATABASE dynamo_catalog OWNER dynamo;
   ```
2. **The schema.** `extenddb migrate` applies the catalog schema. The
   application role owns both databases, so it needs no further grant.
3. **The certificate.** TLS is mandatory; the server refuses to start with
   `enabled = false`. `init` generates a self-signed certificate and is the
   only subcommand that does, so it **shall** be run once for the certificate
   even where it cannot create the databases — it writes the certificate
   before it touches PostgreSQL. Every address a client will use **shall** be
   a SAN (`--bind-addr`, `--tls-san`, repeatable).
4. **The four rows.** Write §3's rows into the catalog's `settings`.
   `encryption_key` is `openssl rand -base64 32`.
5. **The config.** `extenddb.toml`, mode `0600`, owned by the service uid:
   `bind_addr`, `port`, the two TLS paths, and `connection_string` naming the
   **catalog**.
6. **The unit.** The container runs `--network=host` with the state directory
   bind-mounted. The command **shall** carry `--foreground`: without it the
   server daemonizes and systemd reports `PID file not created within 5
seconds`. The image's `ENTRYPOINT` is already `extenddb`, so the command is
   `serve --config … --foreground`, never `extenddb serve …`.
7. **Verify.** `extenddb verify` **shall** report `HEALTHY` before any
   deployment is pointed at the server.

**MEASURED 2026-09-20 on hel1v5:** the procedure above brought up
`extenddb 0.1.11 (catalog 0.0.3)` on `0.0.0.0:18445` against `dynamo` /
`dynamo_catalog` at `10.0.5.1`, with the role holding no `CREATEDB`.
`verify` reports `HEALTHY: All checks passed`.

### Ports

A port **shall** be unique per store server on a host. The fleet holds
`18443` (krons), `18444` (sloth) and `18445` (the shared server). A port
collision surfaces as a start failure, not as a misroute.

## 6. Enrolment

`arizuko store enroll` gives one instance its identity on a store server: one
account, and for each daemon that reaches the store one IAM user, one policy
and one access key.

### The problem

A daemon signs every store request with its own access key. It reads that key
from its environment (`objstore/dynstore/env.go:16-23`), and `generate` carries
the pair forward from `env/<daemon>.env` and never mints one
(`compose/compose.go:1025-1051`). Nothing in arizuko mints the pair. Today a
person runs `extenddb manage` three times per daemon (`INSTALL.md`
§"The per-daemon store keys limit each daemon to its own tables"). The
bootstrap script does the same in shell, for five users only
(`INSTALL.md:202-216`). That script is a second renderer of the policy that
`StoreReach` declares (`store/owner_reach.go:16-34`), and it leaves `dashd`,
`webd`, `proxyd` and every adapter without a key. A daemon that has an
endpoint and no key exits at boot and restart-loops the instance (BUGS
`L406`).

On one server per instance this was one run by hand. On the shared store
server each instance needs its own account and its own keys before its first
restart there (BUGS `L319`). On one instance every key carries `dynamodb:*` on `*`
(BUGS `L318`). On the shared store server such a key reaches every table of
its account, so the move must not copy it.

### Where it fits

```
                         .env: DYNAMODB_URL, STORE_CA
                         process env: EXTENDDB_PASSWORD
                                     │
                                     ▼
 store.StoreReach ─────► ┌────────────────────────────┐ HTTPS, Basic admin ┌────────────────────────┐
 (daemon → prefixes)     │ arizuko store enroll (new) ├───────────────────►│ store server           │
                         └─────────────┬──────────────┘ /management (new)  │ management API:        │
                                       │ key pair (new)                    │ accounts, users,       │
                                       ▼                                   │ policies, access keys  │
                         env/<daemon>.env, env/operator.env                └───────────▲────────────┘
                                       │ carried forward                               │
                                       ▼                                               │ SigV4, own key
                         ┌────────────────────────────┐  env file  ┌─────────┐         │
                         │ arizuko generate           ├───────────►│ daemons ├─────────┘
                         └────────────────────────────┘            └─────────┘
```

The management API is ExtendDB's. It is mounted at `/management` on the same
port as the DynamoDB API (`crates/server/src/lib.rs:169-178` in the ExtendDB
source). It is JSON over HTTPS, with HTTP Basic authentication as an admin
user (`crates/server/src/management/auth.rs`). `extenddb manage` is a client
of the same routes (`crates/app/src/manage_http.rs`). These routes were read
from the ExtendDB source at version 0.1.12. The deployment runs 0.1.11, and
the bootstrap script drives its `manage` client successfully, so the routes
are an inference for 0.1.11 and not a measurement.

`enroll` reads the address and the certificate from the two keys that every
daemon reads. It reads the admin password from `EXTENDDB_PASSWORD`, the name
that `extenddb manage` reads. It adds no key, file or flag. It dials
`DYNAMODB_URL` as the host CLI sees it (`core/config.go:196-206`), the address
that every other `arizuko` command dials. It does not edit `.env`, because the
platform never edits an operator line (`compose/manifest.go:124-127`). So the
operator points `.env` at the server first. The admin user is `admin`, the
name that `extenddb init` gives it.

```yaml
- path: cmd/arizuko/store_enroll.go (new)
  job: one enrolment pass over store.StoreReach against the server that DYNAMODB_URL names
  names:
    - 'cmdStore(args []string)  // arizuko store [-i NAME] enroll'
    - 'enroll(dataDir, instance string, m *manager) error'
    - 'policy(prefixes []string) string  // the `tables` document'
    - 'enrollee(daemon string) (user, file string)'
  never: prints a secret; deletes a user, a policy or a key; edits .env; mints for a daemon whose StoreReach row is empty
- path: cmd/arizuko/extenddb.go (new)
  job: the management calls, over HTTPS with the STORE_CA certificate and Basic admin authentication
  names:
    - 'manager{base string; client *http.Client; password string}'
    - 'account(name string) (id string, err error)  // GET, else POST /management/accounts'
    - 'user(acct, name string) error  // POST .../users; 409 is success'
    - 'putPolicy(acct, user, doc string) error  // PUT .../users/{user}/policy/tables'
    - 'keys(acct, user string) ([]string, error)  // GET .../access-keys, active ids'
    - 'mint(acct, user string) (id, secret string, err error)  // POST .../access-keys'
  never: logs a request body or a response body
- path: cmd/arizuko/main.go (changed)
  job: registers "store"
- path: compose/compose.go (changed)
  job: owns the env file format, so the key pair is written where mergeStoreCredentials reads it
  names:
    - 'ReadStoreKeyID(path string) string'
    - 'WriteStoreKey(dataDir, path, keyID, secret string) error  // replaces two lines, keeps the rest, 0600, atomic'
  never: mints a key
- path: objstore/dynstore/env.go (changed)
  job: exports the STORE_CA decoder, so enroll and FromEnv read the certificate one way
  names:
    - 'CA() ([]byte, error)'
- path: objstore/dynstore/dyntest/dyntest.go (changed)
  job: starts the endpoint with a known EXTENDDB_ADMIN_PASSWORD and exports it as EXTENDDB_PASSWORD
  never: invents an admin password for an endpoint it did not start
- path: cmd/arizuko/store_enroll_test.go (new)
  job: enrols against the dyntest endpoint twice; the second run mints nothing, and a scoped key is refused on another owner's table
- path: INSTALL.md (changed)
  job: the bootstrap loop and the recipe per daemon become one enroll call
```

### Each run converges, and a re-run rotates nothing

`enroll` walks `StoreReach` in sorted order and skips the two empty rows,
`timed` and `ttsd`. At `36f98f1b7` the map has 15 rows, and 13 of them need a
key: 12 daemons and the operator CLI. `enroll` enrols every row that is not
empty, also a daemon that is not installed. So a later `package add` finds
its key in place, and `L406` cannot occur again. Each step reads before it
writes:

| step                         | the call                                        | when it exists                                                    | when it is absent        |
| ---------------------------- | ----------------------------------------------- | ----------------------------------------------------------------- | ------------------------ |
| account `arizuko_<instance>` | `GET`, then `POST /management/accounts`         | use its id                                                        | create it                |
| user, one per row            | `POST /management/accounts/{id}/users`          | `409`, continue                                                   | create it                |
| policy `tables` on that user | `PUT .../users/{user}/policy/tables`            | replace it with the document from `StoreReach`                    | create it                |
| key in the env file of a row | `GET .../users/{user}/access-keys`, then `POST` | keep it when its id is an active key of that user in that account | mint one, write the file |

The account name is the compose project name that `generate` writes
(`name: arizuko_<instance>`). ExtendDB keeps `account_name` unique
(`crates/storage-postgres/migrations/001_schema.sql:9-13`), so the lookup is
exact. The user is the `StoreReach` key, with one exception: `cmd/arizuko` is
not a valid IAM name, because `/` is outside the allowed set
(`crates/server/src/management/mod.rs:216-222`). That row is the user
`operator`, the name that the bootstrap script uses, and its pair goes to
`env/operator.env`. The policy has one resource per prefix of the row:
`arn:aws:dynamodb:*:*:table/<prefix>.*`.

`enroll` puts the policy on every run. So a re-run against an enrolled
instance sets each policy back to `StoreReach`, and that closes `L318` for
every enrolled deployment without a separate rollout.

`enroll` writes a key to its file immediately after the mint, before the next
row, so an interrupted run loses at most one pair. That pair stays on the
server as a key that no file holds. A later run prints the id of each such
key and deletes none. An env file that holds a key the server does not know
gets a new key. After a move, every file is in this state. To rotate the key
of one daemon, the operator deletes its two lines and runs `enroll` again.
The run then prints the old key as held by no file, and the operator deletes
it with `extenddb manage delete-access-key` after the restart. No flag
rotates keys.

The output is one line per row: the user, the prefixes of its policy, the key
id, and `kept` or `minted`. A key id is an identifier and not a secret. A
secret goes only to its env file, mode `0600`. A failed call ends the run with
a non-zero status, after the lines of the rows that are done.

**One refusal.** An instance that already holds keys on this server, under an
account with another name, would get a second account that is empty. Every
daemon would then start on no data. The per-instance servers that
`extenddb init` made name their account `default`. So when no account
`arizuko_<instance>` exists and `env/operator.env` holds a pair, `enroll`
first signs one `DescribeTable` on `routd.acl` with that pair. Any answer
other than `UnrecognizedClientException` means that the server knows the key.
`enroll` then stops and names the key id. This refusal also stops an operator
whose host CLI still dials the old server (BUGS `L661`).

### The command table gets one entry

`cmd/arizuko/main.go:65-67`

```go
		"images":       cmdImages,
		"package":      cmdPackage,
		"secret":       cmdSecret,
```

Proposed:

```go
		"images":       cmdImages,
		"package":      cmdPackage,
		"secret":       cmdSecret,
		"store":        cmdStore, // spec 6/43 §6
```

### The bootstrap loop becomes one call

`INSTALL.md:202-216`

```bash
for user in authd routd onbod operator runed; do
  case "$user" in
    authd) resources='["arn:aws:dynamodb:*:*:table/authd.*"]';;
    routd) resources='["arn:aws:dynamodb:*:*:table/routd.*"]';;
    onbod) resources='["arn:aws:dynamodb:*:*:table/onbod.*","arn:aws:dynamodb:*:*:table/routd.*"]';;
    operator) resources='["arn:aws:dynamodb:*:*:table/authd.*","arn:aws:dynamodb:*:*:table/onbod.*","arn:aws:dynamodb:*:*:table/routd.*"]';;
    runed) resources='["arn:aws:dynamodb:*:*:table/runed.*"]';;
  esac
  policy="{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"dynamodb:*\",\"Resource\":$resources}]}"
  sudo --preserve-env=EXTENDDB_PASSWORD docker exec -e EXTENDDB_PASSWORD "$container" extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin create-user --account-id "$STORE_ACCOUNT_ID" --user-name "$user" >/dev/null 2>&1
  sudo --preserve-env=EXTENDDB_PASSWORD docker exec -e EXTENDDB_PASSWORD "$container" extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin put-user-policy --account-id "$STORE_ACCOUNT_ID" --user-name "$user" --policy-name tables --policy-document "$policy" >/dev/null 2>&1
  key=$(sudo --preserve-env=EXTENDDB_PASSWORD docker exec -e EXTENDDB_PASSWORD "$container" extenddb manage -c /var/lib/extenddb/extenddb.toml --user admin create-access-key --account-id "$STORE_ACCOUNT_ID" --user-name "$user" 2>/dev/null)
  printf '%s' "$key" | python3 -c 'import json,sys; x=json.load(sys.stdin); print("AWS_ACCESS_KEY_ID="+x["access_key_id"]+"\nAWS_SECRET_ACCESS_KEY="+x["secret_access_key"])' > "$proof/$user.env"
  unset key
done
```

Proposed, after `STORE_CA` and `DYNAMODB_URL` are in `.env` and before the
owner health check:

```bash
./arizuko store enroll -i "$flavor"
```

The loop goes, and so do the copies of `$proof/<user>.env` into `env/`.
`StoreReach` is then the only source of every policy, and the test that binds
it to the call sites (`TestStoreReachMatchesTheCallSites`) also binds what the
server holds.

### Amazon takes the same calls, and has no account call

The daemons need nothing new on Amazon. `dynstore` signs through the AWS SDK,
and the conformance suite already runs against `amazon/dynamodb-local`.
`enroll` differs as the table shows. Only the ExtendDB client is built. No
deployment runs on Amazon, so the interface for a second client waits for the
first such deployment.

| step       | ExtendDB                                        | Amazon                                                             |
| ---------- | ----------------------------------------------- | ------------------------------------------------------------------ |
| wire       | JSON REST under `/management`, HTTP Basic admin | IAM Query API, SigV4 as a principal that holds the `iam:` actions  |
| account    | `GET` or `POST /management/accounts`, by name   | none in IAM; the account is the caller's (`sts:GetCallerIdentity`) |
| tenancy    | one account per instance on one server          | one instance per account and region; table names hold no instance  |
| user       | `POST .../users`, `409` when it exists          | `CreateUser`, `EntityAlreadyExists` when it exists                 |
| policy     | `PUT .../policy/tables`                         | `PutUserPolicy`, the same document; the `*:*` ARN form is valid    |
| keys       | `GET .../access-keys`; no limit per user        | `ListAccessKeys`; two keys per user, so one orphan blocks a mint   |
| new key    | valid at once                                   | valid after IAM propagation, some seconds later                    |
| `STORE_CA` | the certificate of the server                   | empty, because the endpoint has a public certificate               |

### The move onto the shared store server

This is the acceptance for each instance. It is not a part of `enroll`.

1. The instance is stopped. The item count of every table under its old
   account is recorded: every owner prefix and adapter prefix, `audit_log`
   included.
2. `.env` names the shared store server in `DYNAMODB_URL` and its certificate
   in `STORE_CA`. `generate` runs. `arizuko store enroll` reports a new
   account and 13 minted keys.
3. Every table is copied into the new account. `arizuko dump` and `apply` do
   not do this step: a dump holds config documents, secrets, messages and the
   group trees, and not the stores of authd or runed
   (`cmd/arizuko/README.md:80`). User accounts, signing keys, sessions, runs,
   audit rows and adapter dedup rows would stay behind.
4. The item count of every table under the new account equals the count from
   step 1.
5. The instance restarts, every daemon is healthy, and one live turn answers
   end to end.
6. The old store server is stopped. Its data directory is kept.

## Ties

`6/42` (the engine, the driver, the conformance gate) · `6/37` (the object
model) · `6/40` (the surface and the audited write) · `5/14` (which tier a
store credential sits in) · `5/33` (the path is the coordinate, and the
account is not) · BUGS `L318`, `L319`, `L406`, `L661`.
