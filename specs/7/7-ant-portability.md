---
status: unshipped
---

# Ant portability — lockfile, arzpack, fleet skill ops

How an agent (folder + arizuko per-group state) gets exported,
shipped between instances, and how its layered skill set is
updated, drift-detected, and rolled back across N groups. The
verbs live in `arizuko/`; the per-folder mechanics live in ant as
library packages.

Sibling to [b-ant-standalone.md](../10/b-ant-standalone.md) (folder
shape) and [c-ant-mcp-runtime.md](../10/c-ant-mcp-runtime.md) (runtime
contract). This spec depends on neither's implementation details —
the lockfile and merge work apply to any folder that follows the
documented layout.

## Why one spec instead of five

Lockfile schema, `.arzpack` format, layer mechanics, operator
verbs, and migration handling are five interlocking pieces. Each
references the others (the lockfile carries `applied_migrations`;
arzpack imports update the lockfile; layer mechanics define what
`skill update` does; verbs operate on lockfile state). Splitting
them means N specs that all sit at "unshipped" until the last
lands. Keep them together so the file format and the algorithm
land as one understandable unit.

## Principles (informed by prior art)

Recorded from a survey of how mature systems handle the same
shape of problem (dpkg `ucf`, Helm `--three-way-merge`, kubectl
server-side apply, Flyway, Alembic, npm/pnpm/cargo lockfiles,
Backstage + cruft, OpenRewrite, Nix closures, OCI):

1. **The lockfile is the merge base.** Per-file content hashes
   recorded at install time let drift be classified offline as
   {upstream changed, local changed, both changed} — exactly
   dpkg `ucf`'s model. No registry reachability required at
   merge time.
2. **Migrations live inside the artifact, not next to the
   registry.** A skill's migration scripts ship inside the skill
   tarball, addressed by version pair. This is what preserves
   portability — the migration travels with the folder.
3. **Three-way text merge via `git merge-file`** is the default
   driver. Boring, deterministic, reviewable. Per-file, not
   per-folder. Conflict markers, then operator decides.
4. **Per-field merge for structured data** (`MCP.json`) borrows
   kubectl server-side-apply's managed-fields model — layers
   declare which JSON pointer paths they own.
5. **LLM-based semantic merge is an escalation, not a default.**
   Activates only when `git merge-file` conflicts on a prose file.
   Output is a `.llm-merge` proposal; never auto-committed. Off
   by default behind `--with-llm-merge`. v3 work, not v1.

## The lockfile — `.ant.lock.json`

Lives at `<folder>/.ant.lock.json`. Single source of truth for
what's installed and at what version. Schema-versioned (the
lockfile itself, separate from per-layer schema versions).

```jsonc
{
  "schema_version": 1,

  "base_template": {
    "source":   "git+https://github.com/arizuko/templates@v1.2#customer-support",
    "version":  "1.2.0",
    "owns":     ["SOUL.md", "CLAUDE.md", "MCP.json"],
    "hashes":   {"SOUL.md": "sha256:…", "CLAUDE.md": "sha256:…", "MCP.json": "sha256:…"},
    "applied_migrations": ["V1.0.0__V1.1.0", "V1.1.0__V1.2.0"]
  },

  "skills": {
    "commit": {
      "source":   "git+https://github.com/arizuko/ant-skills@v1.4#commit",
      "version":  "1.4.0",
      "owns":     ["skills/commit/SKILL.md", "skills/commit/scripts/*"],
      "installed_hash":     "sha256:…",
      "template_vars":      {"author": "ari"},
      "applied_migrations": ["V1.3.0__V1.4.0"]
    },
    "diary": { … }
  },

  "mcp_servers": {
    "linear": {
      "source":   "claude-plugin:linear-mcp",
      "version":  "0.3.1",
      "owns":     ["MCP.json#mcpServers.linear"],
      "templated_keys": ["api_token"]
    }
  }
}
```

Field semantics:

| Field                       | Meaning                                            | Update behavior                             |
| --------------------------- | -------------------------------------------------- | ------------------------------------------- |
| `source`                    | URI handled by `ant/pkg/sources`                   | Hint only; trust anchor is `hashes`         |
| `version`                   | Semver string from the source                      | Bumped on update; for display + diff        |
| `owns`                      | Files (or `path#json.pointer`) owned by this layer | Other layers/operators MUST NOT claim these |
| `hashes` / `installed_hash` | SHA-256 at install/last-merge time                 | The merge base for 3-way merge              |
| `template_vars`             | Variables substituted at install                   | Re-substituted on update from same source   |
| `applied_migrations`        | Migration script IDs applied in order              | Update runs new ones; never re-runs old     |

