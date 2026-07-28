# template

Instance seed files + bundled adapter TOMLs + Vite web scaffold.

## Purpose

Files copied into a new instance's data dir by `arizuko create` and by
`compose.Generate`. `services/` holds adapter TOMLs operators copy into
`<dataDir>/services/` to enable a channel. `web/` is a minimal Vite
scaffold that `vited` serves in dev.

## Contents

- `env.example` — default `.env` with every knob and a comment per var
- `services/*.yml` — bundled adapter packages (partial compose files):
  `teled`, `discd`, `slakd`, `mastd`, `bskyd`, `reditd`, `emaid`,
  `whapd`, `twitd`, `linkd`, `ttsd`, `kokoro`. A package with inbound
  web paths ships `<name>-routes.json` beside it (see `slakd`)
- `web/` — Vite project (`pub/`, `priv/`, `secret/` path regions,
  `vite.config.ts`, `package.json`)

## Usage

- `arizuko create <name>` seeds a new instance from `env.example`
- `arizuko packages <instance> add <name>` copies a package into
  `<dataDir>/services/` (`list` shows available vs enabled, `remove`
  deletes it)
- `compose.Generate` emits one `include:` per `<dataDir>/services/*.yml`;
  docker loads the fragments

## Related docs

- `ARCHITECTURE.md` (Compose Containers)
- `EXTENDING.md`
