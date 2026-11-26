---
status: shipped
---

# Each group carries one harness document

A harness document selects the backend, model and effort inside ant.

Opaque storage, HTTP carry, ant validation, whole-document replacement,
Codex effort and model-credential delivery have implementation tests in
`routd/harness_carry_test.go`, `ant/src/harness-main.test.ts`,
`routd/groups_detail_test.go`, `store/groups_test.go` and `runed/mounts_test.go`.
The carry after a bulk import is `TestApplyRows_CarriesHarnessDocument`
(`cmd/arizuko/apply_harness_test.go`). The dashboard form is in
`dashd/chat_settings_test.go`. An anteval case names its group's harness with the
case field `harness` (`anteval/pkg/spec/spec.go`): the runner writes the document
before the case's first turn and clears it after, a case whose backend is `codex`
needs the Codex login (`HOST_CODEX_DIR`) and the hosts `chatgpt.com` and
`auth.openai.com`, and the eval bearer gains `routes:write:own_group` only for a run
with such a case (`anteval/cmd/anteval/lifecycle.go`).

`anteval/backends/backends.toml` holds a case for the default backend, for pi and
for codex. On 2026-11-25 the three passed on one disposable instance of v0.75.28
whose default backend was codex; §"Contract" holds the measurement. The suite
holds no claude case: a disposable run must not copy an instance's credential,
and the live fleet runs claude beside pi (BUGS record `L951`).

## The platform must carry the document without interpreting it

`store/groups.go` stores the document; `routd/dispatch.go` copies it into
`RunRequest.Harness`; `runed/render.go` emits `ARIZUKO_HARNESS`.
The backend switch belongs in `ant/src/backend.ts`, with validation before
adapter startup. Go does not interpret backend names, models or effort.

## One opaque value reaches ant

```text
dashd store write (changed)    groups update / derived MCP (new)
              │                       │
              └── harness document (new) ──┘
                              ▼
                     groups.config.harness (new)
                              │ raw JSON (new)
                              ▼
                      routd carry (changed)
                              │ RunRequest.harness (new)
                              ▼
                      runed render (changed) ◄── instance env
                              │ ARIZUKO_HARNESS (new) + default env
                              ▼
                       ant parse (changed)
                              │ selected adapter
                              ▼
                     Claude / Codex / Pi

routd default resolution (removed)
RunRequest.Model / Effort (removed)
Go effort vocabulary (removed)
```

### Module map

The map names production changes.
No Go type declares the document's backend, model or effort fields.

```yaml
- path: core/types.go (changed)
  job: expose the opaque group document for reads
  names: [Group.Harness]
  never: interpret harness fields
- path: store/groups.go (changed)
  job: read and replace the raw harness setting while preserving other settings
  names: [GroupConfig, SetGroupHarness]
  never: choose defaults or validate backend semantics
- path: routd/db.go (changed)
  job: forward the opaque store read and its errors
  names: [GroupConfig]
  never: resolve defaults
- path: routd/dispatch.go (changed)
  job: copy the document into the run request
  names: [RunRequest.Harness]
  never: inspect the document or gate model credentials by backend
- path: routd/loop.go (changed)
  job: remove model and effort defaults from router state
  names: [LoopConfig]
  never: hold harness defaults
- path: routd/cmd/routd/main.go (changed)
  job: remove the router's harness environment readers
  names: [main]
  never: choose a model
- path: runed/api/v1/types.go (changed)
  job: carry one raw JSON value
  names: [RunRequest.Harness]
  never: declare backend fields
- path: runed/api/v1/client.go (changed)
  job: encode the request with json.Marshal
  names: [Client.Run]
  never: decode the raw value
- path: core/config.go (changed)
  job: load the instance defaults as an environment map
  names: [Config.AgentEnv, LoadConfig]
  never: validate backend names or effort levels
- path: compose/compose.go (changed)
  job: send instance defaults to runed
  names: [Generate]
  never: send harness defaults to routd
- path: runed/render.go (changed)
  job: emit instance env and the raw group document
  names: [Turn.Env]
  never: merge group fields with defaults
- path: ant/src/backend.ts (changed)
  job: parse and validate the selected document
  names: [selectHarness, Turn]
  never: accept a partial merge with instance defaults
- path: ant/src/index.ts (changed)
  job: pass the validated selection to the adapter and report errors
  names: [runTurn]
  never: start an adapter after configuration failure
- path: ant/src/codex.ts (changed)
  job: forward effort to the native CLI
  names: [codexArgs]
  never: choose an effort silently
- path: resreg/resources/groups.go (changed)
  job: declare the update action and preserve the raw settings document
  names: [GroupsRow, GroupsAgentEndpoints, GroupsMCPArgs]
  never: declare a backend or model catalogue
- path: routd/groups_resource.go (changed)
  job: handle the shared update and bind MCP authorization to its folder
  names: [groupsHandler]
  never: add a bespoke MCP setter
- path: routd/groups_http.go (changed)
  job: bind the update's REST authorization to its target
  names: [Server]
  never: authorize a write with read scope
- path: dashd/groups_admin.go (changed)
  job: edit backend, model and effort as fields and one "Use instance defaults" control
  names: [handleGroupSettingsSave]
  never: validate backend, model or effort values, or send the write through routd
- path: store/acl.go (changed)
  job: remove codex from accepted grant actions after approval
  names: [ACLActionChoices, ValidateACLAction]
  never: classify model login as a capability grant
- path: runed/mounts.go (changed)
  job: deliver the host login under the model-credential policy
  names: [buildMounts]
  never: consult the harness document
```

