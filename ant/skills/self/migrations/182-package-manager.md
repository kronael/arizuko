## package manager — `arizuko packages install | upgrade | remove`

**Operator feature, no agent action.** Instances can now install packages (spec
`5/28`): a git source or local dir shipping compose fragments, proxyd routes,
grants, and skills as one versioned unit.

- `arizuko packages <inst> install github.com/org/pkg` — resolves an immutable
  revision, writes the files, hot-applies routes/grants to the live DB (no
  restart), layers any package skills into every group, and records an
  installed-package record (source + revision + owned identities + per-asset hash).
- `upgrade` refuses a locally edited (dirty) asset instead of overwriting it.
- `remove` deletes exactly what the record owns (routes withdrawn before fragments).

Package skills land in `<datadir>/skills/<name>/` and seed into every group like
stock skills. Spec `5/28`.
