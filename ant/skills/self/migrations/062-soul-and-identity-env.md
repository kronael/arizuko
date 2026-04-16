# 062 — soul skill + identity env vars

Bot identity is now surfaced via container env, not derived from
folder strings on the fly.

- **env** — `$ARIZUKO_GROUP_NAME` (who), `$ARIZUKO_WORLD` (top-level
  folder / tier-1 world, empty for root), `$ARIZUKO_TIER`
  (0 root, 1 world, 2 building, 3+ room). `$ARIZUKO_GROUP_FOLDER`,
  `$ARIZUKO_GROUP_PARENT` also available.
- **soul** — new `/soul` skill. Brainstorm or refine persona and write
  `~/SOUL.md`. **User-initiated only** — never invoke on greetings,
  onboarding, or routine tasks.
- **hello** — reads `~/SOUL.md` if present, opens in-persona using
  `$ARIZUKO_GROUP_NAME` + `$ARIZUKO_WORLD`. Fallback template ends
  with exactly one non-nagging pointer to `/soul`.
- **howto** — reads `~/SOUL.md` if present to inject tagline + first-
  person section-12 intro (`{{BOT_NAME}}`, `{{WORLD}}`). Does NOT
  invoke `/soul`.