The `owns` list is authoritative. Files in the folder NOT listed
in any layer's `owns` are runtime state (diary/, workspace/, agent
artifacts) and are NEVER touched by updates.

## Optional manifest — `ant.toml`

Declarative bootstrap, optional. Lockfile is canonical; manifest
is convenience for reproducibility.

```toml
[base]
source = "git+https://github.com/arizuko/templates@v1.2#customer-support"

[[skills]]
name   = "commit"
source = "git+https://github.com/arizuko/ant-skills@v1.4#commit"
vars   = { author = "ari" }

[[skills]]
name   = "diary"
source = "claude-plugin:wisdom-skills"

[[mcp_servers]]
name   = "linear"
source = "claude-plugin:linear-mcp"
```

`ant materialize <folder>` reads the manifest, fetches sources,
writes the folder + lockfile from scratch — like `flake.nix` +
`flake.lock` or `package.json` + `package-lock.json`. Imperative
mutations (`arizuko skill add`) update the lockfile and, if a
manifest exists, append/update its entries.

## Source URIs

Resolved by handlers in `ant/pkg/sources/`. Initial set:

| Scheme                   | Example                                                 | Resolver                                     |
| ------------------------ | ------------------------------------------------------- | -------------------------------------------- |
| `git+https://…@ref#path` | `git+https://github.com/arizuko/ant-skills@v1.4#commit` | `git fetch` into CAS, copy subpath           |
| `file:///abs/path`       | `file:///home/ari/skills/foo`                           | Direct filesystem copy                       |
| `claude-plugin:<name>`   | `claude-plugin:wisdom-skills`                           | Read from `~/.claude/plugins/<name>/skills/` |

The lockfile records the URI but the SHA hashes are the trust
anchor. `ant remap <old-uri> <new-uri>` rewrites the URI after
verifying the new URI publishes the same `version` + `hashes`. This
is the registry-handoff story (analog: `go mod` GOPROXY failover,
npm registry mirrors).

Add `https://` (zip archive) or a real index later if a use case
arrives. v1 covers git + filesystem + claude plugin store.

## The update algorithm (per layer, per file)

```
for each file F owned by layer L being updated:
  base    = lockfile.L.hashes[F]                  (or installed_hash)
  current = sha256(file_on_disk(F))
  upstream = fetch source @ new version

  case (upstream==base, current==base):
      no-op (already in sync)

  case (upstream==base, current≠base):
      operator edited; keep local; warn

  case (upstream≠base, current==base):
      copy upstream; bump hash

  case (upstream≠base, current≠base, upstream==current):
      coincidental match; bump hash silently

  case (upstream≠base, current≠base, upstream≠current):
      3-way merge:
        if F is prose (.md, .txt): git merge-file base current upstream
            on clean merge: write result; bump hash
            on conflict:   write with conflict markers; surface to operator
                           if --with-llm-merge: also write F.llm-merge proposal
        if F is structured (.json, .yaml, .toml):
            if owned via JSON pointer (per-field): merge per kubectl server-side-apply
            else: emit F.conflict; halt this layer's update for this folder

then run any migration scripts in source's migrations/V<from>__V<to>.sh
in version order, recording them in applied_migrations.
```

Update is **per-layer atomic**: either every file in the layer
moves to the new version (modulo conflicts surfaced to the
operator), or the layer rolls back to its prior lockfile state.

## Layer conflict rules

Layers can collide if two want the same path. Resolution order
(later wins on `owns` claim):

1. `base_template` foundation
2. `skills` (user opted in; override base)
3. `mcp_servers` (override their MCP.json sections)
4. Local operator edits (recorded as drift on each owned file)

On `add`/`update`, ant validates the proposed file set against
existing `owns` and either yields (no conflict), or refuses with
`ErrLayerCollision` naming both claimants. Operators resolve
manually before retrying.

## The `.arzpack` format

`tar.gz` with `manifest.json` at the root + two payload sections:

```
agent-name.arzpack/
├── manifest.json           # ant + claude versions, schema versions,
│                            # required secrets, what's included
├── folder/                 # ant-folder bytes minus secrets, minus
│                            # absolute paths in workspace/ and
│                            # MCP.json (rewritten relative or marked
│                            # templated)
│   ├── SOUL.md
│   ├── CLAUDE.md
│   ├── MCP.json            # ${VAR} placeholders preserved
│   ├── skills/
│   ├── diary/
│   ├── workspace/
│   └── .ant.lock.json
├── arizuko/                # arizuko's per-group state
│   ├── channels.json       # channel mappings for this group
│   ├── schedule.json       # scheduled tasks targeting this group
│   ├── grants.json         # permission grants
│   └── store.jsonl         # any other per-group rows from the store
└── sessions/               # opt-in (--include-sessions)
    └── <session-id>.jsonl  # claude session transcripts
```

