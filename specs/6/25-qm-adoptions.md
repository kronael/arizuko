---
status: shipped
source: yc-software/qm (MIT), read at 83k LOC 2026-08-20
depends:
  [
    24-crackbox-lightest,
    22-proxyd-cut,
    16-daemon-standalone-matrix,
    ../5/9-agent-capability-eval,
  ]
---

# specs/6/25 — three decisions to take from qm

`github.com/yc-software/qm` is the closest public neighbour to arizuko's
shape: a multiplayer agent harness, Slack plus web, per-person isolated
workspaces with shared memory, files, keychain, permissions, crons and web
apps. MIT, TypeScript, ~83k LOC. It was read as source, not as marketing.

qm owns conversation history in Postgres and replays it into every harness;
arizuko gives the transcript to the harness and makes the **group** the
durable layer (`container/episodes.go`, `runner.go:511,516`, the
`recall-memories` skill). That is a deliberate, better trade: it keeps each
harness native, keeps the provider's KV cache, and makes the durable record
meaning rather than raw tool calls. `6/1` keeps that position.

Three decisions are genuinely better and are adoptable without touching it.

## 1 — egress checks the address, not only the name

**What arizuko does.** `egress/proxy.go:131` matches the CONNECT host against
a name allowlist, then `net.DialTimeout("tcp", r.Host, …)` at `:142` **dials
the name**, resolving a second time. The name that was checked and the address
that is reached are two independent resolutions. There is no address check of
any kind: no link-local, no cloud metadata, no loopback, no private range.

**Why it matters here and not in the agent.** The spawn's network is
`--internal`, so the agent itself has nowhere to send a packet — `6/24`
measured that and it still holds. The proxy is the one container with an
uplink, which makes it the confused deputy. Any allowlisted name that resolves
to `169.254.169.254`, to the docker gateway, or to a private address on the
host's network is dialled on the agent's behalf.

**What qm does.** `src/egress-authz-main.ts` resolves once, rejects the answer
when any returned address is link-local, loopback, `0.0.0.0/8`, a known
metadata host, or (optionally) private, and then returns the **resolved
address** for the data plane to dial. `deploy/egress-proxy/envoy.yaml` sends
it to `cluster: original_dst` keyed on `x-egress-upstream-address`, so the
address checked IS the address connected to. There is no second resolution and
therefore no window to rebind into.

**Adopt.** Resolve once in `handleConnect`, reject the address classes, dial
the resolved IP rather than the name. `egress/` is 233 lines; this is tens.
Keep the SNI-free design — nothing is decrypted, this is address hygiene, not
inspection.

### 1b — deny beats allow, and one grammar at every boundary

Deny-beats-allow lives in the matcher (`egress/match.Allowed`: deny first,
then allow-all, then the allowlist), and the binary takes `--deny`/`EGRESS_DENY`
— qm evaluates deny FIRST the same way (`egressDecision` → `isHostDenied`), so
a deny can carve an exception out of a broad allow. One grammar
(`match.ValidEntry` / `ValidateLists`) is enforced at every boundary that
accepts a rule: the binary refuses to start naming the bad rule;
`store.AddNetworkRule` guards the operator CLI (`arizuko network allow`);
routd's `network_allow` MCP tool applies the same grammar. Refused: schemes,
ports, paths, credentials, IPs/CIDRs, wildcards other than one leading `*.`,
duplicates, a host in both lists. qm validates the policy where it is written
the same way (`parseEgressPolicy`) and fails loudly at write time — a rule an
operator writes and that never matches is a rule that reads as protection and
is not.

`"*"` IS still needed — live rows on krons (`eval`, `fiu`, `rhias/nemo`) and
marinade (`atlas`, `atlas/support`), plus `container/runner.go`'s
elevated-egress append — so it is an explicit mode: `--allow-all` /
`Proxy.SetAllowAll`, with deny and the address checks still applying. The DB
row `*` stays the operator's allow-all marker; `container.startEgressProxy` is
the one seam every producer passes through, and it translates the entry to the
flag, so the magic string never reaches a matcher list. A deny list has no
arizuko-side producer: `network_rules` has no deny kind, and the agent tool
`network_deny` means "remove an allow row", so deny is binary-level.

