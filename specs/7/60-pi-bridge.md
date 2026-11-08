---
status: partial
---

# specs/7/60 — pi is a full ant harness

pi is an agent harness that gives a turn arizuko's tools, manual, skills, delivery, and model access through ant.

## The problem

The owner requires:

> "pi has to be the same experience as codex or claude and the code has to have adaptation at a single place... ant"
>
> "the pi should be able to consume anything"

## Implementation status

The release line selects pi through `ARIZUKO_BACKEND=pi` in `ant/src/backend.ts`.
`ant/src/pi.ts` renders the assembled MCP map, streams status, forwards effort,
uses existing credentials and native provider configuration, and applies finite retries.
`ant/src/pi-extension.ts` guards native edits and writes and applies Bash rewrites.
The dependency is pinned to `@earendil-works/pi-coding-agent` 1.4.0.
Pi uses the same tools and explicit delivery as Claude Code and Codex.

Acceptance remains partial. These items are deferred:

- The installed-binary image smoke in step 5, including hostile configuration,
  provider errors, stalled streams, resume, nudge and usage checks. `ant/smoke/`
  and the `pi-smoke` target are not present.
- Native edit fixtures in step 1 for fuzzy and disjoint edits, tilde paths and CRLF/BOM input.
- Pi-specific chassis integration tests in step 4. Reducer fixtures cover split
  status and tool-only completion; the chassis nudge tests use a fake harness.
- The real pi turn through routd's `gated.sock`, including delivery, submission,
  authorization and audit evidence. Local-model reachability and privacy are unmeasured.
- Borrowed-login renewal ownership. Pi refreshes its temporary snapshot only;
  durable renewal of the shared login needs an operator decision.
- Full acceptance gates in step 6, including the image and live checks.

Any pi provider works through its normal operator configuration and a
`network_rules` row for the provider host. A local model uses that same setup.

The design excerpts below compare base `6d5095d685e8e4aae891df8464b005231fb3a12c`
with the intended bridge. Their line numbers refer to that base; the status above
states what the release line implements. The module map defines the full acceptance
scope, including deferred work. No live acceptance is claimed.

## Where it fits

```text
┌────────────────────┐ Turn credentials ┌────────────────────────┐
│ ant chassis        ├─────────────────►│ pi.ts (changed)        │
│ index.ts           │                  │ pi-extension.ts        │
└────────────────────┘                  │ (changed)              │
                                        └──────────┬─────────────┘
                                                   │ native config (new)
                                        ┌──────────▼─────────────┐
                                        │ pi 1.4.0 (changed)     │
                                        │ builtin:mcp (new)      │
                                        └─────┬──────────┬───────┘
                                  stdio (new) │          │ model request
                                  ┌───────────▼──┐    ┌──▼──────────────┐
                                  │ socat        │    │ egress proxy    │
                                  └───────┬──────┘    └──┬──────────────┘
                                  ┌───────▼──────┐    ┌──▼──────────────┐
                                  │ gated.sock   │    │ chosen provider │
                                  │ routd gate   │    │ or gateway      │
                                  └──────────────┘    └─────────────────┘
```

All pi-specific executable changes stay in `ant/`. runed passes the existing credential sources.
`Turn`, `Frame`, `runed.Runtime`, `objstore`, spawn env, and MCP submission fields keep their current shape.
There is no `LOCAL_MODEL_URL`, special local-provider prefix, new daemon reader, or automatic local-host allowance.
The MCP map remains the declaration at `ant/src/mcp-servers.ts:30`.
The policy table remains the declaration at `ant/src/policy.ts:65`.

## Module map

The owner requested a map per coding step. This block owns files, symbols, dependencies, and gates.
The sections below own the current and proposed excerpts. Steps run serially after their dependencies pass.
Tests named here are required additions or extensions, not completed evidence.