### Ant owns selection and validation

The setting is one JSON value under `groups.config.harness`:

```json
{ "harness": { "backend": "codex", "model": "gpt-6-astra", "effort": "high" } }
```

An absent setting means instance defaults. A present setting replaces the
defaults as a whole, including when its backend matches the instance backend.
There is no parent-folder lookup. A missing optional model or effort leaves
the selected adapter's native default; neither value inherits from the instance.

### Two levels set the harness, and nothing else does

```text
instance .env ──► ARIZUKO_BACKEND, ARIZUKO_DEFAULT_MODEL, ARIZUKO_DEFAULT_EFFORT
                      │ a group without a document
                      ▼
groups.config.harness ──► one document; it replaces the three defaults whole
```

The instance defaults are infrastructure configuration and stay in `.env`.
The group document is the only setting below them. These have no reader:

- a top-level `model` or `effort` key in a group's `config`;
- an ACL row with the action `codex`: `store.ValidateACLAction` refuses a new
  one, and `arizuko group ungrant` removes a stored one;
- the document of a parent folder. Every document on krons and sloth is on a
  folder with no child (`krons/public/marble`, `rhias/nemo`, `rhias/trip`,
  `sloth/trading`, read 2026-11-25), so a lookup would change no group.

`TestHarnessCarry` (`routd/harness_carry_test.go`) dispatches a child group
that holds flat keys under a parent with a document, and fails when the guest
env carries a harness or a flat value. `TestTurnEnvEmissions`
(`runed/render_test.go`) fixes the full set of keys a turn receives.

Ant requires an object with a nonempty `backend` string. Optional `model` and
`effort` must be nonempty strings when present. Ant rejects unknown fields,
unknown backends, wrong types and unsupported effort values. Model IDs pass
verbatim without a model list. The adapter reports vendor rejection visibly.
Pi is a backend ant accepts (`selectHarness` and `parseHarness` in `ant/src/backend.ts`; `specs/7/60-pi-bridge.md`).

`runed/api/v1/types.go` carries the opaque value:

```go
Harness json.RawMessage `json:"harness,omitempty"`
```

routd copies the raw setting. runed sets `ARIZUKO_HARNESS` to its exact text.
Absence omits the env key; `null`, `{}` and an empty env value are not defaults.
Go checks transport syntax only. Ant validates document meaning inside the
delivering error boundary at `ant/src/index.ts:479`, before any adapter starts.
Invalid configuration produces an error submission and terminal failure;
it must not enter the transient retry loop or silently choose Claude.

The value is preserved, not its whitespace: `json.RawMessage` keeps key order,
escapes, unknown fields and numeric spelling, and `json.Marshal` only compacts
it. One request encoder stays; no hand-written splice and no second schema.
Settings writes and import must preserve that same raw value, including when
another setting changes. Remove the harness's route through `map[string]any`
and whole-config canonicalization. Malformed stored settings produce a visible
read failure, never an empty-default result: `setSetting`,
`SetGroupObserveWindow`, `SiblingFolders` and `GroupObserveWindow` return the
decode error. The prompt logs it and uses the instance window;
`set_observe_window` answers it and writes nothing.

The instance keeps `ARIZUKO_BACKEND`, `ARIZUKO_DEFAULT_MODEL` and
`ARIZUKO_DEFAULT_EFFORT`. Compose sends them to runed; `Config.AgentEnv` maps
them mechanically to `ARIZUKO_BACKEND`, `ARIZUKO_MODEL` and `ARIZUKO_EFFORT`.
runed passes these strings without knowing their values. Ant reads them only
when `ARIZUKO_HARNESS` is absent. Empty instance fields leave native defaults;
an absent instance backend selects Claude inside ant.

