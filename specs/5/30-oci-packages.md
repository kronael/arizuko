---
status: superseded
relates-to: [20-ant-portability, 21-products, 27-compose-native-packaging]
---

# OCI packages — cross-layer distribution artifacts

A package is an OCI artifact that spans the full arizuko stack: daemon
fragments (compose services), group folder templates (products), routes,
grants, and skills — distributed as one versioned, signable, registry-hosted
unit.

`spec/5/20` defines state transport and product mixins.
`spec/5/21` defines the product format (PRODUCT.md, persona, skills).
`spec/5/27` defines compose-native service fragments.
This spec defines how those pieces travel as a single OCI artifact.

## Why OCI

- Registry, versioning, signatures: `ghcr.io` is the package registry, free.
- Pull = `oras pull` or one Go function call; no custom server.
- Annotations carry metadata; layers carry files — no new manifest format.
- `oras-go/v2` is ~3 MB, no docker daemon needed at install time.

## Package anatomy

An arizuko package is an OCI artifact (not a runnable image):

```
Manifest annotations:
  arizuko.name      = "teled"
  arizuko.version   = "1.2.0"
  arizuko.requires  = "TELEGRAM_BOT_TOKEN"      (comma-separated)
  arizuko.min       = "0.47.0"                  (min arizuko version)

Layers (each a tar.gz, mediaType custom per component):
  vnd.arizuko.compose.v1      → services/<name>.yml     (optional)
  vnd.arizuko.product.v1      → groups/<name>/           (optional)
  vnd.arizuko.routes.v1       → POST /v1/routes on install
  vnd.arizuko.grants.v1       → POST /v1/acl on install
  vnd.arizuko.skills.v1       → ant/skills/<name>/       (optional)
  vnd.arizuko.web.v1          → web/pub/<name>/          (optional)
```

A package may include any subset of layers. An adapter-only package ships
only the compose layer. A product-only package ships product + skills.
A full package ships all six.

## Install flow

```
arizuko packages install ghcr.io/kronael/teled:latest
```

1. Pull artifact via oras-go/v2 (`remote.NewRepository` + `oras.Copy`).
2. Read annotations: check `arizuko.min`, surface missing `requires`.
3. For each layer by mediaType:
   - `compose` → extract to `<datadir>/services/<name>.yml`
   - `product` → seed `<datadir>/groups/<name>/` (refuse if exists, unless `--force`)
   - `routes` → `POST routd:8080/v1/routes` (skip if routd not reachable)
   - `grants` → `POST routd:8080/v1/acl`
   - `skills` → extract to agent image path (or `<datadir>/skills/<name>/`)
   - `web` → extract to `<datadir>/web/pub/<name>/`
4. Regenerate compose (`arizuko run` equivalent: write include: entry).
5. Print: installed layers, missing secrets to set before first run.

## Publish flow

```
arizuko packages publish ./my-package ghcr.io/myorg/my-package:1.0.0
```

Reads `<dir>/package.toml` for metadata, tars each subdirectory with its
mediaType, pushes via oras-go/v2.

`package.toml` (minimal):

```toml
name    = "teled"
version = "1.0.0"
requires = ["TELEGRAM_BOT_TOKEN"]
min     = "0.47.0"

[layers]
compose = "compose.yml"
product = "groups/teled/"
routes  = "routes.json"
grants  = "grants.json"
skills  = "skills/"
```

## Go implementation (oras-go/v2)

Dependency: `oras.land/oras-go/v2` + `oras.land/oras-go/v2/remote`.
No containerd, no docker daemon.

```go
// Pull
repo, _ := remote.NewRepository("ghcr.io/kronael/teled:latest")
store    := memory.New()
desc, _  := oras.Copy(ctx, repo, "latest", store, "", oras.DefaultCopyOptions)

// Read manifest + annotations
var manifest ocispec.Manifest
rc, _ := store.Fetch(ctx, desc); json.NewDecoder(rc).Decode(&manifest)
name     := manifest.Annotations["arizuko.name"]
requires := strings.Split(manifest.Annotations["arizuko.requires"], ",")

// Extract layers
for _, layer := range manifest.Layers {
    rc, _ := store.Fetch(ctx, layer)
    dest   := destFor(layer.MediaType, dataDir, name)
    extractTar(rc, dest)
}
```

Push mirrors this: `file.New()` store → `oras.Pack()` manifest →
`oras.Copy()` to remote.

## Relation to existing specs

- **5/20 state transport** — `export/apply` moves a live agent (with state).
  OCI packages distribute templates (no state). They meet at restore time:
  `apply` reinstalls packages from the lock before seeding state.
- **5/21 products** — a product's `PRODUCT.md`/`PERSONA.md`/`skills/` become
  the `product` layer. OCI is the distribution envelope; the product format
  is unchanged.
- **5/27 compose packaging** — the compose fragment (`.yml`) becomes the
  `compose` layer. The `arizuko packages` CLI gains `install/publish` verbs
  alongside list/add/remove.

## What does NOT travel in a package

- Secrets / tokens (annotations declare `requires`; operator sets them post-install)
- State (conversation history, diary, session — use `arizuko export`)
- Instance-specific config (`.env`, `CHANNEL_SECRET`)

## Code pointers

- `cmd/arizuko/packages.go` — new: `install`, `publish`, `list`, `add`, `remove`
- `compose/packages/` — new: oras-go wrapper, layer mediaTypes, tar extract
- `go.mod` — add `oras.land/oras-go/v2`