```yaml
steps:
  - step: 1
    depends_on: []
    excerpt: 'The pinned package keeps the guard effective'
    files:
      - path: ant/Dockerfile (changed)
        job: expose the locally installed pinned pi CLI
        names: ['/app/node_modules/.bin/pi', '/usr/local/bin/pi']
        never: install a second global copy of pi
      - path: ant/package.json (changed)
        job: declare the pi runtime and public tool factories at one version
        names: ['@earendil-works/pi-coding-agent: 1.4.0']
        never: use a version range for pi
      - path: ant/package-lock.json (changed)
        job: lock the declared pi dependency and its transitive dependencies
        names: ['npm install']
        never: choose a second pi version
      - path: ant/src/pi-extension.ts (changed)
        job: translate native tool calls and render the existing policy rows
        names:
          [
            'canonicalCall(name, input)',
            'registerWith(pi, ctx)',
            'PI_HONOURS',
            'native edit/write operations',
          ]
        never: invent another guard or change MCP tool names
      - path: ant/src/pi-extension.test.ts (changed)
        job: test real edit inputs, MCP names, denial, steering, and bash rewrites
        names:
          [
            'dangerous native write/edit',
            'fuzzy edits and tilde paths',
            'bash scrub reaches native input',
            'deny wins',
          ]
        never: use the obsolete top-level edit fixture
    gate: 'cd ant && npm install && bun test src/pi-extension.test.ts && bunx tsc --noEmit'
    mutation: 'Bypass the native write guard or drop Bash input mutation; the respective regression must fail.'
  - step: 2
    depends_on: [1]
    excerpt: 'pi uses the one MCP declaration'
    files:
      - path: ant/src/pi.ts (changed)
        job: render native per-turn config, argv, credentials, and event frames
        names:
          [
            'piMcpConfig(tools)',
            'piArgs(turn, promptFile, skillDirs)',
            'runPi(turn)',
            'PiTurn',
            'piHarness',
          ]
        never: widen Turn or discover agent-written MCP configuration
      - path: ant/src/backend.ts (changed)
        job: select pi and keep the existing harness contract
        names: ['selectHarness', 'HarnessProfile.tools']
        never: carry pi-specific fields on Turn or Frame
      - path: ant/src/pi.test.ts (changed)
        job: verify config isolation, argv, events, cleanup, credentials, and resume
        names:
          [
            'direct MCP without secret env',
            'fresh agent directory',
            'PiTurn fixtures',
          ]
        never: treat a mocked binary as package acceptance
      - path: ant/src/backend.test.ts (changed)
        job: check pi selection and its capability profile
        names: ['selectHarness pi', 'pi profile']
        never: accept tools none for a selectable pi
    gate: 'cd ant && bun test src/pi.test.ts src/backend.test.ts && bunx tsc --noEmit'
    mutation: 'Drop --no-approve or add env to mcp.json; the isolation test must fail.'
  - step: 3
    depends_on: [2]
    excerpt: 'pi consumes existing model sources'
    files:
      - path: runed/env_contract_test.go (changed)
        job: check ant reads against both existing process env and stdin model secrets
        names:
          [
            'TestSpawnEnvCarriesEveryKeyAntReads',
            'TestSpawnSecretsCredentialContract',
          ]
        never: add a pi exemption, credential name list, or production env key
    edits_in:
      [
        'ant/src/pi.ts',
        'ant/src/pi-extension.ts',
        'ant/src/pi.test.ts',
        'ant/src/pi-extension.test.ts',
      ]
    names:
      [
        'registerGateway(pi, env)',
        'piArgs effort',
        'runPi native models.json and borrowed login',
      ]
    never: add a daemon config key, a model catalog, or egress policy
    gate: 'cd ant && bun test && bunx tsc --noEmit'
    contract_gate: "go test ./runed -run 'TestSpawnEnv' -count=1"
    secret_contract_gate: "go test ./runed -run 'TestSpawnSecretsCredentialContract' -count=1"
    mutation: 'Send the literal gateway key name or omit the OAuth alias; a credential test must fail.'
  - step: 4
    depends_on: [3]
    excerpt: 'The turn has the same delivery and progress contract'
    files:
      - path: ant/src/index.test.ts (changed)
        job: verify pi frames through the shared delivery and progress loop
        names:
          [
            'pi nudge resumes same turn',
            'split status arrives before message_end',
            'tool-only completion',
          ]
        never: add a pi branch to the chassis
    edits_in: ['ant/src/pi.ts', 'ant/src/pi.test.ts']
    names: ['PiTurn.push', 'PiTurn.finish', 'runPi controlled retry settings']
    never: add a second status parser, delivery path, or timeout
    gate: 'cd ant && bun test && bunx tsc --noEmit'
    mutation: 'Drop text_delta handling or require final prose; the respective contract test must fail.'
  - step: 5
    depends_on: [4]
    excerpt: 'The image proves the bridge'
    files:
      - path: ant/smoke/pi-bridge.ts (new)
        job: drive the installed binary against a scripted provider and recording socket
        names:
          [
            'reply over gated.sock',
            'native provider configuration',
            'bounded provider failure',
          ]
        never: contact a vendor or claim to prove routd authorization
      - path: ant/smoke/pi-models.json (new)
        job: supply a native provider fixture mounted read-only into the smoke container
        names: ['providers', 'smoke/model:1b', 'http://127.0.0.1:23456/v1']
        never: contain a real credential
      - path: ant/Makefile (changed)
        job: expose the image smoke command
        names: ['pi-smoke']
        never: run the smoke as part of bun test
    gate: 'make agent DOCKER="sudo docker" && make -C ant pi-smoke DOCKER="sudo docker"'
    verify: 'sudo docker run --rm arizuko-ant pi --version; sudo docker run --rm arizuko-ant node --version'
    mutation: 'Remove builtin:mcp or direct exposure; no reply reaches the socket and the smoke fails.'
  - step: 6
    depends_on: [5]
    excerpt: 'Documentation and live evidence close the change'
    files:
      - path: ant/README.md (changed)
        job: document harness parity, native config, credentials, and smoke use
        names:
          [
            'The harness',
            'The model gateway, per harness',
            'Contracts with the Go side',
          ]
        never: promise provider entitlement or unmeasured privacy
      - path: EXTENDING.md (changed)
        job: explain instance backend selection and native provider mounts
        names:
          ['Swapping the agent harness', 'Adding a host mount at /mnt/<name>']
        never: document a group harness document on this base
      - path: INSTALL.md (changed)
        job: show existing credentials, gateway, network rows, and local reachability
        names:
          [
            'Set the model key and the channel token in .env',
            'Add a model gateway',
          ]
        never: introduce LOCAL_MODEL_URL or promise Anthropic plan quota
      - path: SECURITY.md (changed)
        job: document pi config pinning and the scope of model privacy
        names: ['gated.sock', 'native models.json', 'borrowed login']
        never: describe bash scrubbing as a security boundary
      - path: README.md (changed)
        job: state verified pi availability and the qualified privacy claim
        names: ['ARIZUKO_BACKEND']
        never: equate model privacy with channel privacy
      - path: CHANGELOG.md (changed)
        job: record v0.75.4 behavior in the required announcement form
        names: ['Unreleased']
        never: claim the live check passed before it runs
      - path: specs/6/47-voluntary-reply.md (changed)
        job: reconcile the pi row with delivery through submit_turn
        names: ['Day one, per harness']
        never: retain the pi selection refusal as target behavior
      - path: specs/6/23-llm-gateway.md (changed)
        job: reconcile native provider configuration with measured gateway behavior
        names: ['registerGateway']
        never: erase the measured vendor entitlement restriction
      - path: template/web/pub/arizuko/concepts/ant.html (changed)
        job: describe pi's verified harness capabilities
        names: ['pi']
        never: advertise unavailable progress or delivery
      - path: template/web/pub/arizuko/concepts/secrets.html (changed)
        job: explain the existing credential sources for pi
        names: ['env-profile', 'codex grant']
        never: invent a pi secret tier
      - path: template/web/pub/arizuko/security/index.html (changed)
        job: define model privacy and its limits
        names: ['private model call']
        never: include channel or connector traffic in that claim
      - path: template/web/pub/arizuko/components/egress.html (changed)
        job: explain operator-declared model hosts under existing network rules
        names: ['network_rules']
        never: claim automatic local-host permission
      - path: specs/7/60-pi-bridge.md (changed)
        job: record implementation and acceptance status
        names: ['status']
        never: mark unverified work shipped
      - path: specs/7/index.md (changed)
        job: keep the index row and frontmatter status equal
        names: ['60-pi-bridge.md']
        never: report a different status
      - path: specs/index.md (changed)
        job: link the phase index explicitly so tools discover its spec rows
        names: ['7/index.md']
        never: duplicate the phase's spec rows
    gate: 'make build && make lint && go test ./... -short'
    agent_gate: 'cd ant && bun test && bunx tsc --noEmit'
    docs_gate: 'go test ./tests/specrefs ./tests/webdocs -count=1; speclint specs'
    live_gate: 'One real pi turn calls reply through gated.sock on a throwaway or operator-chosen setup.'
```

