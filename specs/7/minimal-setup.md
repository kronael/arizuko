# minimal-setup — deployment profiles

**Status**: design
**Problem**: the compose generator always includes proxyd, dashd, and timed. An operator
who wants just `gated + teled` must delete services manually after every `arizuko generate`.

---

## Nanoclaw mode

The original nanoclaw was a single process: one bot, one agent, no web layer.
Arizuko's microservice split makes that harder than it should be.

A minimal deployment must support:

- `gated` only (or `gated + one adapter`)
- no proxyd, no dashd, no timed
- no auth gate (no `WEB_HOST`, no OAuth)
- `AUTH_SECRET` still required (JWT signing); empty = auth disabled

The compose generator currently hardcodes gated, timed, and dashd as built-in services.
There is no mechanism to omit them short of post-generation editing.

---

## Service profile concept

A `PROFILE` env var selects which built-in services compose generates.
User-defined `services/*.toml` files are always included regardless of profile.

### Profiles

| Profile    | Built-in services generated                                             |
| ---------- | ----------------------------------------------------------------------- |
| `minimal`  | gated only                                                              |
| `standard` | gated + timed + proxyd + dashd                                          |
| `full`     | gated + timed + proxyd + dashd + onbod (if enabled) + davd (if enabled) |

Default: `full` (backward compatible — current behavior unchanged).

### Single-user implication

In `minimal` profile:

- `AUTH_SECRET` empty or absent → auth disabled in proxyd/dashd (those services are not generated anyway)
- `WEB_HOST` empty → proxyd not generated; dashd (if somehow added) serves without auth gate
- No `timed` → no scheduled tasks running; agents can still call `schedule_task` MCP tool,
  rows sit in `scheduled_tasks` but nothing polls them

### Example

```bash
# .env
PROFILE=minimal
TELEGRAM_BOT_TOKEN=...
CONTAINER_IMAGE=arizuko-agent:latest
AUTH_SECRET=...
```

`arizuko generate REDACTED` writes a compose with only `arizuko_gated_REDACTED` and
whichever adapter service files exist in `services/teled.toml`.

---

## Open questions

1. **PROFILE env vs explicit service list**: `PROFILE=minimal` is a named preset.
   Alternative: `COMPOSE_SERVICES=gated,teled` as an explicit list.
   Named presets are easier to document and explain; explicit list is more flexible.
   Recommendation: named presets now, explicit override later if needed.

2. **Pre-composed minimal template**: ship a `template/docker-compose.minimal.yml`
   that operators can copy directly? Simpler but diverges from `arizuko generate`.
   Probably not worth the maintenance burden.

3. **timed always-on question**: if `timed` is omitted in minimal, scheduled tasks
   created via MCP accumulate silently. Should gated warn at startup when
   `scheduled_tasks` has active rows but `PROFILE=minimal`?

---

## Recommendation

Add `PROFILE` env var support to `compose/compose.go`:

1. Read `PROFILE` from config (default `full`)
2. `minimal`: omit timed, proxyd, dashd from built-in set
3. `standard`: include timed, proxyd, dashd; omit onbod and davd regardless of flags
4. `full`: current behavior (all built-ins, onbod when enabled, davd when enabled)
5. User-defined `services/*.toml` always appended regardless of profile

This is a pure compose-generation change — no runtime behavior affected.
Existing deployments unaffected (default is `full`).