Keep ant's literal `env.ARIZUKO_MODEL` and `env.ARIZUKO_EFFORT` reads from
`ant/src/index.ts:281-282` in the default-selection path. The source scanner
at `runed/env_contract_test.go:58` detects them and the new env key.
Update its fixtures, not its exclusions. Remove the redundant model exemption
at `runed/env_contract_test.go:129`.

Ant parses the document inside the error boundary in `ant/src/index.ts`
before selecting an adapter. The selected model and effort reach `runTurn`.

`parseHarness` lives in `ant/src/backend.ts`; its input remains untyped until
validation succeeds. Adapter-owned validation keeps effort vocabularies in ant.
`codexArgs` at `ant/src/codex.ts:40` must accept `Turn.effort` and append
`-c model_reasoning_effort="<effort>"` through its existing TOML renderer.
Cover both new and resumed runs. Never silently drop or downgrade effort.

Settings affect the next run. An active process retains its snapshot.
Before a backend change, the operator ends the active run and uses `/new`
for affected topics. Session conversion and automatic resets are outside scope.
The runtime port and its `Turn` value remain unchanged; `Turn.Env` already
carries the document (specs/8/41-qemu-substrate.md §"The seam").

## One store write serves the dashboard, REST and agents

```text
REST ──► PATCH /v1/groups/{folder} (new) ──┐
agent ──► derived MCP update (new) ────────┼─► shared handler (changed)
                                           │        │ authorized replacement (new)
dashd free-text fields (changed)           │        ▼
        │ operator, admin-gated            │   Store.SetGroupSettings (changed)
        └──────────────────────────────────┼──────► │ settings + audit row, one batch
                                                    ▼
                                               groups.config
dashd SetGroupModel write (removed)
```

Each daemon reaches the store itself. routd's handler and dashd's settings page
both call `Store.SetGroupSettings` (`store/groups.go`), so the replacement rule,
the preservation of other settings and the audit row have one implementation.

The cold-tier `groups` resource gains `resreg.ActionUpdate` and its derived
MCP action. Its input uses `GroupsRow.Config`'s existing JSON-text form:
the supplied `harness` value replaces the entire stored value. Missing
`harness` leaves it unchanged; `harness: null` removes the setting.
Unrelated settings survive. The handler checks JSON structure, not harness
semantics, and records the mutation and audit row in the same store batch.

The resource is a forwarder (`routd/groups_resource.go:33`), so its injected
`Authz` must authorize updates explicitly on both surfaces. Bind action,
write scope and target folder at registration. REST uses the shared
authorization evaluator; MCP uses the existing turn authorization callback.
Neither the list branch nor the current read scopes authorize this write.

`resreg/resources/groups.go` declares the shared update endpoint:

```go
{Verb: "PATCH", Path: "/v1/groups/{folder}", Action: resreg.ActionUpdate},
```

dashd uses free-text backend, model and effort fields, plus one explicit
"Use instance defaults" control. Saving constructs one document; blank
optional fields are omitted, and a document needs its backend. There are no
backend, effort or model lists, per-key inheritance controls, or
effective-value resolver in Go. GET exposes the saved document for reload.
dashd writes the document to the store itself, as the operator
(`handleGroupSettingsSave` in `dashd/groups_admin.go`, through
`Store.AsUser(sub).SetGroupSettings`), and sends routd no request. The audit row
names the operator; a `service:dashd` bearer on `/v1` cannot carry that name
(`dashd/README.md`, "Writes stay direct on the owner's tables").
`TestGroupHarnessDashboard` (`dashd/chat_settings_test.go`) fails when the save
reaches routd or when the audit row names another actor.
No new CLI setter or MCP tool.
Bulk `arizuko apply` retains its existing interchange role and raw config
format; it neither resolves defaults nor validates ant's schema.

## Codex login follows the model-credential policy

```text
operator model env ──► spawnSecrets ───────────────► guest model env
caller env-profile ──►      │
                           └┴─ gateway set: withhold real keys
HOST_CODEX_DIR ──► staged RO login (changed) ──────► guest .codex
                           └── gateway set: withhold host login (new)
codex grant (removed)       group .codex rollouts ─► guest .codex
```

Model credentials belong to the env-profile class in
`SECURITY.md` § "A credential reaches work through one of two doors"; the operator fallback is host infrastructure.
They are not broker-only capability credentials. `store/secrets.go:248-255`
already lists `OPENAI_API_KEY` and `CODEX_API_KEY` beside
`CLAUDE_CODE_OAUTH_TOKEN`. `routd/dispatch.go:858` supplies caller credentials;
`runed/secrets.go:18` reads operator anchors and `runed/secrets.go:38` combines
them. Gateway mode withholds these model keys. Keep that single env path.