`specs/6/46-turn-progress.md` has no pi-specific assertion to change on this base.
Web changes follow `template/web/CLAUDE.md` at implementation time.
The work record owns snapshots, execution results, and release actions.

## The pinned package keeps the guard effective

```text
pi native input (changed) ──► canonicalCall (changed) ──► bindings
pi native input (changed) ◄── bash rewrite (new)     ◄── bash-scrub
```

Current, `ant/Dockerfile:173-175`:

```dockerfile
RUN npm install -g bun agent-browser @anthropic-ai/claude-code @apify/mcpc@0.6.0 @openai/codex \
    @mariozechner/pi-coding-agent \
    && bun install -g @biomejs/biome prettier svgo \
```

Proposed: remove the global pi token. Add the exact dependency to `ant/package.json:12`:

```json
"@earendil-works/pi-coding-agent": "1.4.0"
```

Regenerate the lockfile with npm. After the dependency install at `ant/Dockerfile:186`, expose its CLI:

```dockerfile
RUN ln -s /app/node_modules/.bin/pi /usr/local/bin/pi
```

The CLI and extension import use the same package installation.
The image must report `1.4.0` and Node >=22.19.0. The package's `engines` requires that Node version.

Current, `ant/src/pi-extension.ts:63-66`:

```ts
if (toolName === 'Edit') {
  const { path: p, oldText, newText, ...rest } = input;
  return {
    ...rest,
    file_path: abs(p),
    old_string: oldText,
    new_string: newText,
  };
}
```

Proposed boundary shape, replacing `canonicalToolName` and `canonicalInput`:

```ts
canonicalCall('edit', { path, edits: [{ oldText, newText }] });
// => { name: 'MultiEdit', input: {
//      file_path: absolutePath, edits: [{ old_string: oldText, new_string: newText }]
//    } }
canonicalCall('mcp__arizuko__reply', input);
// => { name: 'mcp__arizuko__reply', input }
```

This mapping supplies canonical logging names and inputs. MCP names remain byte-for-byte intact.
`bash` maps to `Bash` with its input intact. Do not use the mapped MultiEdit input to reconstruct a pi edit.
Pi supports simultaneous and fuzzy edits, line-ending normalization, and tilde paths.
The canonical guard applies literal sequential edits at `ant/src/skillguard.ts:247`.

