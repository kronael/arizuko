---
status: draft
source: yc-software/qm (MIT), read at 83k LOC 2026-08-20
depends:
  [
    24-crackbox-lightest,
    22-proxyd-cut,
    16-daemon-standalone-matrix,
    ../5/9-agent-capability-eval,
  ]
---

# specs/6/25 — four decisions to take from qm

`github.com/yc-software/qm` is the closest public neighbour to arizuko's
shape: a multiplayer agent harness, Slack plus web, per-person isolated
workspaces with shared memory, files, keychain, permissions, crons and web
apps. MIT, TypeScript, ~83k LOC. It was read as source, not as marketing.

Most of what it does differently, arizuko should NOT copy. It owns
conversation history in Postgres and replays it into every harness; arizuko
gives the transcript to the harness and makes the **group** the durable layer
(`container/episodes.go`, `runner.go:511,516`, the `recall-memories` skill).
That is a deliberate, better trade: it keeps each harness native, keeps the
provider's KV cache, and makes the durable record meaning rather than raw
tool calls. `6/1` keeps that position.

Four decisions are genuinely better and are adoptable without touching it.

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
inspection, and MITM stays declined (2026-08-18).

### 1b — the matcher is missing deny and missing validation

Two smaller defects the comparison exposed in `egress/match/match.go`:

- **Allow-only.** `Host()` has no deny list. qm evaluates deny FIRST
  (`egressDecision` → `isHostDenied`), so a deny can carve an exception out of
  a broad allow. arizuko cannot express "all of `example.com` except
  `internal.example.com`".
- **`"*"` allows everything**, and IP entries in an allowlist are silently
  skipped. A rule an operator writes and that never matches is a rule that
  reads as protection and is not. qm validates the policy where it is written
  (`parseEgressPolicy` rejects schemes, wildcards, ports, paths, credentials,
  duplicates, and any host in both lists) and fails loudly at write time.

**Shipped 2026-08-20.** Deny-beats-allow lives in the matcher
(`egress/match.Allowed`: deny first, then allow-all, then the allowlist), and
the binary takes `--deny`/`EGRESS_DENY`. One grammar (`match.ValidEntry` /
`ValidateLists`) is enforced at every boundary that accepts a rule: the
binary refuses to start naming the bad rule; `store.AddNetworkRule` guards
the operator CLI (`arizuko network allow`, which had NO validation — a
host:port or an IP was stored and silently dead); routd's `network_allow`
MCP tool swapped `groupfolder.ValidVhostName` (which let `:` and IPs
through) for the same grammar. Rejected: schemes, ports, paths, credentials,
IPs/CIDRs, wildcards other than one leading `*.`, duplicates, a host in both
lists.

`"*"` IS still needed — live rows on krons (`eval`, `fiu`, `rhias/nemo`) and
marinade (`atlas`, `atlas/support`), plus `container/runner.go`'s
elevated-egress append — so it became the explicit mode the spec asked for:
`--allow-all` / `Proxy.SetAllowAll`, with deny and the address checks still
applying. The DB row `*` stays the operator's allow-all marker (no live-data
migration); `container.startEgressProxy` is the one seam every producer
passes through, and it translates the entry to the flag, so the magic string
never reaches a matcher list. A deny list has no arizuko-side producer yet —
`network_rules` has no deny kind, and the agent tool named `network_deny`
already means "remove an allow row" — so deny is binary-level until a
schema decision earns its own spec.

## 2 — the request carries its own policy

**What qm does.** `src/auth/capability-token.ts` mints ONE signed, 1-hour
credential with an `aud` discriminator across six audiences (control plane,
OAuth consent, credential broker, egress proxy, blob transfer, secret drop).
The claims carry the **policy itself** — `egress`, `credentials`, `memory:
{write, orgWrite, read[]}`, `grants[]`, `scopeVersion` — not a key to look one
up. The egress proxy needs no database: it verifies the signature and reads
`claims.egress`. Identity rides the request (`proxy-authorization`), so one
proxy serves every scope.

**Why this is the answer to the shared proxy.** The design `6/24` replaced
kept a registry of
`(source_ip, id, allowlist)` and leaked when an entry was not unregistered.
`6/24` cut that by giving every spawn its own proxy — correct, and the right
call at the time. qm shows the shared model is safe when identity is **proved
per request** instead of inferred from the source address. That is the same
correction `L4` records for folders: read identity from the credential, never
derive it from the transport.

