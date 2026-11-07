---
status: draft
---

# Each group carries one harness document

A harness document selects the backend, model and effort inside ant.

## The platform must carry the document without interpreting it

`routd/dispatch.go:844` resolves model and effort against instance defaults.
`runed/render.go:109` emits one instance backend for every group.
These separate choices can send a Claude model to Codex, the L129 failure
recorded at `routd/cmd/routd/main.go:221`.

The backend switch belongs inside ant's process, in `ant/src/backend.ts:112`.
Its adapters are `claude.ts`, `codex.ts` and `pi.ts`; they are not peer daemons.
Only ant interprets backend names, model IDs and effort levels.

## One opaque value reaches ant

```text
dashd (changed) ──► groups update / derived MCP (new)
                              │ harness document (new)
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
                     Claude / Codex / Pi refusal

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
  job: preserve document bytes through HTTP encoding
  names: [Client.Run]
  never: normalize the raw value
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
  job: edit the whole document through the groups resource
  names: [handleGroupSettingsSave]
  never: write harness settings directly to the store
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

Ant requires an object with a nonempty `backend` string. Optional `model` and
`effort` must be nonempty strings when present. Ant rejects unknown fields,
unknown backends, wrong types and unsupported effort values. Model IDs pass
verbatim without a model list. The adapter reports vendor rejection visibly.
Pi keeps its explicit MCP-delivery refusal at `ant/src/backend.ts:116`.

Current fields, `runed/api/v1/types.go:29-32`:

```go
Model string `json:"model"`
Effort string `json:"effort,omitempty"`
```

Proposed replacement:

```go
Harness json.RawMessage `json:"harness,omitempty"`
```

routd copies the raw setting. runed sets `ARIZUKO_HARNESS` to its exact text.
Absence omits the env key; `null`, `{}` and an empty env value are not defaults.
Go checks transport syntax only. Ant validates document meaning inside the
delivering error boundary at `ant/src/index.ts:479`, before any adapter starts.
Invalid configuration produces an error submission and terminal failure;
it must not enter the transient retry loop or silently choose Claude.

Byte preservation includes whitespace inside the value, key order, escapes
and numeric spelling. `json.RawMessage` alone does not establish this:
`runed/api/v1/client.go:72` calls `json.Marshal`, which compacts raw values.
The request encoder must write the raw value without another marshal pass.
Keep one request encoder; do not create a second HTTP client or JSON schema.
Settings writes and import must preserve that same raw value, including when
another setting changes. Remove the harness's route through `map[string]any`
and whole-config canonicalization. Malformed stored settings produce a visible
read failure, never the empty-default result of `store/groups.go:66`.

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

Current selection, `ant/src/index.ts:482`:

```ts
const harness = selectHarness(process.env.ARIZUKO_BACKEND);
```

Proposed selection, within the same error boundary:

```ts
const selection = parseHarness(env);
const harness = selectHarness(selection.backend);
// runTurn receives selection.model and selection.effort, not default env reads.
```

`parseHarness` lives in `ant/src/backend.ts`; its input remains untyped until
validation succeeds. Adapter-owned validation keeps effort vocabularies in ant.
`codexArgs` at `ant/src/codex.ts:40` must accept `Turn.effort` and append
`-c model_reasoning_effort="<effort>"` through its existing TOML renderer.
Cover both new and resumed runs. Never silently drop or downgrade effort.

Settings affect the next run. An active process retains its snapshot.
Before a backend change, the operator ends the active run and uses `/new`
for affected topics. Session conversion and automatic resets are outside scope.
The runtime port and its `Turn` value remain unchanged; `Turn.Env` already
carries the document (specs/6/41-qemu-substrate.md §"The seam").

## One groups update serves the dashboard and agents

```text
dashd free-text fields (changed) ──► PATCH /v1/groups/{folder} (new)
agent ──► derived MCP update (new) ──► shared handler (changed)
                                           │ authorized replacement (new)
                                           ▼
                                      groups.config