Render the existing `skill-guard` row at the native file-write operation, where pi supplies the exact absolute path and final content.
Import public `createEditToolDefinition` and `createWriteToolDefinition` from the pinned dependency.
Register these native definitions under their existing tool names, with their operations overridden.
The shared `writeFile` operation evaluates the row as a canonical `Write`, then writes only when the row allows.
Keep native read/access/mkdir operations and tool schemas. Do not implement pi's matching or path rules in ant.
Skip this row only in the pre-tool renderer for these two native tools, because their write operation renders it once.
Other policy rows keep their pre/post-tool position. Native tool errors remain tool errors.
Use `runRow` for this binding so exception handling retains the shared policy behavior.

Proposed write-operation excerpt inside `registerWith`:

```ts
writeFile: async (file_path, content) => {
  const verdict = await runRow(skillGuardRow, {
    hook_event_name: 'PreToolUse',
    tool_name: 'Write',
    tool_input: { file_path, content },
    tool_use_id: '',
    session_id: '',
  });
  if (verdict.kind === 'deny') throw new Error(verdict.reason);
  await fs.promises.writeFile(file_path, content, 'utf8');
};
```

`skillGuardRow` is selected from `bindings(ctx)`, not a second declaration or a copy of the scanner.
Test a dangerous fuzzy multi-edit, disjoint edits whose inserted text matches another fragment, tilde paths, and CRLF/BOM input.
The test must show denial before disk mutation and normal native output for an allowed edit.

Current, `ant/src/pi-extension.ts:94-97`:

```ts
pi.on('tool_call', async (ev) => {
  const verdict = await runRows(
    rows,
    'PreToolUse',
    toToolEvent(ev, 'PreToolUse'),
  );
  return verdict.kind === 'deny'
    ? { block: true, reason: verdict.reason }
    : undefined;
});
```

Proposed: include `rewrite` in `PI_HONOURS` and render the existing `bash-scrub` binding.
`runRows` ignores rewrites at `ant/src/policy.ts:149`; passing the row to it is insufficient.
Within `registerWith`, evaluate matching pre-tool rows with the existing `runRow` and `matches` functions.
Keep deny precedence and run every matching row. Apply an allowed Bash rewrite to `ev.input.command` in place.
Only the Bash row rewrites input today. Do not create a generic reverse mapper or copy `SECRET_ENV_VARS`.
The prefix remains owned by `ant/src/guards.ts:36`.
The post-tool path keeps `runRows`, result blocks, skill rescan, and the existing steering renderer.

## pi uses the one MCP declaration

```text
Turn.tools ──► piMcpConfig (new) ──► fresh agent dir (new)
                                             │
                                             ▼
                                  builtin:mcp (new) ──► socat ──► gated.sock
```

Current, `ant/src/pi.ts:48-63`, selected statements:

```ts
const args = ['-p', '--mode', 'json'];
args.push('--session-dir', sessionDir(turn.cwd));
if (turn.continuity) args.push('--session', turn.continuity);
args.push('--no-extensions', '--no-skills');
args.push('-e', PI_EXTENSION);
```

Proposed argv additions:

```ts
args.push('--no-extensions', '--no-skills', '--no-approve', '--offline');
args.push('-e', 'builtin:mcp', '-e', PI_EXTENSION);
if (turn.effort) args.push('--thinking', turn.effort);
```

Keep `--session-dir`, `--session`, `--system-prompt`, scanned `--skill` paths, and the `@promptFile` envelope.
`piArgs` adds `effort` to its existing `Pick<Turn, ...>`; the port already carries effort.

Proposed MCP renderer:

```ts
export function piMcpConfig(tools: Record<string, McpServerConfig>) {
  return {
    mcpServers: Object.fromEntries(
      Object.entries(tools).map(([name, s]) => [
        name,
        { command: s.command, args: s.args ?? [], exposure: 'direct' },
      ]),
    ),
  };
}
```

All servers from the assembled map are direct. The current map has only `arizuko`, with `alwaysLoad: true`.
Do not copy `env` to MCP configuration. Pi's stdio child inherits the pi process environment.
Do not read extra MCP declarations from provider configuration or the group home.

Current, `ant/src/pi.ts:194-205`, writes a prompt file and calls `driveCli` with `env: turn.env`.
Proposed: `runPi` creates a mode-0700 temporary agent directory and writes `mcp.json` at mode 0600.
Pass `PI_CODING_AGENT_DIR` only in the pi child's environment, alongside `turn.env`.
Keep persistent sessions under `sessionDir(turn.cwd)`.
Remove temporary files on success, failure, abort, and nudge completion. Never remove persistent sessions.

`--no-approve` excludes trust-gated project configuration.
`--no-extensions` excludes discovered, configured, and built-in extensions; explicit `-e builtin:mcp` restores MCP.
The fresh agent directory excludes writable `~/.pi/agent` configuration.
Test hostile project MCP/settings and discovered extensions with the real binary.
Do not describe the directory as a same-UID security boundary or claim pi can never reload configuration.

Current, `ant/src/backend.ts:116`:

```ts
    case 'pi': throw new Error('pi cannot deliver through MCP; select claude or codex until the bridge ships');
```

Proposed: import `piHarness` and return it for `case 'pi'`.
Set its `tools` profile to `'mcp'`; remove unused `'bridged'` at `ant/src/backend.ts:78`.
Correct the obsolete MCP comments in the touched files.
Every model-invoked arizuko call still crosses `ipc.serveConn` at `ipc/ipc.go:489`.
The gate, authorization, and audit remain routd's responsibility.

