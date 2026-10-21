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
  `teled`, `discd`, `slakd`, `bskyd`, `emaid`,
  `whapd`, `ttsd`, `kokoro`, `litellm`. A package that
  needs anything from the system ships `<name>.yaml` beside it — its package
  manifest: a `proxyd_routes` block for inbound web paths (see `slakd`),
  an `env` block for what its process needs from `.env` (see `litellm`,
  whose virtual key is minted at install, and `ttsd`, whose defaults turn
  TTS on for runed). `litellm` is the model gateway: a
  third-party image whose config is inline in the fragment and whose
  credential is interpolated from `.env`
- `web/` — Vite project (`pub/`, `priv/`, `secret/` path regions,
  `vite.config.ts`, `package.json`)

## Usage

- `arizuko create <name>` writes a new instance's `.env` from its own key
  set — the generated secrets and the product choice. It writes no egress
  switch, because every turn runs behind the egress proxy. It does not read
  `env.example`, which stays the operator's reference for every remaining knob
- `arizuko packages <instance> add <name>` links a package into
  `<dataDir>/services/`, provisions its `env` needs and applies its rows
  (`list` shows available vs enabled, `remove` withdraws exactly what the
  record names)
- `compose.Generate` emits one `include:` per `<dataDir>/services/*.yml`;
  docker loads the fragments

## Related docs

- `../compose/README.md` — the compose generator and the package path
- `EXTENDING.md`