dashd SetGroupModel write (removed)
```

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

Current endpoints, `resreg/resources/groups.go:30-31`:

```go
{Verb: "GET", Path: "/v1/groups", Action: resreg.ActionList},
{Verb: "GET", Path: "/v1/groups/{folder}", Action: resreg.ActionGet},
```

Proposed addition to that declaration:

```go
{Verb: "PATCH", Path: "/v1/groups/{folder}", Action: resreg.ActionUpdate},
```

dashd uses free-text backend, model and effort fields, plus one explicit
"Use instance defaults" control. Saving constructs one document; blank
optional fields are omitted. There are no backend, effort or model lists,
per-key inheritance controls, or effective-value resolver in Go.
GET exposes the saved document for reload. dashd calls the groups update
through `/v1` with the caller's authority. No new CLI setter or MCP tool.
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
`specs/5/14-credentials.md`; the operator fallback is host infrastructure.
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
the guest. This changes credential authorization; owner sign-off is required.

Current condition, `runed/mounts.go:174`:

```go
if cfg.HostCodexDir != "" && req.Codex {
```

Proposed condition:

```go
if cfg.HostCodexDir != "" && cfg.ModelGatewayURL == "" {
```

The `codex` action and `RunRequest.Codex` are removed after approval.
Existing grant and deny rows for that action then have no enforcement effect;
an operator must review them before rollout. The spec authorizes no live edits.
Other capability grants, folder containment and caller-secret ownership stay intact.

Direct Codex also requires allowlist rules for the endpoints its login and
model requests use, or the existing unrestricted `egress` grant.
`store/seed.go:20` seeds only Anthropic hosts. A selected backend adds no
network rules; a configured gateway retains its existing egress allowance.

## Remove the superseded declarations

Deletion anchors refer to `dbbf61ef2e53fa5551f5aac50984de4a94f5f310`.

- Remove routd's group-else-default branches at `routd/dispatch.go:844-850`
  and flat request assignments at `routd/dispatch.go:899-901`.
  Remove default state at `routd/loop.go:107-108`, config at
  `routd/loop.go:163-168`, and initialization at `routd/loop.go:227-228`.
  Remove env readers and their fallback commentary at
  `routd/cmd/routd/main.go:218-225`; remove routd's env-key entries at
  `compose/compose.go:126-127`, moving the keys to runed.
- Remove `RunRequest.Model` and `Effort` at `runed/api/v1/types.go:29-32`,
  their env rendering at `runed/render.go:100-105`, and the typed return
  fields at `routd/db.go:145-148` and `store/groups.go:576-600`.
  Remove the effort filter at `store/groups.go:592` and vocabulary at
  `store/groups.go:603`. Do not add a shared harness struct or a Go backend enum.
- Remove flat group keys at `store/groups.go:49-50`, model projection at
  `store/groups.go:108-110`, and `core.Group.Model` at `core/types.go:86`.
  Remove the model-only upsert paths at `store/groups.go:149-178`,
  `store/groups.go:218-231` and `store/groups.go:266-275`, and
  `SetGroupModel` at `store/groups.go:522-528`. Replace the direct call at
  `dashd/groups_admin.go:383` and delete its model menu at
  `dashd/groups_admin.go:285-303`.
- Remove `Config.AgentBackend` and its semantic comments at
  `core/config.go:58-62`, its initializer at `core/config.go:208`, and its
  renderer branch at `runed/render.go:109-111`. The generic instance env
  map replaces them. Remove whole-config normalization at
  `resreg/resources/groups.go:100`; preserve raw harness bytes in the
  settings writers at `store/groups.go:441` and `store/groups.go:493`.
  Remove the obsolete env-test exemption at `runed/env_contract_test.go:129`.
- After auth approval, remove the grant check, log field and assignment at
  `routd/dispatch.go:871`, `routd/dispatch.go:878` and
  `routd/dispatch.go:910`; remove the wire flag at
  `runed/api/v1/types.go:64-66`, accepted action at `store/acl.go:118`,
  and grant condition at `runed/mounts.go:174`. Replace their tests at
  `routd/capability_log_test.go:113`, `runed/mounts_test.go:355` and
  `store/acl_action_test.go:53` with the approved credential contract.

The three instance knobs retain readers through ant's default path.
`HOST_CODEX_DIR` retains readers at `core/config.go:237`,
`compose/compose.go:171`, `compose/compose.go:1224` and `runed/mounts.go:174`.
It is not dead configuration. No parallel flat group model or effort reader
survives. Before rollout, the operator rewrites existing flat overrides into
whole documents with the intended backend. Go must not guess their backend.

## Open questions

1. Approve removal of the `codex` authorization gate? A direct-mode group
   without that grant can then receive the configured host login, including
   groups with an explicit `codex` deny. Recommend approval because model
   credentials already follow that policy; require operator review before rollout.

## Contract

Source baseline at `dbbf61ef2e53fa5551f5aac50984de4a94f5f310`: two flat wire
fields, one Go effort vocabulary, one codex authorization read, and one
consumer of its wire flag. The deletions above remove all listed declarations
or decisions. Existing instance defaults and login sources remain functional.

- A Go integration test starts with a stored group document, passes it through
  routd and the real HTTP client/server, and compares guest `Turn.Env` bytes.
  Include whitespace, reordered keys, escapes, nested unknown fields and a
  number exceeding float64 precision. Go must carry even ant-invalid values.
  Repeat after an unrelated settings write and bulk import. Reintroducing
  ordinary `json.Marshal` at the raw boundary must fail the test.
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
  free-text fields use that operation. Codex tests observe the CLI effort
  argument on exec and resume; Pi tests retain the named refusal.
- Credential tests cover a direct Codex turn without a codex grant, caller
  API-key delivery, operator-login staging and gateway withholding of both
  credential forms. Keep egress denial independent of selection. A disposable
  smoke proves Claude and Codex replies on one instance with explicit network
  prerequisites. Shipping also requires `specs/CLAUDE.md`'s documentation,
  dashboard, image and verification gates; no live-instance anteval run.

## Steps

1. **Opaque carry and ant parse.** Add the raw setting, wire value, env emission
   and ant validation with whole-document replacement. Forward Codex effort.
   Verify byte preservation through HTTP, visible invalid-document failure,
   default replacement, env coverage and Pi refusal.
2. **Write door and dashd.** Add the groups update action and derived MCP,
   then connect the free-text form. Verify save/reload/clear, audit atomicity,
   folder authorization and preservation across unrelated writes and import.
3. **Credential door.** After owner sign-off, remove the codex grant and apply
   the model-credential policy to the host login. Verify direct credentials
   without the grant, gateway withholding, persistent rollouts and egress denial.
4. **Delete superfluous config.** Complete every anchored deletion and replace
   obsolete tests and documentation. Verify no production readers of flat
   group model/effort or the codex flag remain. Run build, lint, short Go and
   ant suites, then the disposable mixed-backend smoke before shipping.