**Adopt, narrowly.** Do NOT reinstate a shared proxy — `6/24` shipped and the
per-spawn model is simpler and stronger. Adopt the property: arizuko already
mints service bearers (`auth.ProxydTransit`) and already has a per-spawn
`--client` check. Carrying the allowlist in a signed claim rather than in the
proxy's env is what makes policy auditable at the point of use, and it is the
precondition for `6/22` step 3, whose forward-auth response must mint the
transit proof per request.

## 3 — a substrate declares what it can do, and degrades loudly

**What qm does.** `src/sandbox/sandbox.ts` defines `AgentComputerProfile`:
`backend`, `writablePersistence` (`snapshot_to_workspace` | `resident_disk`),
`processSessions`, `egressEnforcement` (`none` | `ip_port` | `domain`), and a
spec of cpus/memory/disk/tools. `effectiveEgressEnforcement` returns `none`
when the control plane has no signing secret or API URL — the capability
downgrades **explicitly, in a named function**, rather than by a feature
quietly not happening.

**Why arizuko needs this.** `6/16` already keeps a matrix in prose. `6/9`'s
KVM sandbox is shipped-but-unwired and its differences from Docker live in a
spec, not in a value any code can read. And this repository's failure mode is
precisely silent degradation: `L54` logged a lost transcript at INFO for
eleven weeks; `L45`'s env var stopped applying and looked healthy; `X3` shipped
a key to a daemon with no reader.

**Adopt.** Give each spawn substrate a declared profile in code, and require
the resolver that computes an effective capability to be a named function that
can log or fail when it downgrades. The matrix in `6/16` becomes the doc
rendering of a value, not a second source of truth.

## 4 — agent behaviour is testable without a live instance

**What qm does.** `src/harness/mock-harness.ts` (802 lines) is a full harness
implementation behind the same interface as Claude Code, Codex, Pi and
OpenCode. With `replay.ts` and the recorded tape, agent behaviour is
exercised deterministically, offline, for free.

**What arizuko has.** `anteval` — a black-box prober that needs a live
instance, a minted bearer, an eval folder with raised `MaxChildren`, a
reachable sink, granted tools, and real model spend. It is excellent and it is
also, by construction, run rarely. That is why nothing caught `L54`: the fault
lived between routd's session id and ant's resume guard, and no tier owned
that seam. `tests/feat_continuity_test.go` (2026-08-20) now pins it end to end
by extracting ant's own `UUID_RE` from `ant/src/index.ts` and asserting routd's
minted id passes it — a cross-language contract test with no container.

**Adopt.** Extend that shape rather than building a mock harness. Wherever a Go
producer feeds a TypeScript consumer (or the reverse), the contract belongs in
a test that reads BOTH sides. Candidates: the `submit_turn` result contract
(`reference_submit_turn_result` — the split broke every reply for two days),
the spawn env keys ant reads from `process.env` (three have been silently dead),
and the prompt envelope tags ant parses. anteval stays what it is: manual,
operator-invoked, never automated.

## Not adopted, recorded so it is not re-proposed

- **Owning conversation history.** Costs a per-harness translation layer and
  re-sends the whole conversation every turn, forfeiting the provider's cache.
  arizuko's group layer is the better durable record. It does buy qm one thing
  arizuko cannot do — `src/harness/tape-fold.ts` filters a replayed tape per
  audience, redacting tool results a viewer is not entitled to. If per-audience
  redaction ever becomes a requirement, this is the reason to revisit, and only
  then.
- **A durable per-scope sandbox.** A compromised turn would persist exactly the
  way an installed tool does. `6/20` already declined the warm pool; this is the
  same trade one step further.
- **Flat typed scopes.** `parseScopeId` derives the kind by parsing the id
  string — the `L4` bug class arizuko forbids in `CLAUDE.md`. arizuko's
  arbitrary-depth folder with prefix containment is strictly more general.
- **Postgres.** Single-box SQLite WAL is a feature, not a gap.

## Order

1 first: it is a live address-hygiene gap in code shipped to three instances on
2026-08-18, and it is tens of lines. Then 3, which is cheap and pays every time
a substrate is added. 2 is a precondition for `6/22` step 3 and should land
with it. 4 is continuous — one contract test per seam, as each seam is touched.

## Ties

`6/24` (the per-spawn proxy this hardens) · `6/22` (step 3's forward-auth, which
decision 2 unblocks) · `6/16` (the matrix decision 3 turns into a value) ·
MITM declined 2026-08-18 (this is address hygiene, not interception) ·
`5/9` (anteval, which decision 4 explicitly does not replace) · BUGS `L54`
(the seam decision 4 exists to cover) · `L4` (identity from the credential,
never from the transport — decision 2's precedent).