## pi consumes existing model sources

```text
existing env-profile keys ──► pi child env (changed) ──► native providers
existing codex RO login ────► native auth snapshot (new) ──► openai-codex
existing RO host mount ────► native models.json (new) ──► selected provider
MODEL_GATEWAY_URL/key ─────► registerGateway (changed) ──► existing gateway
```

| Existing source                                                      | Pi adaptation in ant                                                                                                                                                                                                                            |
| -------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `store/secrets.go:248`, `runed/secrets.go:38`: five env-profile keys | Keep `ANTHROPIC_API_KEY`, `ANTHROPIC_OAUTH_TOKEN`, and `OPENAI_API_KEY`. Alias `CLAUDE_CODE_OAUTH_TOKEN` to pi's Anthropic OAuth slot and `CODEX_API_KEY` to its OpenAI slot when that slot is absent. Explicit provider-native values win.     |
| `runed/mounts.go:173`: `HOST_CODEX_DIR` and the `codex` grant        | Read the existing read-only `.codex/auth.json` snapshot. Translate Codex token fields into pi's native `openai-codex` OAuth record in temporary `auth.json`. Never read host paths directly or persist refreshed credentials in the group home. |
| `runed/render.go:115`: gateway URL and per-turn virtual key          | Use `registerGateway`. In gateway mode, do not import a borrowed vendor login. Native provider configuration must use the gateway and virtual key for the selected model.                                                                       |
| `runed/mounts.go:179`, `EXTENDING.md:35`: existing read-only mounts  | Copy the operator's native `/mnt/pi/models.json` into the temporary agent directory. Mount with `Container: "pi", RO: true`. Pi owns the provider schema, API choice, model metadata, and compatibility settings.                               |

`/mnt/pi/models.json` is an ant-side mount convention, not a daemon field or an arizuko provider schema.
Require an actual read-only mounted source. Reject a writable source, symlink escape, or malformed configuration.
An absent source keeps pi's built-in catalog available. An unresolved configured provider/model fails explicitly.
Use the existing mount allowlist and `container_config.Mounts`; add no mount machinery.

An operator can declare any pi-supported provider or OpenAI-compatible local or remote endpoint in native `models.json`.
The declaration uses pi's `providers`, `api`, `baseUrl`, `models`, and native key interpolation.
An endpoint credential can use an existing env-profile slot or a secret already delivered through `shell:env:<KEY>`.
`routd/dispatch.go:848` owns that grant path. An arbitrary host `.env` key does not automatically enter a guest.
Keep custom model context defaults at pi's 128000/16384 values. Keep upstream metadata for built-in models.
Do not create a parallel catalog or restrict native providers to text-only input.
An unsupported wire protocol requires a trusted provider extension in ant; changing the endpoint alone cannot supply it.

Current, `ant/src/pi-extension.ts:122-124`:

```ts
const auth = gw.key ? { apiKey: GATEWAY_KEY_ENV } : {};
pi.registerProvider('anthropic', { baseUrl: gw.url, ...auth });
pi.registerProvider('openai', { baseUrl: `${gw.url}/v1`, ...auth });
```

Proposed key expression:

```ts
const auth = gw.key ? { apiKey: `$${GATEWAY_KEY_ENV}` } : {};
```

Pi 1.4.0 treats an unprefixed key name as a literal.
Keep built-in gateway rendering in `registerGateway`, using `modelGateway` from `ant/src/gateway.ts:18`.
Native `models.json` supplies non-catalog gateway models, including the API and model ID served by the gateway.
Pi composes native model configuration above registered providers. Reject a selected model override that bypasses a configured gateway.
Tests must inspect the outgoing URL and Authorization header, not only mocked registration arguments.

Only read the borrowed login when its existing mount is read-only and no gateway takes precedence.
Translate `tokens.access_token`, `tokens.refresh_token`, and `tokens.account_id` from a Codex OAuth fixture.
Use the access token's expiry claim for pi's `expires`; do not fabricate expiry from `last_refresh`.
The native target record is `{type:"oauth", access, refresh, expires, accountId}` under `openai-codex`.
Verify the source fixture against the installed Codex format before implementing the converter.
Handle a key-based Codex login through the corresponding native API-key path, not as fabricated OAuth.
Reject malformed or unusable credentials with a clear auth error.
Keep credentials out of logs, argv, and MCP config. Native refresh can update only the temporary copy.
Durable shared renewal needs owner decision 1.

The existing Go/TypeScript contract test at `runed/env_contract_test.go:95` checks only `turnEnv` emissions.
Ant's credential aliases also read keys carried in stdin, through `spawnSecrets` at `runed/render.go:75`.
Extend the same source-reading contract to verify the union of both existing producer paths.
Seed the fixture from `store.EnvProfileKeys`, not a second key list. Check user overrides and gateway withholding separately.
Do not exempt pi reads or introduce a production Go reader. The only non-ant executable edit is this shared contract test.

