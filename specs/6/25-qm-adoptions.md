---
status: partial
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

## 2 — the request carries its own policy (declined 2026-09-05)

**What qm does.** `src/auth/capability-token.ts` mints ONE signed, 1-hour
credential with an `aud` discriminator across six audiences (control plane,
OAuth consent, credential broker, egress proxy, blob transfer, secret drop).
The claims carry the **policy itself** — `egress`, `credentials`, `memory:
{write, orgWrite, read[]}`, `grants[]`, `scopeVersion` — not a key to look one
up. The egress proxy needs no database: it verifies the signature and reads
`claims.egress`. Identity rides the request (`proxy-authorization`), so one
proxy serves every scope.

**What arizuko already has.** Two credentials carry identity with the request
today, both plain ES256 JWTs from authd: the `service:proxyd` transit proof
(`auth.ProxydTransit`) and every user or service bearer, with `scope` and
`arz/folder` as claims (`5/1`). Attenuation exists as an issuer round trip —
`POST /v1/tokens` in downscope mode (`authd/http.go:311`): same `sub`, scope ⊆
the parent's, `parent_jti`, TTL capped at the parent's. That is the property
`L4` names, already held: identity from the credential, never from the
transport. The one verifier with no token is the egress proxy, and its policy
is its argv — `container/egress.go` `startEgressProxy` runs one proxy per
spawn with `--client <spawn ip> --allow <list>` (`--allow-all` for the `*`
row), and the process dies with the turn.

**Why a claim adds nothing there.** A signed claim proves which principal is
on the other end of a connection. The per-spawn proxy has exactly one, pinned
by `--client` and by the folder network, and the list it enforces was set by
runed — the same parent that would have to obtain the claim. Verifying one
re-derives what the process already is, and costs: runed holds no signing key
(only authd signs, `5/1`), so every spawn adds a `POST /v1/tokens` round trip;
the egress binary grows a verifier and needs JWKS reach to authd; the agent's
HTTP client has to send `Proxy-Authorization` (not measured for the claude,
codex and pi harnesses); and token expiry becomes a second revocation path
beside the one that exists — the process ends — two paths that drift. The
design `6/24` replaced leaked because a REGISTRY went stale; the per-spawn
proxy has no registry, so there is nothing for a claim to make safe.

**The precondition claim was wrong.** `6/22` step 3's forward-auth endpoint
shipped 2026-09-04 without this decision, and its response does not mint per
request: it copies the cached `auth.TokenSource` bearer (`attachTransit`,
`proxyd/main.go:773`), re-exchanged one minute before its 15-minute expiry
(`L191`). Nothing in `6/22` waits on decision 2.

**When it stops being a no-op, adopt — do not write.** The property earns its
cost only at a SHARED verifier — a proxy serving many spawns, or a KVM host
with one proxy for every VM — where the transport no longer names the tenant.
`6/24` deleted the shared proxy and `sandbox/` enforces by address, so no such
verifier exists. If one returns, the standard is **Biscuit**: Datalog policy
carried in the token's blocks, verification by any service holding the root
public key, offline attenuation by appending a block
([doc.biscuitsec.org](https://doc.biscuitsec.org/)); Go implementation
[`eclipse-biscuit/biscuit-go`](https://github.com/eclipse-biscuit/biscuit-go),
Apache-2.0, tag v2.2.0, last push 2026-07-13, 90 stars — small, alive, v2
blocks. Surveyed 2026-09-05 and ranked behind it: UCAN 1.0 (capabilities,
proof chains, DID keys — [`ucan-wg/go-ucan`](https://github.com/ucan-wg/go-ucan)
targets rc.1 and is thinner); macaroons (caveats, but the verifier holds the
root SECRET — a shared key to distribute;
[`go-macaroon/macaroon`](https://github.com/go-macaroon/macaroon), BSD-3);
SPIFFE SVIDs (identity and `aud` only, no policy, and SPIRE is a server plus an
agent per node); Ory Keto and OpenFGA (Zanzibar tuples in Postgres/MySQL behind
a check API — the database this decision exists to avoid); Cedar (a policy
language, not a token —
[`cedar-go`](https://github.com/cedar-policy/cedar-go) is core-only); and RFC
9068 JWTs (fixed claim families; RFC 8693 narrows only at the issuer's token
endpoint). Hand-rolling a capability-token format is not justified against
Biscuit, and adopting Biscuit is not justified against a proxy whose argv is
its policy.

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

**1 is adopted.** `egress/address.go` resolves a destination and checks the
resulting IP before dialling it — `resolveDest` refuses instance-metadata and
blocked classes, loopback and private are opt-in, and the dial targets the
address that was checked, so a name cannot resolve past the gate. Six tests in
`egress/address_test.go` plus the tunnel path in `connect_test.go` cover it.

1 first: it is a live address-hygiene gap in code shipped to three instances on
2026-08-18, and it is tens of lines. Then 3, which is cheap and pays every time
a substrate is added. 2 is declined: no verifier needs it while the proxy is
per-spawn, and `6/22` step 3 shipped without it. 4 is continuous — one
contract test per seam, as each seam is touched.

## Ties

`6/24` (the per-spawn proxy this hardens) · `6/22` (step 3's forward-auth,
which shipped without decision 2) · `6/16` (the matrix decision 3 turns into a
value) ·
MITM declined 2026-08-18 (this is address hygiene, not interception) ·
`5/9` (anteval, which decision 4 explicitly does not replace) · BUGS `L54`
(the seam decision 4 exists to cover) · `L4` (identity from the credential,
never from the transport — the property decision 2 found already held) ·
`L191` (the forward-auth proof is the cached service bearer, not a per-request
mint).