## 2 — a substrate declares what it can do, and degrades loudly

**What qm does.** `src/sandbox/sandbox.ts` defines `AgentComputerProfile`:
`backend`, `writablePersistence` (`snapshot_to_workspace` | `resident_disk`),
`processSessions`, `egressEnforcement` (`none` | `ip_port` | `domain`), and a
spec of cpus/memory/disk/tools. `effectiveEgressEnforcement` returns `none`
when the control plane has no signing secret or API URL — the capability
downgrades **explicitly, in a named function**, rather than by a feature
quietly not happening.

**Why arizuko needs this.** This repository's failure mode is precisely silent
degradation: `L54` logged a lost transcript at INFO for eleven weeks; `L45`'s
env var stopped applying and looked healthy; `X3` shipped a key to a daemon
with no reader.

**Adopt.** Give each spawn substrate a declared profile in code, and require
the resolver that computes an effective capability to be a named function that
can log or fail when it downgrades. The matrix in `6/16` becomes the doc
rendering of a value, not a second source of truth.

## 3 — agent behaviour is testable without a live instance

**What qm does.** `src/harness/mock-harness.ts` (840 lines at `main`, read
2026-09-06) is a full harness implementation behind the same interface as
Claude Code, Codex, Pi and OpenCode. With `replay.ts` and the recorded tape,
agent behaviour is exercised deterministically, offline, for free.

**What arizuko has.** `anteval` — a black-box prober that needs a live
instance, a minted bearer, an eval folder with raised `MaxChildren`, a
reachable sink, granted tools, and real model spend. It is excellent and it is
also, by construction, run rarely. That is why nothing caught `L54`: the fault
lived between routd's session id and ant's resume guard, and no tier owned
that seam. `tests/feat_continuity_test.go` (2026-08-20) pins it end to end
by extracting ant's own `UUID_RE` from `ant/src/claude.ts` and asserting routd's
minted id passes it — a cross-language contract test with no container.

**Adopted.** The shape is extended: wherever a Go producer feeds a TypeScript
consumer, the contract is a test that reads BOTH sides. Three seams carry one
each, and every one had already broken production.

`container/env_contract_test.go` reads the keys ant takes from the container
env out of `ant/src/*.ts` and asserts `buildArgs` emits each, plus the dual —
an emission nothing reads is dead wiring that looks live. It found `CODEX_HOME`
on the first run: codex's own variable with a fallback in ant, so arizuko does
not emit it, and that exemption now names its owner.

`tests/contract_ant_test.go` carries the other two. The `submit_turn` payload
must name the same fields in ant's `SubmitTurnPayload` and Go's
`ipc.TurnResult`, and a fully-populated payload must decode with no field lost
— the split dropped `result` and emptied every reply for two days while the
frame still arrived. The `<status>` grammar is parsed in BOTH languages
(`router.ExtractStatusBlocks` and ant's `STATUS_RE`), so the two parsers run
over one corpus and must agree; drift means a progress notice is delivered
twice or lost.

anteval stays what it is: manual, operator-invoked, never automated.

## Order

**1 is adopted.** `egress/address.go` resolves a destination and checks the
resulting IP before dialling it — `resolveDest` refuses instance-metadata and
blocked classes, loopback and private are opt-in, and the dial targets the
address that was checked, so a name cannot resolve past the gate. Six tests in
`egress/address_test.go` plus the tunnel path in `connect_test.go` cover it.

1 first: it is a live address-hygiene gap in code shipped to three instances on
2026-08-18, and it is tens of lines. Then 2, which is cheap and pays every time
a substrate is added. **3 is adopted**: the three seams above each carry a
contract test, and a new seam earns one as it is touched.

## Ties

`6/24` (the per-spawn proxy this hardens) · `6/16` (the matrix decision 2
turns into a value) · `5/9` (anteval, which decision 3 explicitly does not
replace) · BUGS `L54` (the seam decision 3 exists to cover) · `L4` (identity
from the credential, never from the transport).
