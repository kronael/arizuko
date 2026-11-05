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

The alternative is one server per instance, each a container, a PostgreSQL
pair and a port. It works, and it costs one engine and one connection pool per
tenant for a tenancy the engine already implements: the tenth instance costs
ten idle engines.

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
apart because the account differs.

From this the access rules follow, and they **shall** be read as a chain:

1. Each deployment **shall** have its own account.
2. Each deployment **shall** authenticate with a SigV4 access-key pair issued
   under that account. Possession of the key is possession of the account.
3. The engine **shall** resolve every request to the caller's account before
   it reaches a table.
4. The PostgreSQL role is **not** a tenancy boundary. One role owns both
   databases and serves every account. A deployment never holds a
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

**MEASURED 2026-09-20 on the shared store server:** the procedure above
brought up `extenddb 0.1.11 (catalog 0.0.3)` against a database pair on a
shared PostgreSQL, with the role holding no `CREATEDB`. `verify` reports
`HEALTHY: All checks passed`.

### Ports

A port **shall** be unique per store server on a host. A port collision
surfaces as a start failure, not as a misroute.

## 6. Enrolment

`arizuko store enroll -i NAME [SNAPSHOT]` gives one instance its identity on a
store server: one account, and for each daemon that reaches the store one IAM
user, one policy and one access key. With `SNAPSHOT`, it then copies every
table of the old store server of the instance into that account, and proves by
count that no item is lost.

### The problem

