---
status: shipped
supersedes: [4/Y-minimal-setup.md]
---

# compose: native Docker Compose profiles and includes

**DECISION: use Docker Compose's built-in machinery instead of
re-implementing it.** `profiles:` for optional daemons, `include:` for
channel adapters. The custom `PROFILE=minimal|web|full` enum and the
hand-parsed `template/services/*.toml` mini-format are both retired.

Why: a custom TOML mini-format meant `compose.go` owned a parser, a
per-adapter Go builder function, and a YAML emitter for something Docker's
own YAML loader already does. Every new adapter cost Go code. Now a channel
adapter ships `template/services/<name>.yml` — a valid partial compose
service that Docker `include`s verbatim — plus an optional
`<name>-routes.json` ([`7-proxyd-standalone.md`](7-proxyd-standalone.md)).
**Adding an adapter touches no Go.**

## Opt-in by omission

Profiles are the mechanism; the stance behind them predates them and
survives unchanged: **a feature is off because its daemon is not running or
its env var is not set — not because a flag says `false`.** Onboarding is
off until `ONBOARDING_ENABLED=true`, WebDAV until `WEBDAV_ENABLED`, media
transcription until `MEDIA_ENABLED`, the web bundle until `WEB_PORT`.
`activeProfiles(env)` (`compose/compose.go:451`) reads exactly those vars
and derives `COMPOSE_PROFILES`; there is no second switch.

The floor of a deployment is the core plane plus one channel adapter
(ARCHITECTURE.md "Core vs integrations"). Everything above that floor is a
profile, and adding one is adding an env var — never editing `compose.go`.

This is why per-feature boolean flags stay out of the compose layer: they
would be a second gate that drifts from the first. The retired
`PROFILE=minimal|web|full` enum was exactly that drift — three hardcoded Go
paths encoding combinations the env vars already described.

## Known gap

**C2 — package removal left orphan proxyd routes.** Resolved by
[`5/28`](28-packages.md): the installed-package record names the
`proxyd_routes` rows a package owns, and `packages remove` deletes them
through proxyd's handler rather than guessing at ownership.

## Code pointers

- `compose/compose.go` — `activeProfiles` (:417), profile emission
  (:811-813, :1024, :1055), the `include:` block (:687).
- `template/services/*.yml` — the adapter fragments Docker includes.
- `cmd/arizuko/packages.go` — the `list`/`add`/`remove` verbs over them.

Optional core daemons (`timed`, `onbod`, `davd`) get their `profiles:` key
emitted inline by `compose.go`, not from a `template/services/` fragment —
they are core, not includable packages.
