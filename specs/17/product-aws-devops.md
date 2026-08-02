---
status: draft
brand: argus
---

# Product: aws-devops

_An SRE agent in your team's chat. Reads your runbooks, runs
read-before-write against AWS, and each engineer acts with their own
keys — attributed to them in CloudTrail._

Public pitch: `template/web/pub/arizuko/products/aws-devops/index.html`.
Shipped as `ant/examples/aws-devops/` via the existing `--product` flag
on `arizuko create`. NO Go changes, NO migrations, NO new daemons —
everything below composes from primitives already on `main`.

Distinct from `product-ops.md` (a generic runbook + scoped-bash SRE
helper). This product is AWS-specific and its headline is the
per-operator credential model: one agent, but Alice's `aws` calls run
with Alice's keys and Bob's with Bob's.

## What's actually free today

Verified by reading the source before writing this spec:

| Promise on the product page                           | Primitive that already supports it                                                                                                                                                                                                                              |
| ----------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Per-operator AWS keys (BYOA) shadow a shared fallback | `secrets` table carries `ScopeFolder` + `ScopeUser` (`store/secrets.go:88`); `FolderSecretsResolvedForUser` overlays the caller's user keys over folder keys (`store/secrets.go:449`). Test: `routd/connectors_test.go:141`.                                    |
| The overlay keys on the actual triggering human       | `dispatch.go` sets `caller = trigger` (the message sender sub) for any non-timed/non-system turn (`routd/dispatch.go:481`) and resolves `FolderSecretsForUser(folder, caller)` (`routd/dispatch.go:497`). Works on every adapter, not just web chat.            |
| Keys reach the AWS CLI/boto3 in the container         | routd decrypts, runed injects the resolved set as container env; `mergeSecrets(readSecrets(), in.Secrets)` overlays them (`container/runner.go:272`). `aws`/`boto3` read `AWS_*` from env.                                                                      |
| Operator sets shared keys; each engineer sets own     | CLI `secret` (folder) vs `user-secret` (user) — `cmd/arizuko/secret.go:36` / `:73`; self-serve at `/dash/me/secrets`.                                                                                                                                           |
| Agent can't reach non-AWS hosts                       | Default-deny egress; per-folder allowlist walked by ancestry (`store/network.go:92`), enforced by crackbox (`SECURITY.md` "Network egress"). Subdomain match (`crackbox/pkg/match/match.go:59`) means one `amazonaws.com` entry covers every regional endpoint. |
| Prod and staging isolated                             | The group is the tenant boundary — own container, own DB view, own egress, own secrets (`SECURITY.md` "The group is the tenant boundary"). `acme/prod` and `acme/staging` are separate folders.                                                                 |
| No long-lived process holds cloud creds               | Containers are per-turn ephemeral — `docker run --rm` per turn, creds injected at spawn, container exits (`SECURITY.md` "Per-turn freshness"; `ant/CLAUDE.md` "Storage").                                                                                       |
| Multi-channel oncall                                  | `teled`/`slakd`/`discd` adapters route a chat JID to this folder; alerts + destructive-action confirmations land in Telegram, team thread in Slack/Discord.                                                                                                     |
| Per-identity audit                                    | Every mutation writes an `audit_log` row (`store/secrets.go` emits on secret ops; per-daemon `audit_log`); AWS-side, CloudTrail attributes each `aws` call to the key's IAM principal.                                                                          |

## Surface decision — in-container, not a connector

**The agent reaches AWS through the in-container agent surface
(`uvx --from awscli aws ...` / `uv run --with boto3`), NOT through an
`mcp_connector` or `[[ext]]` REST descriptor.** Why:

1. **AWS APIs require SigV4 request signing.** The REST descriptor path
   (`specs/5/13-ext-mcp.md` Handler shape 3) supports only
   `bearer` / `apikey-header` / `apikey-query` / `basic` / `json-body`
   wire forms. The spec itself lists Route53 as "needs SigV4 — low
   priority" and it is unimplemented. A `[[ext]]` block cannot sign an
   AWS call.
2. **The MCP subprocess connector runs inside routd's image** (Handler
   shape 2), which is a minimal Go build — no python, no node, no
   `aws` CLI — and connectors are `per_call` only. Spawning an AWS MCP
   server there is not viable.
3. **The agent (ant) image already has the toolchain** — `uv`, `bun`,
   `go`, `gh`, `sops` (`ant/Dockerfile`) — and the spawn-injected
   `AWS_*` land in its env. `uvx`/`boto3` read them and honor
   `HTTPS_PROXY=crackbox`, so egress stays filtered.

**Trade-off (name it honestly):** the in-container surface loses the
connector's fine-grained per-tool `mcp:`/`ext:` grant gating. For AWS
the destructive gate moves to two layers that are arguably stronger for
cloud ops:

- **arizuko grants + persona discipline** — the `bash` grant gates
  whether the agent may shell out at all; the group `CLAUDE.md`
  enforces read-before-write and operator confirmation before any
  mutating call (same shape as `product-ops.md`).
- **IAM on each operator's keys** — the real blast-radius enforcer.
  Read-first policies wide, destructive actions narrow or denied, per
  key. arizuko attributes the identity; IAM decides what it may do;
  CloudTrail records it per human.

