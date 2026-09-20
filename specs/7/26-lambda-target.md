---
status: draft
depends:
  [
    ../6/42-dynamo-over-postgres,
    ../6/43-shared-store-server,
    ../6/41-qemu-substrate,
    ../6/1-adoption-interop,
    ../6/16-daemon-standalone-matrix,
    ../5/33-paths-roles,
  ]
---

# specs/7/26 — the Lambda target: what arizuko must stop assuming

`6/1` says arizuko publishes contracts **like Lambda** — the agent environment
and the component environment — and that the value is what the environment
gives for free. This spec asks the literal question that framing invites:
**what would it take to run arizuko ON Lambda?**

It is a draft, and it decides nothing yet. What it does is name every
assumption arizuko makes about its host, measure each against the platform,
and say which of them the project should stop making **whether or not** AWS is
ever the target. That is the payoff: most of this list is a portability debt
that a bare-metal deployment also pays, in restart cost and in blast radius.

## 0. The quotas this spec rests on

Read from the AWS Lambda quotas page on 2026-09-20
(https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html).
Every number below is quoted, not remembered:

| Resource                                        | Quota                                                                                                    |
| ----------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| Function timeout                                | 900 s (15 min); 5,400 s (90 min) for Managed Instances, asynchronous or event-source-mapping invocations |
| Memory                                          | 128 MB to 10,240 MB; "At 1,769 MB, a function has the equivalent of one vCPU"                            |
| `/tmp` storage                                  | 512 MB to 10,240 MB                                                                                      |
| Container image code package                    | 10 GB uncompressed                                                                                       |
| Invocation payload                              | 6 MB request and response (synchronous); **200 MB for each streamed response**; 1 MB asynchronous        |
| Streamed-response bandwidth                     | uncapped for the first 6 MB, then 2 MB/s                                                                 |
| File descriptors                                | 1,024                                                                                                    |
| Execution processes/threads                     | 1,024                                                                                                    |
| Concurrent executions                           | 1,000 by default, increasable                                                                            |
| **Lambda MicroVMs: maximum execution duration** | **8 hours (28,800 seconds), not adjustable**                                                             |
| Lambda MicroVMs: architecture                   | ARM64 (AWS Graviton) only                                                                                |

The MicroVM row is the one that changes the answer, and §3 is about it.

## 1. What the store move already settled

`6/42` deleted SQLite, `database/sql` and every local file that was a state of
record. `6/43` puts every deployment on one store server addressed over the
network, and a deployment's identity is an account plus a SigV4 key pair.

That is the whole of the persistence question, and it is **already answered in
the AWS-native direction**: the wire protocol is DynamoDB's, so the same
driver reaches Amazon's service by changing one `BaseEndpoint`
(`6/42` §12 open question 7). No component holds a file it must not lose.

This is worth stating plainly because it inverts the usual order. The part of
a system that normally blocks a serverless move — the database — is the part
arizuko has finished.

## 2. What does not fit, measured against the code

Four assumptions remain, and two of them are hard.

**2.1 A turn is `docker run`.** `container/runner.go:689` builds
`run -i --rm --name … --shm-size=1g` and `:763` appends a `-v` per mount. A
Lambda function cannot start a container. This is the blocker that decides
the shape, and §3 is the answer to it.

**2.2 The turn's credential is a kernel fact.** `ipc/ipc.go:352` accepts on a
unix socket and "verifies each peer via SO_PEERCRED"; `:644` reads
`unix.GetsockoptUcred`. The turn is credentialed by the socket, not by a
token, which is exactly why there is no token to steal. Lambda offers no
local peer to credential, so this mechanism does not port — it must be
**replaced**, not adapted, and the replacement is a security-boundary change
that needs its own spec before any code moves.

**2.3 The per-folder filesystem.** A turn mounts nine mount families from
`groups/<folder>/`. Lambda's `/tmp` is 512 MB–10,240 MB and does not survive
the execution environment, so a folder's tree is either rebuilt per turn from
the store, or held on a network filesystem. Both are real options; neither is
free, and §6 says what measurement chooses.

**2.4 Nine long-lived listeners.** `routd`, `runed`, `authd`, `onbod`,
`timed`, `proxyd`, `webd`, `dashd` and `davd` are HTTP servers. This is the
**soft** part of the list: a request-scoped HTTP handler is what a function
is, and `6/16` already measured each daemon's ability to run alone. `timed`
is not a listener at all — it is a clock, and a clock is an EventBridge rule.

## 3. Three shapes, and why the third is the one

**Shape A — functions everywhere.** Every daemon becomes a function; the turn
becomes a function. **Refused.** A turn is not a 15-minute unit of work; the
existing run timeout is already configurable past it, and a turn that a model
outage makes slow is a turn that must not be killed at a wall the operator
did not choose. `6/42` §13 and the `anteval` gate both record turns running
past 150 s under ordinary conditions.

