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

## 6. What is not built

**Enrolment.** `arizuko` has no subcommand that creates an account on a store
server and issues that account's access key. Today an operator does it by
hand through `extenddb manage` and pastes the pair into the instance's
`env/` files and `operator.env`. That is BUGS `L319`, and it is why this spec
is `partial` rather than `shipped`: the topology is live and verified, and
nothing yet walks an instance onto it.

**Per-deployment key scope.** `L318` records the adjacent finding: every
daemon's store key carries `dynamodb:* on *`, so the 19 per-daemon keys give
attribution and not isolation. On a shared server that matters more than it
did on a per-instance one, because the blast radius of a leaked key is now
every deployment's account rather than one.

Until both close, a deployment **shall** be moved onto the shared server only
by an operator who can verify the row counts on both sides.

## Ties

`6/42` (the engine, the driver, the conformance gate) · `6/37` (the object
model) · `6/40` (the surface and the audited write) · `5/14` (which tier a
store credential sits in) · `5/33` (the path is the coordinate, and the
account is not) · BUGS `L318`, `L319`.
