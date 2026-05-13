# template

Instance seed files + bundled adapter TOMLs + Vite web scaffold.

## Purpose

Files copied into a new instance's data dir by `arizuko create` and by
`compose.Generate`. `services/` holds adapter TOMLs operators copy into
`<dataDir>/services/` to enable a channel. `web/` is a minimal Vite
scaffold that `vited` serves in dev.

## Contents

- `env.example` — default `.env` with every knob and a comment per var
- `services/*.toml` — bundled adapter specs: `teled`, `discd`, `slakd`,
  `mastd`, `bskyd`, `reditd`, `emaid`, `whapd`, `twitd`, `linkd`
- `web/` — Vite project (`pub/`, `priv/`, `secret/` path regions,
  `vite.config.ts`, `package.json`)

## Usage

- `arizuko create <name>` seeds a new instance from `env.example`
- Operator copies desired `services/*.toml` into `<dataDir>/services/`
  before `arizuko run`
- `compose.Generate` appends any `<dataDir>/services/*.toml` to the
  generated compose file

## Related docs

- `ARCHITECTURE.md` (Compose Containers)
- `EXTENDING.md`
