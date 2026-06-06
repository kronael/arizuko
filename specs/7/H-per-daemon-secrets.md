---
status: shipped
---

# specs/7/H — Per-daemon channel secrets

## What this solves

All channel adapters share a single `CHANNEL_SECRET` bearer to register
with gated and authenticate inbound deliveries. If one adapter's config
leaks (a Slack app manifest, a misconfigured systemd drop-in, a CI log
with the env dumped), every other adapter's authentication is
compromised because they all carry the same bearer.

Per-daemon overrides let operators rotate or isolate the secret for one
platform without touching the others.

## Design

Each adapter reads `<DAEMON>_CHANNEL_SECRET` first, falling back to
`CHANNEL_SECRET`. `arizuko create` keeps generating one shared
`CHANNEL_SECRET`; the per-adapter vars are optional overrides.

gated does not change. It already accepts any registered channel's
bearer — the token returned from `/v1/channels/register` is what
gates subsequent traffic; the registration step accepts whatever
bearer the adapter presents and binds it to that channel record.

## Env vars

| Adapter | Var                     |
| ------- | ----------------------- |
| slakd   | `SLAKD_CHANNEL_SECRET`  |
| teled   | `TELED_CHANNEL_SECRET`  |
| discd   | `DISCD_CHANNEL_SECRET`  |
| emaid   | `EMAID_CHANNEL_SECRET`  |
| mastd   | `MASTD_CHANNEL_SECRET`  |
| bskyd   | `BSKYD_CHANNEL_SECRET`  |
| reditd  | `REDITD_CHANNEL_SECRET` |
| linkd   | `LINKD_CHANNEL_SECRET`  |
| whapd   | `WHAPD_CHANNEL_SECRET`  |
| twitd   | `TWITD_CHANNEL_SECRET`  |

Each falls back to `CHANNEL_SECRET` when unset.

## Migration

Existing deployments: no change. `CHANNEL_SECRET` continues to work for
every adapter.

New deployments: `arizuko create` generates `CHANNEL_SECRET` as before.
Operators set the per-adapter override only when they want isolation
(typically after a suspected leak or for high-blast-radius channels).

## What changed

- `slakd/main.go`, `teled/main.go`, `discd/main.go`, `emaid/main.go`,
  `mastd/main.go`, `bskyd/main.go`, `reditd/main.go`, `linkd/main.go`
  read `<DAEMON>_CHANNEL_SECRET` with fallback to `CHANNEL_SECRET`.
- `whapd/src/main.ts`, `twitd/src/main.ts` do the same in TS.
- Per-adapter READMEs document the new var.
- `template/web/pub/reference/env.html` adds entries for every
  per-adapter override.