The measured `codex` grant has one authorization read,
`routd/dispatch.go:871`, with elevation as a bypass. The decision is logged
at `routd/dispatch.go:878`, serialized at `routd/dispatch.go:910` through
`runed/api/v1/types.go:66`, and consumed at `runed/mounts.go:174`.
`store/acl.go:118` accepts the action name; it does not gate execution.
The gate controls only staged RO copies of `auth.json` and `config.toml`
from `HOST_CODEX_DIR` (`container/runtime.go:11`, `runed/mounts.go:299`).
The writable group `.codex` mount is unconditional at `runed/mounts.go:170`.
The grant controls neither backend selection nor env-profile API keys.
Ant's Codex invocation ignores user config (`ant/src/codex.ts:50`).

Proposed credential rule: a Codex turn receives its model credential through
the same spawn-time policy as a Claude turn, with no backend-specific grant.
Retain `HOST_CODEX_DIR` as the file-form operator login source and retain its
staging, permissions and cleanup. In direct mode, configured login files
reach every turn, just as configured operator model env keys do.
In gateway mode, neither the host login mount nor real model env keys reach
the guest. Model credentials use the approved shared authorization policy.

`runed/mounts.go` stages the host login only in direct mode:

```go
if cfg.HostCodexDir != "" && cfg.ModelGatewayURL == "" {
```

The `codex` action and `RunRequest.Codex` are absent.
A stored row for that action has no enforcement effect.
Other capability grants, folder containment and caller-secret ownership stay intact.

Direct Codex also requires allowlist rules for the endpoints its login and
model requests use, or the existing unrestricted `egress` grant.
`store/seed.go:20` seeds only Anthropic hosts. A selected backend adds no
network rules; a configured gateway retains its existing egress allowance.

The flat group model/effort fields, Go effort vocabulary and Codex grant
flag are absent. Instance defaults and `HOST_CODEX_DIR` keep their readers.
Go does not infer a backend from a model name.

## Contract

- A Go integration test starts with a stored group document, passes it through
  routd and the real HTTP client/server, and compares guest `Turn.Env` with the
  compacted document. Include reordered keys, escapes, nested unknown fields and
  a number exceeding float64 precision. Go must carry even ant-invalid values.
  Repeat after an unrelated settings write and bulk import. Decoding the value
  into `map[string]any` anywhere on the path must fail the test.
- An ant test supplies invalid JSON, null, arrays, missing backend, unknown
  backend, wrong field types and invalid effort. Each must submit a visible
  error and exit with terminal failure before adapter invocation. A parser
  unit test alone is insufficient. Removing validation must fail this test.
- An ant test sets Claude instance defaults, then selects a Codex document
  with no model or effort. Neither Claude default may reach the Codex adapter.
  Repeat with a same-backend document, and verify absent-document defaults.
  A key merge mutation must fail. Keep literal env reads under the existing
  two-way env contract tests at `runed/env_contract_test.go:95` and
  `runed/env_contract_test.go:120`.
- Resource tests prove save/reload/clear, unrelated-setting preservation,
  audit atomicity and REST/MCP foreign-folder denial. Dashboard tests prove
  the fields and the control build one document and omit blank fields. Codex tests observe the CLI effort
  argument on exec and resume; Pi is selectable as a backend.
- Credential tests cover a direct Codex turn without a codex grant, caller
  API-key delivery, operator-login staging and gateway withholding of both
  credential forms. Keep egress denial independent of selection.
- `anteval/backends/backends.toml` proves a reply on each backend on one
  disposable instance with explicit network prerequisites. Each case names the
  agent program from the guest's process table, so a turn on the wrong backend
  fails. Measured 2026-11-25, v0.75.28, default backend codex; the lifecycle
  allowed the sink host, `chatgpt.com` and `auth.openai.com` for the eval folder:
  `backend-a-default` answered `codex` in 38 s, `backend-b-pi` (document
  `{"backend":"pi","model":"openai-codex/gpt-6.1-sol"}`) answered `pi` in 30 s,
  `backend-c-codex` (document `{"backend":"codex","effort":"low"}`) answered
  `codex` in 43 s; routd's usage record of each turn carries the same harness.
  The run used the `.codex` ownership stand-in that
  `specs/6/50-anteval-ephemeral-instance.md` §"Measured on a disposable instance"
  describes. The suite holds no claude case (BUGS record `L951`).
  No anteval run reaches a live instance.
