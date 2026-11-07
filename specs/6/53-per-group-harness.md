---
status: partial
---

# Each group carries one harness document

A harness document selects the backend, model and effort inside ant.

Opaque storage, HTTP carry, ant validation, whole-document replacement,
Codex effort and model-credential delivery have implementation tests in
`routd/harness_carry_test.go`, `ant/src/harness-main.test.ts`,
`routd/groups_detail_test.go`, `store/groups_test.go` and `runed/mounts_test.go`.

## Remaining

- Review flat overrides and Codex grant/deny rows before rollout (`cmd/arizuko/README.md`; operator data).
- Build the packaged agent image and prove Claude and Codex replies on one disposable instance with explicit egress (`Makefile`, `anteval/cases/anteval.toml`).

## The platform must carry the document without interpreting it

`store/groups.go` stores the document; `routd/dispatch.go` copies it into
`RunRequest.Harness`; `runed/render.go` emits `ARIZUKO_HARNESS`.
The backend switch belongs in `ant/src/backend.ts`, with validation before
adapter startup. Go does not interpret backend names, models or effort.

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

`resreg/resources/groups.go` declares the shared update endpoint:

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
the guest. Model credentials use the approved shared authorization policy.

`runed/mounts.go` stages the host login only in direct mode:

```go
if cfg.HostCodexDir != "" && cfg.ModelGatewayURL == "" {
```

The `codex` action and `RunRequest.Codex` are absent.
Existing grant and deny rows for that action have no enforcement effect;
an operator must review them before rollout. The spec authorizes no live edits.
Other capability grants, folder containment and caller-secret ownership stay intact.

Direct Codex also requires allowlist rules for the endpoints its login and
model requests use, or the existing unrestricted `egress` grant.
`store/seed.go:20` seeds only Anthropic hosts. A selected backend adds no
network rules; a configured gateway retains its existing egress allowance.

The flat group model/effort fields, Go effort vocabulary and Codex grant
flag are absent. Instance defaults and `HOST_CODEX_DIR` keep their readers.
Before rollout, operators must review flat overrides and Codex ACL rows;
Go must not infer an override's backend.

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
  free-text fields use that operation. Codex tests observe the CLI effort
  argument on exec and resume; Pi tests retain the named refusal.
- Credential tests cover a direct Codex turn without a codex grant, caller
  API-key delivery, operator-login staging and gateway withholding of both
  credential forms. Keep egress denial independent of selection. A disposable
  smoke proves Claude and Codex replies on one instance with explicit network
  prerequisites. Shipping also requires `specs/CLAUDE.md`'s documentation,
  dashboard, image and verification gates; no live-instance anteval run.