A bespoke AWS Go handler using `registerWithSecrets` + a SigV4 client
would restore per-tool grants inside arizuko, but `registerWithSecrets`
is unbuilt (`specs/5/13-ext-mcp.md` "not yet shipped"). Logged as an
open question, not shipped here.

Consequently the product ships **no** `template/connectors/aws.toml` —
there is nothing a connector could do that the in-container surface
doesn't do better for AWS.

## File set to ship

```
ant/examples/aws-devops/
  PRODUCT.md   manifest (TOML; consumed by cmd/arizuko cmdCreate — reads skills + [[env]])
  CLAUDE.md    group overlay: whose-keys awareness, read-before-write, egress reality,
               incident flow, escalation
  PERSONA.md   (operator-seeded) calm-under-pressure SRE register — optional, not required
  facts/       (operator-seeded) service map, alert definitions, runbooks
```

`PERSONA.md` and `facts/` are operator content, not shipped by the
template — the CLAUDE.md and PRODUCT.md are the platform pieces.

## The per-operator credential model (the headline)

```
Alice DMs the agent "terminate the stuck ec2 instance i-0abc"
   │
   ▼
routd: caller = alice's sub               (dispatch.go:481)
   │
   ▼
FolderSecretsForUser(folder, alice) →     (dispatch.go:497)
   folder AWS_* (team fallback)
   overlaid by alice's user-scoped AWS_*  (store/secrets.go:449 — user wins)
   │
   ▼
runed injects the resolved AWS_* as container env (runner.go:272)
   │
   ▼
per-turn container: uvx aws ec2 terminate-instances ...
   runs as ALICE's IAM principal → CloudTrail logs it as Alice
   │
   ▼
container exits (--rm). No process keeps Alice's keys.
```

Bob triggering the next turn gets Bob's keys in a fresh container. In a
shared incident channel the per-turn-sender model still holds — each
turn's spawn carries that turn's sender's keys, and containers don't
outlive the turn, so there is no cross-turn credential bleed. For
unambiguous attribution, point each engineer at a DM or a linked web
identity so "the caller" is always one known human.

## Honest gaps — what needs operator care

1. **AWS CLI cold-start.** `uvx --from awscli aws` resolves the package
   on first use, which needs `pypi.org` + `files.pythonhosted.org` on
   the egress allowlist and adds a few seconds. Operators who want it
   instant can bake `awscli` into a custom agent image and drop the
   pypi hosts.
2. **No in-arizuko per-action gate on AWS.** Destructive gating is the
   `bash` grant + persona confirmation + IAM (see surface decision).
   There is no `aws:ec2:terminate` grant string today — IAM is the
   fine-grained authority. Document this; don't pretend arizuko gates
   individual AWS actions.
3. **An engineer with no user keys falls back to the folder role.**
   That is intended — but it means "acts as themselves" only holds for
   engineers who set their own keys. Encourage per-operator keys via
   `/dash/me/secrets`; treat the folder role as a shared break-glass.
4. **Egress restricts where, not what leaks.** The allowlist stops the
   agent reaching a non-AWS host; it does not stop an injected secret
   being sent to an allowed host. Standard arizuko caveat
   (`SECURITY.md` "Network egress isolation" caveats).

## Acceptance criteria

1. `arizuko create acme --product aws-devops` exits 0 and prints the
   env checklist (`ANTHROPIC_API_KEY`, `TELEGRAM_BOT_TOKEN`, `WEB_HOST`).
2. `ls /srv/data/arizuko_acme/groups/main/` shows `CLAUDE.md` and the
   seeded `.claude/skills/{bash,commit,find,oracle}/SKILL.md`.
3. `arizuko secret acme set main AWS_ACCESS_KEY_ID --value AKIA...` then
   `arizuko user-secret acme set <sub> AWS_ACCESS_KEY_ID --value AKIB...`
   both succeed; a turn triggered by `<sub>` resolves the user value
   (verify: agent runs `uvx --from awscli aws sts get-caller-identity`
   and the ARN is the user's principal, not the folder's).
4. `arizuko network acme allow main amazonaws.com` — the agent can
   `curl -sI https://ec2.us-east-1.amazonaws.com` (subdomain match)
   but a non-allowlisted host 403s on every path.
5. A mutating request in chat produces a proposal + confirmation prompt,
   not an immediate `terminate` (persona discipline, verify by reading
   the reply).

## Open

- `registerWithSecrets` + a SigV4 Go handler would give per-tool AWS
  grants inside arizuko (`aws:ec2:terminate` as a grant string). Worth
  it once `specs/5/13-ext-mcp.md` M-gap closes. Separate spec.
- STS AssumeRole / short-lived session tokens instead of long-lived
  `AWS_ACCESS_KEY_ID`. There is **no broker to mint one**: the
  `Broker`/`mcp_tokens` capability path was deleted, and a turn is
  credentialed by the SO_PEERCRED socket, not an exchangeable token. Any
  AssumeRole design starts from spawn-time injection
  (`routd/dispatch.go:523`), not from a token exchange, and ties to
  `specs/5/15-surrogate-oauth.md`'s "write token into secrets" shape.
- Whether to seed a starter `facts/` (blank service-map + alert-def
  templates) with the product, or leave `facts/` empty like `support`.
