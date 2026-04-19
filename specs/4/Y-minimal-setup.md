---
status: shipped
---

# Minimal Setup — Deployment Profiles

`PROFILE` gates which built-in services `arizuko generate` emits.
User-defined `services/*.toml` are always included.

```bash
PROFILE=minimal   # gated only (add adapter via services/*.toml)
PROFILE=web       # gated + proxyd + vited
PROFILE=standard  # gated + timed + proxyd + vited
PROFILE=full      # all built-ins + dashd + onbod/davd  (default)
```

Implemented in `compose/compose.go`: reads `PROFILE` from instance
`.env`. Existing deployments unaffected.

## Opt-in by omission

Most features are disabled by omitting an env var or not running the
daemon:

| Feature             | Disable                                                      |
| ------------------- | ------------------------------------------------------------ |
| Channel auth        | omit `CHANNEL_SECRET` → any adapter registers freely         |
| Web/dashboard auth  | omit `AUTH_SECRET` → dashd/proxyd pass all requests          |
| OAuth               | omit `*_CLIENT_ID` → login page shows username/password only |
| Scheduled tasks     | don't run `timed`                                            |
| Onboarding          | omit `ONBOARDING_ENABLED=true`                               |
| Social adapters     | don't run mastd/bskyd/reditd                                 |
| Dashboard           | don't run dashd                                              |
| Web proxy/auth gate | don't run proxyd                                             |

## Feature flags

| Flag                            | Default | Effect                                                            |
| ------------------------------- | ------- | ----------------------------------------------------------------- |
| `IMPULSE_ENABLED=false`         | `true`  | skip weight-based batching; every message fires agent immediately |
| `ONBOARDING_ENABLED=true`       | `false` | route unregistered JIDs through onboarding flow                   |
| `ONBOARDING_PLATFORMS=telegram` | all     | restrict onboarding to listed platforms                           |
| `MEDIA_ENABLED=true`            | `false` | enable media transcription via whisper                            |

Impulse: with default weights (threshold=100, message=100) every
message fires immediately. Only social verb events (join/edit/delete,
weight=0) batch. `IMPULSE_ENABLED=false` is an explicitness flag for
standard usage.

## Absolute minimum

```
TELEGRAM_BOT_TOKEN=...
CONTAINER_IMAGE=arizuko-agent:latest
```

Run `gated` + `teled`. No compose. No auth. No dashboard. One bot,
one agent.
