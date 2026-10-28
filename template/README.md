# template

Instance seed files, bundled adapter packages and Vite web scaffold.

## Purpose

Files copied into a new instance's data dir by `arizuko create` and by
`compose.Generate`. `services/` holds bundled adapter packages that
`arizuko package add` selects. `web/` is a minimal Vite
scaffold that `vited` serves in dev.

## Contents

- `env.example` — default `.env` with every knob and a comment per var
- `services/<name>/` — bundled adapter packages (partial compose files):
  `teled`, `discd`, `slakd`, `emaid`,
  `whapd`, `ttsd`, `kokoro`, `litellm`. A package that
  needs anything from the system ships `arizuko.yml` beside it — its package
  manifest: a `proxyd_routes` block for inbound web paths (see `slakd`),
  an `env` block for what its process needs from `.env` (see `litellm`,
  whose virtual key is minted at install, and `ttsd`, whose defaults turn
  TTS on for runed). `litellm` is the model gateway: a
  third-party image whose config is inline in the fragment and whose
  credential is interpolated from `.env`
- `web/` — Vite project (`pub/`, `priv/`, `secret/` path regions,
  `vite.config.ts`, `package.json`)

## Usage

- `arizuko create -i <name>` writes a new instance's `.env`. `env.example`
  stays the operator's reference for the remaining settings.
- Select a catalog source with `arizuko package add -i <name> catalog:<name>`;
  the command installs its fragment, provisions service environment needs and
  applies its rows.
- `compose.Generate` emits one `include:` per `<dataDir>/services/*.yml`;
  docker loads the fragments

## Related docs

- `../compose/README.md` — the compose generator and the package path
- `EXTENDING.md`
