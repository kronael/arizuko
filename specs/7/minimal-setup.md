# minimal-setup — deployment profiles

**Status**: implemented (`IMPULSE_ENABLED` in gated; `PROFILE` in compose generator)

---

## What's already opt-in (no code changes needed)

Most features are disabled by omission — don't set the env var, don't run the daemon:

| Feature             | How to disable                                               |
| ------------------- | ------------------------------------------------------------ |
| Channel auth        | omit `CHANNEL_SECRET` → any adapter registers freely         |
| Web/dashboard auth  | omit `AUTH_SECRET` → dashd/proxyd pass all requests          |
| OAuth               | omit `*_CLIENT_ID` → login page shows only username/password |
| Scheduled tasks     | don't run `timed`                                            |
| Onboarding          | omit `ONBOARDING_ENABLED=true`                               |
| WebDAV              | omit `WEBDAV_ENABLED=true`                                   |
| Social adapters     | don't run mastd/bskyd/reditd                                 |
| Dashboard           | don't run dashd                                              |
| Web proxy/auth gate | don't run proxyd                                             |

## Absolute minimum

```bash
# .env
TELEGRAM_BOT_TOKEN=...
CONTAINER_IMAGE=arizuko-agent:latest
```

Run: `gated` + `teled`. No compose. No auth. No dashboard. No scheduler.
One bot, one agent — the nanoclaw equivalent.

## Feature flags (gated env vars)

| Flag                            | Default | Effect when changed                                               |
| ------------------------------- | ------- | ----------------------------------------------------------------- |
| `IMPULSE_ENABLED=false`         | `true`  | skip weight-based batching; every message fires agent immediately |
| `ONBOARDING_ENABLED=true`       | `false` | route unregistered JIDs through onboarding flow                   |
| `ONBOARDING_PLATFORMS=telegram` | all     | restrict onboarding to listed platforms                           |
| `MEDIA_ENABLED=true`            | `false` | enable media transcription via whisper                            |

**Note on impulse**: with default weights (threshold=100, message weight=100), the
gate is already transparent for normal messaging — every message fires immediately.
It only batches social verb events (join/edit/delete, weight=0). `IMPULSE_ENABLED=false`
is an explicitness flag, not a behavior change for standard Telegram/Discord usage.

## PROFILE for compose generation

```bash
PROFILE=minimal   # gated only (no adapter — add teled/discd via services/*.toml)
PROFILE=web       # gated + proxyd + vited  (WEB_PORT required)
PROFILE=standard  # gated + timed + proxyd + vited
PROFILE=full      # all built-ins + dashd + onbod/davd when enabled  ← default
```

`PROFILE` gates which built-in services `arizuko generate` emits.
User-defined `services/*.toml` are always included regardless of profile.

Existing deployments unaffected: default remains `full`.

Implemented in `compose/compose.go`: reads `PROFILE` from instance `.env`, gates
built-in service generation. `dashd` only emitted for `full`.

## LOC per profile (production code, excl. tests)

These are the Go source lines compiled into each profile's running processes.
Shared packages counted once per profile.

| Package   | nanoclaw  | web       | standard  | full       |
| --------- | --------- | --------- | --------- | ---------- |
| core      | 302       | 302       | 302       | 302        |
| store     | 1,349     | 1,349     | 1,349     | 1,349      |
| gateway   | 1,617     | 1,617     | 1,617     | 1,617      |
| gated     | 80        | 80        | 80        | 80         |
| router    | 320       | 320       | 320       | 320        |
| container | 1,139     | 1,139     | 1,139     | 1,139      |
| queue     | 398       | 398       | 398       | 398        |
| api       | 282       | 282       | 282       | 282        |
| chanreg   | 409       | 409       | 409       | 409        |
| chanlib   | 167       | 167       | 167       | 167        |
| teled     | 528       | 528       | 528       | 528        |
| proxyd    | —         | 407       | 407       | 407        |
| auth      | —         | 984       | 984       | 984        |
| timed     | —         | —         | 368       | 368        |
| dashd     | —         | —         | —         | 413        |
| grants    | —         | —         | —         | 240        |
| ipc       | —         | —         | —         | 881        |
| notify    | —         | —         | —         | 13         |
| onbod     | —         | —         | —         | 577        |
| **Total** | **6,591** | **8,982** | **9,350** | **11,474** |

**Goal**: bring nanoclaw profile to ~500 LOC. Currently 6,591. The heaviest
packages for a simple bot are `store` (1,349), `gateway` (1,617), and `container`
(1,139) — all loaded unconditionally by `gated`.

## Open design questions

### 1. Which gated features could move elsewhere?

`gated` currently bundles: message loop, container runner, MCP sidecar wiring,
queue management, channel registry proxy, and the full HTTP API. For a truly
minimal deployment only the message loop + container exec matter.

Candidates for extraction:

| Feature           | Currently in | Could move to           | Impact on nanoclaw LOC |
| ----------------- | ------------ | ----------------------- | ---------------------- |
| Container runner  | `container/` | standalone `rund`       | −1,139                 |
| Channel registry  | `chanreg/`   | standalone `chregdd`    | −409                   |
| HTTP API server   | `api/`       | standalone `apid`       | −282                   |
| Queue concurrency | `queue/`     | inline in gateway       | 0 (already linked)     |
| IPC/MCP sidecar   | `ipc/`       | per-container sidecar   | 0 (already excluded)   |
| Thread routing    | `store/`     | prune unused store cols | −200 est.              |

Extracting `container/` + `chanreg/` + `api/` into separate daemons could
bring the nanoclaw gateway binary to ~2,500 LOC. Reaching ~500 would require
also simplifying `store/` to a minimal read/write surface.

### 2. Minimal store surface for nanoclaw

The full `store` (1,349 LOC) includes: auth, grants, tasks, sessions, onboarding,
thread routing, and slink tokens — none needed for a single-user bot.

A `minstore` or `store/lite` interface exposing only:

- `PutMessage` / `GetPendingMessages`
- `GetRoutes`
- `GetOrCreateGroup`

...would be ~200 LOC. The rest are dead weight for minimal deployments.

### 3. Platform profile (future)

Beyond `full`, a `platform` profile for multi-tenant/SaaS deployments would add:

- **Metering** — per-user/per-group token and request counts
- **Limits** — rate limits, daily caps, hard stops enforced in gateway
- **Billing hooks** — emit metering events to external system
- **Admin UI** — beyond dashd; user management, limit overrides

This is not yet specced. Placeholder: `PROFILE=platform` triggers these additions
when they exist. `full` remains the default for self-hosted operator use.