`container/egress.go:62` keeps its gateway exception.
Every direct provider host needs an operator-added `network_rules` row.
Private addresses are permitted by default; `egress/address.go:39` blocks loopback and link-local addresses.
Use a DNS name that the proxy resolves to a reachable private or public address.
`runed/vmnet.go:248` starts the proxy inside runed's network namespace.
A name in a guest's hosts file does not establish proxy DNS resolution.
No egress wildcard, `NO_PROXY` addition, host alias, or policy exception belongs in this change.

"You can stay private if you use PI and a local model." requires measured model routing.
The claim covers prompts, model transcripts, and tool results sent to that local model.
Channels, connector calls, transcription, published pages, and shell traffic retain their own destinations.
`--offline` disables automatic pi network activity; model and tool requests still use the network.

## The turn has the same delivery and progress contract

```text
pi events (changed) ──► PiTurn (changed) ──► shared status and result frames
                                                  │
                                                  ▼
reply ──► gated.sock                     submit_turn ──► routd
                                                  ▲          │
                                                  └─ nudge ──┘
```

| Capability                   | Claude and codex on this base                                                                                          | Required pi behavior                                                                                                                                                                                                                                                                                                                   |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| MCP tools and gate           | Both render `Turn.tools`; `ant/src/claude.ts:565`, `ant/src/codex.ts:59`.                                              | Adapted in `pi.ts`: native MCP, direct tools, same socket, hold, grant, and audit path.                                                                                                                                                                                                                                                |
| Delivery and `submit_turn`   | Tools deliver. The chassis submits completion through `ant/src/mcp.ts:94`.                                             | Same chassis. Successful tool-only completion must not require terminal prose. `submit_turn` stays hidden from model tools.                                                                                                                                                                                                            |
| `[deliver]` Stop nudge       | `reply-owed` binds Stop at `ant/src/policy.ts:69`; submission is the shared backstop.                                  | Adapted in `pi.ts`: keep the owner's no-Stop choice. `runTurn` at `ant/src/index.ts:397` resumes the same turn, latest session, and deadline. One nudge and the silent-turn notice still apply.                                                                                                                                        |
| `<status>` progress          | Frames pass through `statusBlocks` at `ant/src/index.ts:157`.                                                          | Adapted in `PiTurn.push`: accumulate text deltas and emit cumulative status frames before `message_end`. The chassis parses and deduplicates tags.                                                                                                                                                                                     |
| Steering                     | Shared spool and PostToolUse injection at `ant/src/policy.ts:89`; Claude also accepts prompts while active.            | Same post-tool injection through `registerWith`, preserving text and image blocks. Queued input without a later tool becomes the next turn. Keep `between-turn`, matching codex. Immediate external prompt injection is impossible with the current one-way print invocation; a bidirectional pi mode would need extra ant adaptation. |
| Skills and guard             | One scan and policy table, with native loading or an index.                                                            | Adapted in `pi.ts` and `pi-extension.ts`: scanned explicit skill paths, native edit/write shapes, and the shared post-tool rescan.                                                                                                                                                                                                     |
| Manual                       | Claude reads the resident manual natively; codex receives rendered text.                                               | Same rendered path at `ant/src/index.ts:53`, passed through `--system-prompt`. No pi manual copy.                                                                                                                                                                                                                                      |
| Continuity/resume            | Each harness returns an opaque native token.                                                                           | Same contract: session header ID, persistent session directory, explicit resume, and one fresh retry on a missing session.                                                                                                                                                                                                             |
| Model/effort                 | Both fields reach `Turn`. Claude forwards effort at `ant/src/claude.ts:545`; this codex driver has no effort override. | Adapted in `piArgs`: pass the chosen model and `--thinking` effort. Pi clamps effort to model capabilities. No generic model-name translation.                                                                                                                                                                                         |
| Usage                        | Result usage reaches submission; the gateway remains the ledger.                                                       | Same `ModelUsage` fields, summed once per completed assistant response and rounded once per model. Do not sum cumulative delta usage. Preserve declared local zero cost.                                                                                                                                                               |
| Bash scrub                   | Claude renders the shared rewrite. Codex cannot render it and relies on its shell environment behavior.                | Adapted in `registerWith`: mutate Bash input with the existing policy prefix. Keep the credential partition. This protects against accidental echo.                                                                                                                                                                                    |
| Timeouts/abort               | `driveCli` at `ant/src/backend.ts:159` uses the process deadline and signal.                                           | Same deadline for the first run, nudge, and resume. No pi timeout port or reset timeout.                                                                                                                                                                                                                                               |
| Unreachable host, L717 class | Codex bounds repeated network-wait errors at `ant/src/codex.ts:116`.                                                   | Adapted in `runPi`: native finite retries, no nested provider retries, and the shared deadline. Test denied 403, unreachable 502, connect failure, and a stalled stream. Report the actual error without guessing a grant failure.                                                                                                     |
| Provider entitlement         | `INSTALL.md:884` records Anthropic rejecting pi plan-quota access.                                                     | Impossible for ant to grant vendor entitlement. Use an accepted API key or gateway credential. Re-measure subscription support; token compatibility does not prove entitlement.                                                                                                                                                        |

The no-Stop choice preserves delivery and the one-nudge rule. Pi pays a subprocess resume for the nudge.
Pi exposes `agent_before_settle`; the owner chose submission as the nudge source for this spec.
Keep that decision settled. Do not claim Claude's exact Stop timing or immediate steering.
Change the omitted-row diagnostic to say delivery uses submission; do not claim pi lacks a settle event.