`manifest.json`:

```json
{
  "schema_version": 1,
  "ant_version": "0.3.0",
  "claude_version": "2.1.119",
  "arizuko_version": "0.34.0",
  "exported_at": "2026-05-15T12:00:00Z",
  "agent_name": "support-bot",
  "required_secrets": ["LINEAR_TOKEN", "SLACK_BOT_TOKEN"],
  "templated_paths": ["MCP.json#mcpServers.linear.config.api_token"],
  "includes": {
    "folder": true,
    "arizuko": true,
    "sessions": false
  },
  "content_hash": "sha256:…" // hash of folder/ + arizuko/ trees
}
```

Hash-signed for tamper detection. Optional GPG/sigstore signature
sidecar (deferred to a security-hardening pass; v1 trusts the
hash + transport).

Secrets are NEVER bundled. The manifest's `required_secrets` is
the operator's checklist on import.

## Operator verbs (arizuko)

All live in `arizuko/cmd/arizuko/`. Each composes
`ant/pkg/{lockfile, sources, skills, pack}` with arizuko's own
state serializer.

### Portability

```
arizuko export <group> [--include-sessions] [--diary-since DATE] \
                       [-o group.arzpack]
arizuko import <pack>  [--rewrite-channels=…] [--allow-missing-secrets] \
                       [--skills={keep|catchup}]
```

`--skills=keep` (default): preserve the lockfile's pinned versions
on the target (fidelity). `--skills=catchup`: after restore, run
`arizuko skill update` to bring imported groups in line with the
target instance's current skill versions (consistency). Both
choices are valid; making the operator pick avoids one being
surprising.

### Fleet skill ops

```
arizuko skill add    <name> [--source=URI] [--groups=…]
arizuko skill update [<name>] [--groups=…] [--dry-run]
arizuko skill remove <name> [--groups=…]
arizuko skill rollback <name> --to=<version> [--groups=…]
arizuko skill status [--groups=…] [--format=table|json]
```

`--groups=` accepts: a single group, a comma list, a glob, or
`all`. `--dry-run` reports the would-be classification (no-op,
upstream-change, local-edit, conflict) without writing anything.

### Template + remap

```
arizuko template upgrade [--groups=…]      # bump base_template
arizuko remap <old-uri> <new-uri>          # registry handoff;
                                            # validates hashes match
```

### Status report shape

```
$ arizuko skill status --format=table
SKILL              v1.2  v1.3  v1.4   EDITED  CONFLICTS
commit                3    12    27       2          0
diary                 0    42     0       1          0
recall-memories      18    24     0       0          1
```

This table is the operator's actual job. Build from per-folder
`.ant.lock.json` files; no separate state store.

## Code split

| Capability                       | Lives in                   | Used by                 |
| -------------------------------- | -------------------------- | ----------------------- |
| Parse/write lockfile             | `ant/pkg/lockfile`         | all the below           |
| Resolve source URI → bytes       | `ant/pkg/sources`          | `skills`, `pack`        |
| Compute drift, 3-way merge       | `ant/pkg/skills`           | `arizuko skill ...`     |
| Pack/unpack `.arzpack`           | `ant/pkg/pack`             | `arizuko export/import` |
| Materialize from manifest        | `ant/pkg/materialize`      | `arizuko init`          |
| Arizuko-half state serialization | `arizuko/internal/arzpack` | `arizuko export/import` |
| All fleet verbs                  | `arizuko/cmd/arizuko`      | operators               |

`ant/pkg/*` has zero arizuko imports (orthogonality rule). All
operator verbs are arizuko's CLI; ant ships no portability verbs of
its own (consistent with "ant is a runtime, not a verb proliferator"
from `c-`).

## Migration scripts inside the source

A skill source's repo layout (illustrative):

```
ant-skills/
├── commit/
│   ├── SKILL.md
│   ├── scripts/
│   └── migrations/
│       ├── V1.2.0__V1.3.0.sh
│       └── V1.3.0__V1.4.0.sh
├── diary/
│   ├── SKILL.md
│   └── migrations/
│       └── V1.0.0__V1.1.0.sh
```

`migrations/V<from>__V<to>.sh` (or `.py`, runtime-detected): a
shell or python script that mutates the installed copy from
version `from` to `to`. Idempotent, ordered, recorded in
`applied_migrations`. Run after file merge, before the lockfile's
version bump becomes durable.

Use cases:

