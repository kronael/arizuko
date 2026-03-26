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

## PROFILE for compose generation (not yet implemented)

```bash
PROFILE=minimal   # gated only
PROFILE=standard  # gated + timed + proxyd + dashd  (default if unset was full)
PROFILE=full      # all built-ins + onbod/davd when enabled  ← current default
```

`PROFILE` gates which built-in services `arizuko generate` emits.
User-defined `services/*.toml` are always included regardless of profile.

Existing deployments unaffected: default remains `full`.

Implemented in `compose/compose.go`: reads `PROFILE` from instance `.env`, gates
built-in service generation. `standard` omits onbod/davd regardless of their flags.