Current, `ant/src/pi.ts:110-112`:

```ts
if (type !== 'message_end') return [];
const m = obj.message as PiMessage | undefined;
if (m?.role !== 'assistant') return [];
```

Proposed event reduction:

```text
message_start: reset current assistant text blocks
message_update.assistantMessageEvent.text_delta: append delta by contentIndex
message_update: emit status with accumulated assistant text
message_end: replace with authoritative content; account usage once
session: emit the existing continuity frame
process exit: emit one result, including failure after an error or abort
```

Current success requires text at `ant/src/pi.ts:144`:

```ts
const ok =
  exitCode === 0 &&
  this.lastText !== undefined &&
  this.lastStop !== 'error' &&
  this.lastStop !== 'aborted';
```

Proposed: require a completed successful assistant response and a clean exit, without requiring nonempty text.
A missing response, error, abort, or resume miss retains its distinct failure behavior.
`agent_end` does not terminate the reducer because retries and recovery can follow it.

Write controlled native `settings.json` in the fresh directory:

```json
{
  "retry": {
    "enabled": true,
    "maxRetries": 2,
    "baseDelayMs": 2000,
    "maxAgentDelayMs": 10000,
    "provider": { "maxRetries": 0 }
  }
}
```

The two retries bound repeated fast failures without multiplying provider retries.
The unchanged process deadline bounds a stalled request.
The image smoke records the request count and failure time; settings alone prove neither.

## The image proves the bridge

```text
scripted model (new) ── tool call (new) ──► installed pi (changed)
                                                 │
                                                 ▼
                                  recording gated.sock (new)
```

`ant/Makefile:14` currently runs `bun test`. Proposed addition:

```makefile
.PHONY: pi-smoke
pi-smoke:
	$(DOCKER) run --rm --network none \
	  --mount type=bind,src=$(CURDIR)/smoke/pi-models.json,dst=/mnt/pi/models.json,readonly \
	  arizuko-ant bun smoke/pi-bridge.ts
```

The smoke lives outside `src/` and has no `.test.ts` suffix.
It starts a scripted OpenAI-compatible server at fixture port 23456 and a unix socket inside the image.
The read-only fixture declares `smoke/model:1b` using native pi syntax and `$OPENAI_API_KEY` for a synthetic key.
The first model response calls `mcp__arizuko__reply` with `{"text":"pong"}`. A later response completes the run.
The socket handles initialize, notifications, tools/list, and tools/call.
Run through the chassis loop to capture `submit_turn` and one forced nudge continuation.
Assert the wire call uses `reply`, the nudge keeps the turn and continuity, and final prose never becomes delivery.

Also exercise split status tags, multi-response usage, tool-only completion, hostile project config, and native resume.
The provider fixture checks the outgoing header value and endpoint, including gateway mode.
Separate failures return proxy-like 403/502 responses, refuse connections, and stall under a short deadline.
Record 1.4.0 events as fixtures. Replace version claims only with captured evidence.

`--network none` prevents vendor contact while permitting in-container loopback fixtures.
This fixture does not prove host-model reachability or network policy.
The recording socket does not prove routd's hold or authorization checks.
The live gate uses the actual routd socket.

## Documentation and live evidence close the change

Set `ARIZUKO_BACKEND=pi` on a throwaway instance or an explicitly operator-chosen setup.
Use existing model/effort settings and one supported credential source.
Send one addressed message that requires `reply`.
Record the guest's `mcp__arizuko__reply`, the socket's wire `tools/call` for `reply`,
its routd audit/span bound to the turn and folder, the bot message, and successful `submit_turn`.
The acceptance fails if only terminal text appears or another delivery path bypasses `gated.sock`.
Use existing hold/grant tests to establish enforcement; a recording socket proves only transport.

For the local privacy claim, retain model-server and egress evidence for that same turn.
Name the actual model, endpoint, and credential source in the record.
Live configuration, deployment, publication, and release stay outside this specification-editing task.
Implementation closeout requires the build, lint, short Go tests, Bun tests, typecheck, image smoke,
repo/web documentation checks, and live gate in the map.
The release record uses v0.75.4 from this main release line and the project's release gates.

## Deferred credential ownership

1. **OWNER DECISION — acceptance setup and borrowed-login renewal.**
   Recommendation: use a throwaway setup, a valid borrowed Codex snapshot, and an operator-managed native provider declaration.
   Pi refreshes only its temporary credential copy. Rotated credentials do not update the read-only host login.
   Accept snapshot-only borrowing for this release. Durable shared renewal needs a separate ownership contract.
   No runed change or host credential write belongs in this bridge.

Settled: keep pi's default context window; omit pi Stop while submission supplies the nudge.
Settled (owner, 2026-11-08: "pi local is just a way to use it … we dont need to do anything special"): a local model is one of pi's native providers and gets no arizuko mechanism. The operator declares it like any provider and adds its host to `network_rules`; a model reachable only on host loopback is outside this spec.
The pin is 1.4.0 from the supplied package, not a claim about the latest npm version.

## Pi API evidence comes from the pinned package