- Frontmatter schema rename (`old_key` → `new_key`)
- File move (`scripts/old.py` → `scripts/new.py`)
- MCP.json shape bump

LLMs are explicitly NOT used for migrations. Schema changes are
mechanical; if a script can't express the transform, the
maintainer fixes the script. Boring beats clever.

## LLM-merge — explicit boundary

Optional, off-by-default escalation when `git merge-file` produces
conflict markers in a **prose-shaped** file (`.md`, `.txt`).
Activated by `--with-llm-merge` on `arizuko skill update`. Output:

- The conflict-marked file is left in place.
- A sidecar `<file>.llm-merge` is written with the proposed
  resolution + a header comment showing the three inputs.
- The operator reviews, copies, deletes the sidecar to accept.

LLM-merge is **never** applied to: `.json`, `.yaml`, `.toml`,
lockfiles, frontmatter blocks, code files (`.go`, `.py`, `.sh`,
`.ts`, …), files marked binary. Posture matches Cursor's "Apply"
and Aider's `/architect`: propose, don't commit. Production teams
do not run LLMs as unattended merge drivers; ant doesn't either.

When to add: not v1. Trigger is a real operator pain pattern
(repeated SKILL.md conflicts that 3-way merge mishandles). Until
then, the sidecar surface stays unspecified.

## Acceptance

- `arizuko export <group> -o foo.arzpack && arizuko import foo.arzpack -o /alt/data`
  round-trips a non-trivial group (≥ 5 skills, ≥ 1 MCP server,
  populated diary + workspace) producing byte-identical content
  after re-export from the alt instance.
- `arizuko skill update <name> --groups=all --dry-run` produces a
  drift report across 50+ groups in under 10s.
- `arizuko skill update <name> --groups=all` applies cleanly to ≥
  95% of groups in a real fleet, surfaces conflicts on the
  remainder, writes no data on conflict paths.
- `arizuko skill status` table renders for a fleet of 100 groups
  in under 2s.
- `arizuko remap` rewrites lockfile URIs and refuses if hashes
  don't match the new URI's resolution.
- All operations work fully offline once sources are cached
  (lockfile + CAS is enough).
- Zero arizuko imports in `ant/pkg/*` (verified by `go vet` /
  importgraph check).

## Out of scope

- Live sync between instances (diary appends from A streaming to
  B). Use git on the folder if needed; build a `sync` daemon only
  when a concrete use case justifies it.
- Codemod framework (comby / ast-grep integration). v2 if
  frontmatter changes become common.
- Per-field merge for arbitrary structured data. v2; v1 covers
  only `MCP.json` mcpServers map (the obvious case).
- Encrypted-at-rest packs. Target's filesystem encrypts; ant
  doesn't double-encrypt.
- Signed-by-publisher manifests (GPG/sigstore). Hash check is
  the v1 trust; signature layer added once a publisher actually
  exists.
- CRDT / multi-writer replication. If two arizuko instances both
  write to the same group folder simultaneously, use a real CRDT
  library; don't roll your own.

## Open questions

1. **Lockfile location collision**. `.ant.lock.json` in the
   folder root vs `.claude/ant.lock.json` to keep folder root
   clean. Pick one before code lands.
2. **MCP.json owners via JSON pointer**. `owns` claim of
   `MCP.json#mcpServers.linear` works if MCP.json is structured.
   Confirm the merge library handles JSON-pointer-based ownership
   (recommend `pkg/jsonpatch` or a thin custom mergerfunc).
3. **Skill source = repo subpath**. `git+…#commit` means
   subpath `commit` inside the source repo. Confirm the
   resolver's subpath semantics handle nested skills + monorepo
   sources cleanly.
4. **Migration script language**. Bash is portable; Python is
   richer. Allow both via shebang detection? Or pin one for v1?
5. **Pack signing scheme** when added: detached `.sig` next to
   `.arzpack` (cosign / minisign), or embedded in the manifest?

## Relation to other specs

- [b-ant-standalone.md](../10/b-ant-standalone.md) — folder layout the
  pack format mirrors.
- [c-ant-mcp-runtime.md](../10/c-ant-mcp-runtime.md) — runtime that
  consumes the folder; unaffected by this spec.
- [d-ant-image-cutover.md](../10/d-ant-image-cutover.md) — bind-mount
  layout for `~/.claude` ties into `--include-sessions` here.
- [3-template-distillation.md](../10/3-template-distillation.md) —
  producer side of templates; this spec is the consumer side.
- [../8/14-plugins.md](../8/14-plugins.md) — MCP-tool plugin
  layer; same "manifest + lockfile + verbs" shape, may share
  `pkg/lockfile`.