**Shape B — daemons on Lambda, turns elsewhere.** The nine daemons become
functions; the turn stays on a machine that can spawn. **Viable, and it is a
split deployment** — two operational models, two failure stories. Recorded,
not chosen, because it buys the smaller half.

**Shape C — the substrate is the seam.** `6/41` already replaces `docker run`
with one microVM per turn on `-M microvm`, with the MCP socket over
CID-pinned vsock and the proxy on the host side of a tap. Once a turn is "a
microVM started from an image, addressed over a control channel, torn down
after", **who operates the hypervisor stops being arizuko's concern**. QEMU on
a host and Lambda MicroVMs are two operators of one contract.

The quotas support the reading: Lambda MicroVMs run for up to **8 hours**,
start from an image ("MicroVM images per account per Region"), and are driven
by `RunMicrovm` / `SuspendMicrovm` / `TerminateMicrovm`, with
`CreateMicrovmShellAuthToken` as a separate credential path. Eight hours is
not a wall a turn hits.

**Shape C is the one this spec proposes**, and its consequence is a schedule,
not a rewrite: **`6/41` is the prerequisite for the Lambda target, and the
Lambda target is not a reason to build `6/41`.** `6/41` earns its place on the
boot budget alone. If it lands, this becomes an integration; if it does not,
Shape B is the fallback and it is strictly smaller.

## 4. The per-component verdict

| Component                         | Lambda shape                                  | What must change first                                                                |
| --------------------------------- | --------------------------------------------- | ------------------------------------------------------------------------------------- |
| store                             | Amazon DynamoDB, or `6/43`'s server on a host | nothing — one `BaseEndpoint` (`6/42` §12.7)                                           |
| `timed`                           | EventBridge rule                              | the cron parse already lives in one place (`core.NextRun`)                            |
| `proxyd`                          | function URL or API Gateway                   | it stamps `X-User-*` behind its own bearer; that survives                             |
| `webd`                            | function with response streaming              | SSE fits 200 MB per streamed response; the 2 MB/s tail after 6 MB is far above a chat |
| `authd`, `onbod`, `dashd`, `davd` | request-scoped functions                      | nothing structural; `6/16` measured them standalone                                   |
| `routd`                           | request-scoped, EXCEPT the MCP socket         | §2.2 — the socket credential has no Lambda equivalent                                 |
| `runed` + the turn                | MicroVM, per `6/41`                           | `6/41` itself                                                                         |
| `egress` proxy                    | per-MicroVM, per `6/41`'s tap                 | `6/41` itself                                                                         |

Six of the nine daemons need nothing this spec must design. That is the
measurement that makes the exercise worth writing down.

## 5. What to stop assuming now, target or not

Each of these is a portability debt that also costs on bare metal, so each
**shall** be treated as ordinary work rather than as cloud preparation:

1. **No component shall hold a state of record on local disk.** Already true
   for the store (`6/42`); not yet audited for the per-folder tree (§2.3).
2. **No component shall assume its peer is local.** `SO_PEERCRED` is the one
   violation, and it is deliberate and load-bearing; it needs a designed
   replacement before it is called a defect.
3. **A clock shall be an input, not a loop.** `timed` firing on a schedule it
   does not own is also what makes its behaviour testable.
4. **A turn's lifetime shall be bounded by policy, not by the host.** The
   operator picks the wall, and the substrate enforces it.

## 6. Open questions — each with the measurement that settles it

1. **What a Lambda MicroVM actually is.** The quotas page states duration,
   architecture, image-per-account and the five control APIs. It does not
   state whether a MicroVM takes an OCI image, whether it gets an inbound
   address, or whether a filesystem survives suspend. **Measurement:** run one.
   Until then §3's Shape C is a proposal resting on a quota table, and this
   spec says so rather than filling the gap with a guess.
2. **The per-folder tree.** Rebuild-per-turn against a network filesystem:
   measure the p50 cost of materialising the nine mount families for the
   largest live folder on krons, against the turn's own wall.
3. **The MCP credential replacement.** What attests a turn when there is no
   peer uid? Any answer is a security-boundary change and gets its own spec.
4. **Cost.** A turn is minutes of one vCPU and the model call dominates.
   Measure a week of krons at Lambda's per-ms price against the host's, and
   put the number in the changelog before anyone argues from intuition.

## 7. What this buys

Not cost, and not scale — arizuko's load is one turn per message. It buys
**an operator who is not the user**: `6/1`'s goal is that people run arizuko
instead of their own harness, and the shortest path to "run" is a deployment
that needs no host. Shape C reaches it without a second codebase, because the
substrate contract is the only thing AWS replaces.

## Ties

`6/41` (the microVM-per-turn substrate — the prerequisite, and the reason
Shape C is not a rewrite) · `6/42` (the store engine, and the
`BaseEndpoint` that reaches Amazon) · `6/43` (one store server, the account
boundary) · `6/1` (publish the environment, do not wrap the harness) ·
`6/16` (what a daemon must satisfy to run alone) · `5/33` (the path is the
coordinate) · the `turn_credential` finding behind §2.2.