All paths below are relative to `/home/onvos/.claude/jobs/58eb9274/tmp/pi-pkg/`.
`e14/package` is coding-agent 1.4.0, `ai14/package` is pi-ai 1.4.0, and `mcp14/package` is pi-mcp 1.4.0.
These are files read for this revision. Static verification is not runtime acceptance.

| Claim used by the design                                                                                                        | File read                                                                                                                                                            |
| ------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Package name/version, pi-mcp dependency, Node >=22.19.0                                                                         | `e14/package/package.json`                                                                                                                                           |
| Native MCP config, stdio fields, direct exposure, `mcp__` names, pipeline hooks, initial direct-tool wait                       | `e14/package/docs/mcp.md`                                                                                                                                            |
| Project MCP config depends on trust; file config can override extension registrations                                           | `e14/package/dist/extensions/mcp/config.js`, `e14/package/docs/mcp.md`                                                                                               |
| Stdio transport inherits process env unless inheritEnv is false                                                                 | `mcp14/package/dist/transports/stdio.js`                                                                                                                             |
| Explicit builtin:mcp works under no-extensions; explicit skills work under no-skills                                            | `e14/package/docs/cli.md`                                                                                                                                            |
| no-approve, offline, model/provider syntax, thinking levels and clamping, session and prompt flags                              | `e14/package/docs/cli.md`                                                                                                                                            |
| PI_CODING_AGENT_DIR replaces the config directory; HTTP/HTTPS proxy support                                                     | `e14/package/dist/config.js`, `e14/package/docs/environment-variables.md`                                                                                            |
| Edit uses path and edits array; fragments match the original file                                                               | `e14/package/dist/core/tools/edit.d.ts`                                                                                                                              |
| Write uses path and content                                                                                                     | `e14/package/dist/core/tools/write.d.ts`                                                                                                                             |
| Native edit applies fuzzy matching and line-ending handling; native tools resolve tilde paths                                   | `e14/package/dist/core/tools/edit.js`, `e14/package/dist/core/tools/edit-diff.js`, `e14/package/dist/core/tools/path-utils.js`                                       |
| Public native tool factories accept operations overrides; registered names replace built-ins                                    | `e14/package/dist/index.d.ts`, `e14/package/dist/core/tools/edit.d.ts`, `e14/package/dist/core/tools/write.d.ts`, `e14/package/examples/extensions/tool-override.ts` |
| tool_call input is mutable; blocking and result hooks exist                                                                     | `e14/package/dist/core/extensions/types.d.ts`, `e14/package/dist/core/extensions/runner.js`                                                                          |
| agent_before_settle exists as a continuation point                                                                              | `e14/package/dist/core/extensions/types.d.ts`                                                                                                                        |
| Session header, delta-only updates, authoritative message_end, retry events, agent_end semantics; print versus RPC framing      | `e14/package/docs/json.md`                                                                                                                                           |
| Assistant usage fields, dollar cost, and stop reasons                                                                           | `e14/package/docs/message-types.md`, `ai14/package/dist/types.d.ts`                                                                                                  |
| Missing session ID emits "No session found matching"                                                                            | `e14/package/dist/main.js`                                                                                                                                           |
| Native models.json APIs, metadata, headers, compatibility settings; custom defaults 128000/16384                                | `e14/package/docs/models-configuration.md`, `e14/package/dist/core/model-runtime.js`                                                                                 |
| Factory-time registerProvider precedes model selection; endpoint-only overrides preserve catalogs; supplied models replace them | `e14/package/docs/custom-provider.md`, `e14/package/dist/core/extensions/types.d.ts`                                                                                 |
| Model config composes above registrations; configured key handling                                                              | `e14/package/docs/custom-provider.md`, `e14/package/dist/core/provider-composer.js`                                                                                  |
| Key values interpolate $NAME or ${NAME}; bare strings are literals                                                              | `e14/package/dist/core/resolve-config-value.js`                                                                                                                      |
| Anthropic recognizes ANTHROPIC_OAUTH_TOKEN and ANTHROPIC_API_KEY; OpenAI recognizes OPENAI_API_KEY                              | `ai14/package/dist/providers/anthropic.js`, `ai14/package/dist/providers/openai.js`                                                                                  |
| Native openai-codex OAuth; credential fields access, refresh, expires, accountId                                                | `ai14/package/dist/providers/openai-codex.js`, `ai14/package/dist/auth/oauth/openai-codex.js`, `ai14/package/dist/auth/types.d.ts`                                   |
| Auth storage defaults to agent-directory auth.json                                                                              | `e14/package/dist/core/auth-storage.js`, `e14/package/dist/core/auth-storage.d.ts`                                                                                   |
| Finite maxRetries and separate provider retry settings                                                                          | `e14/package/dist/core/settings-defaults.js`, `e14/package/dist/core/settings-manager.js`, `e14/package/dist/core/agent-session.js`                                  |

No native import of Codex auth.json, CODEX_API_KEY, or CLAUDE_CODE_OAUTH_TOKEN is assumed.
Searches covered unpacked coding-agent and pi-ai auth, provider, and runtime JavaScript, excluding bundles and source maps.
The snapshot conversion and aliases are implemented in ant with fixtures. Live acceptance remains deferred.
