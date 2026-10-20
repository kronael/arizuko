# core

The vocabulary every daemon shares: the config loader, the row types, the
timestamp format and the cron reader.

## Purpose

`core` is the bottom of the import graph. Every daemon imports it; it imports
nothing arizuko-internal (only `godotenv` for `.env` parsing, `uuid` for ids and
`robfig/cron` for schedules). That is what keeps the split topology from growing
a cycle: two daemons that must agree on the shape of a `Route`, or on the
instant a cron names, agree here rather than through each other.

It holds no state, opens no database and serves no HTTP.

## The config door

`LoadConfig` reads `.env` from the working directory plus the process
environment into one `Config`. Three entry points, and the difference matters:

- `LoadConfig()` — the daemon's door. A container has its environment already;
  `DATA_DIR` is set by compose.
- `LoadConfigFrom(dir)` — the host CLI's door. It pins the data root to `dir`
  first, because without that the root fell back to the operator's cwd and
  `arizuko create` wrote the `groups/` skeleton into whatever directory the
  command was run from.
- `LoadInstanceEnv(dir)` — loads one instance's `.env` plus the operator's own
  store credential, which lives in its own file rather than `.env` because
  `.env` reaches every service without a scoped env file. A process that
  already names a store keeps the endpoint it was given and takes only the
  credential. Every host CLI door calls it: `DYNAMODB_URL` decides whether a
  store is the server or a file, so a subcommand that skipped this wrote to
  the file while every daemon read the server (BUGS `L294`).

`SecretKeyring` splits `SECRETS_KEY` into its comma-separated keyring;
`RequireSecretKeyring` refuses an empty result with `ErrNoSecretsKey`, so one
rule — never store a secret in plaintext — has one enforcement point.

## The timestamp format

`TS(t)` and `NowTS()` render a TEXT timestamp through `TSLayout`
(`2006-01-02T15:04:05.000000000Z07:00`): fixed width, nine fraction digits,
forced UTC. Every compared timestamp column goes through it. `RFC3339Nano`
strips trailing zeros, so a row written that way sorts before a row written a
moment earlier, and a forward-only cursor then skips it permanently — which is
how every chat `/approve` wrote its re-issue trigger into a blind spot and no
held call was ever re-issued (BUGS `L97`).

## The one cron reader

`NextRun(cronVal, tz)` turns what a `Task.Cron` holds into the next instant it
names: a positive integer is an interval in milliseconds, an empty value names
nothing further (`ErrNoSchedule`), anything else is a 5-field cron expression
read in `tz`. `Timezone()` resolves that zone from `TZ`, UTC when it is unset or
unloadable.

Every writer of `scheduled_tasks.next_run` reads it — `store.SeedDefaultTasks`
for a new group's first run, timed's fire loop for the re-arm after each fire,
and routd's and dashd's task surfaces for a cron an operator typed. A seeder
that instead wrote `now` left five tasks due the moment a group was created, so
timed's next sweep fired all five (BUGS `L312`).

## Public API

- Config: `Config`, `LoadConfig`, `LoadConfigFrom`, `LoadInstanceEnv`,
  `SecretKeyring`, `RequireSecretKeyring`, `ErrNoSecretsKey`
- Env helpers: `EnvOr`, `EnvInt`, `DurOr`, `ParseCSV`
- Time: `TSLayout`, `TS`, `NowTS`
- Schedules: `NextRun`, `Timezone`, `ErrNoSchedule`
- Row types: `Message`, `Group`, `GroupConfig`, `Mount`, `Route`, `Task`,
  `SessionRecord`, `TopicLineage`, `PanePrompt`
- Routing: `RouteTarget`, `ParseRouteTarget` — parses `routes.target` as
  `<folder>[#<mode>]`
- JIDs: `JidRoom`, `JidPlatform`
- Ids: `GenHexToken`, `MsgID`, `NewSessionID`
- Names and URLs: `SanitizeInstance` (filesystem / `container_name` / unquoted
  YAML safe, ≤32 chars), `PublicBaseURL` (normalises `WEB_HOST` to a scheme —
  one place, never a per-call-site `https://` prefix, BUGS `F43`)
- Senders: `IsolatedSenderPrefix`, `IsIsolatedSender`, `DefaultProduct`
- Constants: `ContainerHome`, `DefaultImage`, `DefaultAPIPort`
- Errors: `ErrTopicExists`

## What it does not hold

- **No adapter interface.** An adapter satisfies `chanlib.BotHandler`, not
  anything here.
- **No accessors.** A type here is a shape; the reads and writes over it are
  `store`'s.
- **No validation of the egress vars.** They are on `Config` but only runed
  uses them, so runed validates them.

## Files

- `config.go` — `Config`, the three loaders, the env helpers
- `types.go` — the row types, JID parsing, id minting
- `timestamp.go` — `TSLayout` and the two renderers
- `cron.go` — `NextRun`, `Timezone`

## Related docs

- `ARCHITECTURE.md` § Where a change lands