A daemon signs every store request with its own access key, which `generate`
carries forward from `env/<daemon>.env` and never mints
(`objstore/dynstore/env.go:38-55`, `compose/compose.go:1056-1082`). Today a
person mints each pair with `extenddb manage` (`INSTALL.md` §"The per-daemon
store keys limit each daemon to its own tables"), and the bootstrap script does
it in shell for five users only (`INSTALL.md:202-216`). That script is a
second renderer of the policy that `StoreReach` declares
(`store/owner_reach.go:16-34`), and it leaves `dashd`, `webd`, `proxyd` and
every adapter without a key. A daemon that has an endpoint and no key exits at
boot and restart-loops the instance (BUGS `L406`).

On the shared store server each instance needs its own account and its own
keys before its first restart there (BUGS `L319`), and its rows must arrive
complete. `arizuko dump` and `apply` do not move them: a dump holds config
documents, secrets, messages and the group trees, and not the stores of authd,
runed or the adapters (`cmd/arizuko/README.md:80`).

### Where it fits

```
                      .env, env/operator.env          SNAPSHOT/.env, SNAPSHOT/env/
                          │                                 │
                          ▼                                 ▼
                      ┌──────────────────────────────────────────────────┐
 store.StoreReach ──► │ arizuko store enroll -i NAME [SNAPSHOT] (new)    │
 (daemon → prefixes)  └───┬───────────────────┬─────────────────────┬────┘
                          │                   │                     │ ListTables,
                          │ key pairs         │ /management,        │ DescribeTable,
                          │                   │ Basic admin;        │ Scan; old
                          │                   │ DynamoDB API,       │ operator pair
                          ▼                   │ row pairs           ▼
              env/<daemon>.env                │             ┌────────────────────┐
                          │                   │             │ old store server   │
                          │                   ▼             └────────────────────┘
                          │             ┌────────────────────┐
                          ▼             │ store server       │
                ┌──────────────────┐    │ management API     │
                │ arizuko generate │    │ DynamoDB API       │
                └─────────┬────────┘    └────────────────────┘
                          │ env file              ▲
                          ▼                       │ SigV4, own key
                     ┌─────────┐                  │
                     │ daemons ├──────────────────┘
                     └─────────┘
```

The management API is ExtendDB's: JSON over HTTPS at `/management` on the
port of the DynamoDB API, with HTTP Basic authentication as an admin user
(`crates/server/src/lib.rs:169-178`, `crates/server/src/management/auth.rs`).
`extenddb manage` is a client of the same routes. Every ExtendDB route, error
and line this section names was read from its source at tag `v0.1.11`, the
version the template pins (`template/services/extenddb/extenddb.yml:26`), and
none was measured against a live server.

**`enroll` reads no store setting from the process env.** `main` loads the
instance env before any command runs (`cmd/arizuko/main.go:85` →
`instance_args.go:38` → `main.go:245`). That load puts `env/operator.env` over
`.env`, so a loopback line that an earlier `generate` wrote wins over a new
`DYNAMODB_URL`, and a second load keeps what the process holds
(`core/config.go:196-206`). So `enroll` first runs `WriteOperatorEnv`, the URL
step of `generate`, which removes that line when `.env` names another server
(BUGS `L661`, `compose/compose.go:458-473`). Then `view` reads the two files
into a map, `env/operator.env` over `.env`: the host view of every `arizuko`
command, taken after the URL step. The same `view` reads the snapshot. The
admin password comes from `EXTENDDB_PASSWORD`, which `extenddb manage` reads,
and the admin user is `admin`, which `extenddb init` makes. `enroll` does not
edit `.env` (`compose/manifest.go:124-127`).

Only the ExtendDB management client is built. IAM on Amazon has no account
call, allows two keys per user and makes a new key valid only after
propagation, so a second client differs in every step and waits for the first
deployment on Amazon. The daemons need nothing new there.

**The old server is a directory argument.** The copy needs the address, the
certificate and an operator pair of the old server. The live files cannot
give them: `.env` names the new server after step 2 of the move, and `enroll`
replaces every pair in `env/`. `SNAPSHOT` is the copy of `.env` and `env/`
that step 1 of the move makes for the return path, so the argument names a
directory that exists anyway, and no flag or key is added. It does not change,
so a second run reads the same old server. Without it, `enroll` only enrols,
which is what a new install needs.

```yaml
- path: cmd/arizuko/store.go (new)
  job: arizuko store enroll; enrols every StoreReach row on the server of the host view, then copies the snapshot's account into it
  names:
    - 'cmdStore(args []string)  // arizuko store [-i NAME] enroll [SNAPSHOT]'
    - 'view(dir string) (map[string]string, error)  // dir/.env, then dir/env/operator.env over it'
    - 'enroll(dataDir string, dst map[string]string, m *manager) error'
    - 'policy(prefixes []string) string  // the `tables` document'
    - 'enrollee(daemon string) (user, file string)'
    - 'holders(m *manager) (map[string]holder, error)  // key id -> account, user, active'
    - 'copyTables(src *dynamodb.Client, dst map[string]*dynamodb.Client) error  // dst: one client per prefix'
  never: prints a secret; deletes a user, a policy, a key, a table or an item; replaces an item; writes to the old server or the snapshot; reads a store setting from the process env; edits .env; mints for an empty StoreReach row
- path: cmd/arizuko/extenddb.go (new)
  job: the management calls, over HTTPS with the STORE_CA certificate and Basic admin authentication
  names:
    - 'manager{base string; client *http.Client; password string}'
    - 'accounts() (map[string]string, error)  // GET /management/accounts: name -> id'
    - 'account(name string) (id string, err error)  // the id, else POST /management/accounts'
    - 'users(acct string) ([]string, error)  // GET .../users'
    - 'user(acct, name string) error  // POST .../users; 409 is success'
    - 'putPolicy(acct, user, doc string) error  // PUT .../users/{user}/policy/tables'
    - 'keys(acct, user string) ([]key, error)  // GET .../access-keys: id and is_active, inactive keys included'
    - 'mint(acct, user string) (id, secret string, err error)  // POST .../access-keys'
  never: logs a request body or a response body
- path: cmd/arizuko/main.go (changed)
  job: registers "store"
- path: compose/compose.go (changed)
  job: owns the env file format, so enroll writes the pair where mergeStoreCredentials reads it
  names:
    - 'WriteOperatorEnv(dataDir string) error  // the operator.env URL step of Generate, from .env'
    - 'WriteStoreKey(dataDir, path, keyID, secret string) error  // replaces two lines, keeps the rest, 0600, atomic'
  never: mints a key
- path: objstore/dynstore/env.go (changed)
  job: exports the STORE_CA decoder over a raw value; FromEnv passes the process value, enroll the value of a view
  names:
    - 'DecodeCA(raw string) ([]byte, error)'
- path: objstore/dynstore/dynstore.go (changed)
  job: exports the SDK client that Dial builds, so the copy moves typed items without the objstore encoding
  names:
    - 'Client(cfg Config) (*dynamodb.Client, error)'
  never: adds a method to objstore.Store
- path: objstore/dynstore/dyntest/dyntest.go (changed)
  job: starts the endpoint with a known EXTENDDB_ADMIN_PASSWORD and exports it as EXTENDDB_PASSWORD
  never: invents an admin password for an endpoint it did not start
- path: cmd/arizuko/store_test.go (new)
  job: runs instanceArgs then cmdStore against the dyntest endpoint while env/operator.env holds a loopback line to a closed port; two runs, and the second mints nothing; each policy read back equals policy(StoreReach); a half pair and a key of another account stop the run before any write; a wrong secret ends it without `kept`. Copies a seeded account with every attribute type and a table above one 1 MiB page; the counts are equal; a second run keeps a changed destination item; an extra destination item, a table whose prefix has no row, a table with an index and a destination table with another key schema each fail the run
- path: cmd/arizuko/README.md (changed)
  job: documents store enroll
- path: INSTALL.md (changed)
  job: the bootstrap loop becomes one enroll call; the unit of an instance on the shared store server loses the private store
```

The dyntest endpoint is ExtendDB's development build, which skips the policy
decision after authentication (`crates/server/src/request_helpers.rs:95-97`).
So the test proves the policy document, and the dry run of the move proves
that the server enforces it. That endpoint makes every table `ACTIVE` at once
(`objstore/dynstore/dyntest/dyntest.go:331`), so the test does not reach the
wait of the copy. ExtendDB `v0.1.11` has no call that deactivates a key, so no
test makes an inactive one.

### Each run converges, and a re-run rotates nothing

`enroll` walks `StoreReach` in sorted order and skips the two empty rows,
`timed` and `ttsd`. At `962cb3298` the map has 15 rows, and 13 of them need a
key: 12 daemons and the operator CLI. `enroll` enrols every row that is not
empty, also a daemon that is not installed, so a later `package add` finds its
key in place.

The user is the `StoreReach` key, with one exception: `cmd/arizuko` is not a
valid IAM name, because `/` is outside the allowed set
(`crates/server/src/management/mod.rs:214-222`). That row is the user
`operator`, and its pair goes to `env/operator.env`. The account name is the
compose project name that `generate` writes (`name: arizuko_<instance>`).
ExtendDB keeps `account_name` unique
(`crates/storage-postgres/migrations/001_schema.sql:9-13`), so the lookup is
exact. The policy has one resource per prefix of the row:
`arn:aws:dynamodb:*:*:table/<prefix>.*`.

**First, a check that writes nothing.** `enroll` lists every account on the
server, every user of each account, and every access key of each user with its
`is_active` flag, so inactive keys are included. Then it reads both lines of
the env file of each row:

| the env file holds                                              | `enroll`                              |
| --------------------------------------------------------------- | ------------------------------------- |
| no pair                                                         | mints one                             |
| one of the two lines                                            | stops, names the file                 |
| a key id that no user on this server holds                      | mints one, prints the replaced key id |
| a key id of another account, or of another user in this account | stops, names the key id and its owner |
| an inactive key id of the user of the row                       | stops, names the key id               |
| an active key id of the user of the row                         | keeps it after the signed check       |

The owner of a key comes from these listings, never from an authentication
failure: ExtendDB answers `UnrecognizedClientException` for an unknown key, an
inactive key and a request outside the clock window alike
(`crates/auth/src/lib.rs:142-150`, `:194-198`,
`crates/auth/src/sigv4/verify.rs:130-134`). The check also stops a run that
dials the old server by mistake, because there the files hold keys of the
`default` account that `extenddb init` made.

**Then the writes.** Each step reads before it writes:

| step                         | the call                                | when it exists                                 | when it is absent        |
| ---------------------------- | --------------------------------------- | ---------------------------------------------- | ------------------------ |
| account `arizuko_<instance>` | `GET`, then `POST /management/accounts` | use its id                                     | create it                |
| user, one per row            | `POST .../users`                        | `409`, continue                                | create it                |
| policy `tables` on that user | `PUT .../users/{user}/policy/tables`    | replace it with the document from `StoreReach` | create it                |
| key in the env file of a row | the check above                         | the signed check                               | mint one, write the file |

**The signed check.** A kept pair signs one `DescribeTable` on a table name
under the first prefix of its row. The table, or `ResourceNotFoundException`,
proves the secret: the server authenticates the request before it authorizes
it or reads the table (`crates/server/src/handler.rs:91-121`). Any other error
ends the run and prints the error text of the server, so a clock outside the
window reads as `Signature expired`. The check runs after the policy step of
the same row, and a policy write clears the policy cache of the server
(`crates/server/src/management/iam_policy.rs:207`).

`enroll` writes a key to its file immediately after the mint, so an
interrupted run loses at most one pair, which stays on the server as a key
that no file holds. To rotate the key of one daemon, the operator deletes its
two lines, runs `enroll` again, and after the restart deletes the old key with
`extenddb manage delete-access-key`. The output is one line per row: the user,
the prefixes of its policy, the key id, and `kept` or `minted`. A secret goes
only to its env file, mode `0600`. A failed call ends the run with a non-zero
status.

`enroll` replaces only the policy named `tables`. The server adds every other
policy of the user and of its groups (`crates/server/src/authorization.rs:90-105`),
so a policy that an operator put by hand stays in effect. On the shared store
server the account of an instance holds only the users that `enroll` made.
BUGS `L318` closes for an instance when its dry run shows that a row pair is
refused outside its prefixes; it stays open for every other server.

### The copy moves every table and counts it

The copy has no code per table or per entity. It takes the names, the key
schemas and the items from the source and decodes no item, so the SDK
attribute values carry every DynamoDB type.

1. **The inventory.** `ListTables` with the old operator pair, every page, and
   `DescribeTable` of each table, before `enroll` writes anything. The run
   stops there when a
   name has no `<prefix>.` whose prefix is a `StoreReach` row of that name,
   because no live pair can write that table. It also stops on a table with a
   secondary index or an enabled stream, because the copy carries neither and
   `objstore` makes neither (`objstore/dynstore/entity.go:30-40`). It does not
   read TTL: arizuko sets it on no table, and a lost TTL setting keeps items.
2. **The table.** An absent destination table is created, on-demand, with
   the key schema and the attribute definitions of the source. Then the copy
   waits until the table is `ACTIVE`, also a table that an earlier run made,
   with the waiter of `CreateEntity` (`objstore/dynstore/entity.go:46-52`):
   ExtendDB makes a table `CREATING`
   (`crates/storage-postgres/src/create_table.rs:75-94`) and refuses data
   calls on it. Then the key schema and the attribute definitions of the
   destination must equal those of the source, or the run stops. Nothing else
   checks a table that exists (`objstore/dynstore/entity.go:20-23`).
3. **The items.** A `Scan` with `ConsistentRead`, every page until
   `LastEvaluatedKey` is empty. Each item goes to `PutItem` with the attribute
   values unchanged and the condition `attribute_not_exists` on the partition
   key. `ConditionalCheckFailedException` means the key is there, and the copy
   keeps that item. So the copy never replaces an item, and a run after the
   restart on the new server cannot put an old value over a new one.
4. **The count.** The source count is the number of items that the scan of
   step 3 returned. The destination count is the sum of `Count` over a `Scan`
   with `Select: COUNT` and `ConsistentRead`, every page until
   `LastEvaluatedKey` is empty, because ExtendDB applies the 1 MiB page limit
   to a count too (`crates/engine/src/read_helpers.rs:94-104`). The line per
   table is the name, the count and `equal`. Unequal counts end the run with a
   non-zero status.

The count is enough. Every key of the source is in the destination, because
each put succeeded or found the key. So equal counts leave no key that only
the destination holds. During the move nothing else writes to either
account, so a kept item holds the value that an earlier run of the copy
wrote.

The old operator pair is the one source credential, because no row pair
reaches every prefix. Its user needs `ListTables`, `DescribeTable` and `Scan`
on every table; where it has less, the operator adds that read policy with
`extenddb manage put-user-policy` on the old server.

ExtendDB's `ExportTableToPointInTime` and `ImportTable` do not do this job:
both need a path configured on each server
(`crates/engine/src/import_export.rs:43-58`), and `ImportTable` skips an item
that fails a check and only counts it (`:124-151`).

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

The copies of `$proof/<user>.env` into `env/` and the hand-written URL line of
`env/operator.env` go too (`INSTALL.md:221-223`), because `enroll` writes both.
`StoreReach` is then the only source of every policy, and
`TestStoreReachMatchesTheCallSites` binds it to the call sites.

### The unit of an instance on the shared store server

`INSTALL.md:717-719`

```ini
ExecStartPre=/usr/bin/docker compose -f /srv/data/arizuko_<name>/docker-compose.yml down --remove-orphans
ExecStartPre=/usr/bin/docker compose -f /srv/data/arizuko_<name>/docker-compose.yml up -d --wait extenddb_db extenddb
ExecStartPre=/usr/bin/docker run --rm --pull=never --network arizuko_<name>_default -e DYNAMODB_URL=https://extenddb:18443 -v /srv/data/arizuko_<name>:/srv/data/arizuko_<name> -v /var/run/docker.sock:/var/run/docker.sock <release-image> arizuko generate -i <name>
```

Proposed:

```ini
ExecStartPre=/usr/bin/docker compose -f /srv/data/arizuko_<name>/docker-compose.yml down --remove-orphans
ExecStartPre=/usr/bin/docker run --rm --pull=never -v /srv/data/arizuko_<name>:/srv/data/arizuko_<name> -v /var/run/docker.sock:/var/run/docker.sock <release-image> arizuko generate -i <name>
```

The second line starts the private store, so it goes. The `-e DYNAMODB_URL`
of the generator keeps the in-network address over `.env`
(`core/config.go:196-206`), so it goes too, and the generator reads the shared
address from `.env`. Its `--network` goes with them: `down` removes the
network of the instance, and only the store line made it again before the
generator ran.

### The move onto the shared store server

This is the acceptance for each instance, in this order. `generate` writes
store rows (`syncProxydRoutes`, `cmd/arizuko/main.go:189`), so the unit starts
only after `enroll` exits 0: a row written before the copy makes the counts
unequal.

1. **Stop and keep.** `systemctl stop arizuko_<name>`. Copy `.env` and `env/`
   to a snapshot directory, mode `0700`. Start only the old store with
   `docker compose … up -d --wait extenddb_db extenddb`.
2. **Point.** `.env` names the shared store server in `DYNAMODB_URL` and its
   certificate in `STORE_CA`. The address is one that the host and every
   container reach, and that the certificate carries as a SAN.
3. **Enrol and copy.** `arizuko store enroll -i <name> <snapshot>` reports the
   account, 13 minted keys (the shared server knows no key of the old one) and
   every table `equal`, and exits 0.
4. **Detach the private store.** `arizuko package rm -i <name> extenddb`, the
   unit as the previous section shows, wherever the unit is deployed from, and
   `systemctl daemon-reload`.
5. **Restart without the old server.** Stop the two containers of the old
   store, then `systemctl start arizuko_<name>`. The start must succeed while
   the old server is down.
6. **Verify.** Every daemon is healthy. A browser session from before the move
   opens the dashboard without a new login, which proves the users, signing
   keys and refresh tokens of authd. Each adapter in `services/` takes one
   inbound message to a reply, and no message from before step 1 is delivered
   again.
7. **Keep the old data.** The data directory of the old store stays.

Until step 5 the old account is only read. Restoring `.env`, `env/` and the
unit from the snapshot, then `arizuko package sync`, returns the instance to
the old server. After step 5, new rows exist only in the new account.

**A dry run on a copy comes first.** It runs on a copy of the data directory
under another instance name, so `enroll` makes another account, and on a copy
of the PostgreSQL data of the old store, with no adapter started. It must
show:

- the tables that `enroll` copies equal
  `SELECT table_name FROM tables WHERE account_id = '<old id>'` in the old
  catalog;
- every table `equal` with the item total, or the name and the reason of each
  table that stopped;
- a second run that mints nothing and reports the same counts;
- `generate` after the run, and every core daemon healthy while the old server
  is stopped;
- one row pair that signs `DescribeTable` on a table under a prefix outside
  its row gets `AccessDeniedException`.

## Ties

`6/42` (the engine, the driver, the conformance gate) · `6/37` (the object
model) · `6/40` (the surface and the audited write) · `5/14` (which tier a
store credential sits in) · `5/33` (the path is the coordinate, and the
account is not) · BUGS `L318`, `L319`, `L406`, `L661`.
