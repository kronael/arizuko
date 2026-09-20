# Security

arizuko's security model and boundaries. Daemons with non-trivial
surface ship their own `SECURITY.md` next to the source; this file is
the index and cross-cutting model.

## Model

Three isolation axes:

1. **Group isolation** — per-group bind mounts; no path inside one
   group's container resolves to another group's files or sockets.
2. **User isolation** — users reach groups only via `acl` allow rows
   (directly or through `acl_membership`). OAuth → JWT at the proxy
   edge; `auth.Authorize` enforces.
3. **Daemon isolation** — adapters reach routd only over the internal
   docker network using ES256 service tokens minted by `authd`, and each
   daemon reaches only its own owner's tables: its own store access key
   under a server policy bounded to the owners it needs, or one
   bind-mounted `store/<owner>/` subdir on a file instance.

### The group is the tenant boundary

The isolation unit in arizuko is the **group**, not the turn. Each
group runs in its own Docker container with its own filesystem, its
own network namespace, its own DB view, and its own egress allowlist.
`solo/inbox` and `corp/eng` cannot see each other's files, sockets,
network state, or messages — and this is structural, enforced by
docker + bind mounts + MCP folder scoping, not by policy inside the
agent.

What's isolated per group:

- **Filesystem** — only `groups/<folder>/` mounts at `/home/node/`;
  `groupfolder.Resolver` rejects `..` and reserved paths
  (`groupfolder/folder.go`, `container/runner.go` `buildMounts`).
- **Network namespace** — agent container attaches to a Docker
  `internal: true` network with no default route; egress passes
  through that spawn's own proxy against the per-folder allowlist
  (`container/egress.go`, `egress/`).
- **DB view** — the MCP socket at `ipc/<folder>/gated.sock` carries
  the group's identity (`SO_PEERCRED` + socket-path folder); every
  tool call resolves through `auth.Authorize` against that folder,
  not the caller's claim (`ipc/ipc.go`, `auth/middleware.go`).
- **Secrets** — AES-256-GCM encrypted at rest. Container env carries model
  credentials only: the operator anchors from host `.env` plus the
  triggering user's own `store.EnvProfileKeys` (BYOA), which override
  them for that spawn. Capability credentials never enter the container —
  the tool-call-time broker resolves them on the host, per call
  (`specs/5/13-ext-mcp.md`, `specs/5/14-credentials.md`). With the
  `litellm` package the model credential stays out too: the gateway
  container holds it, and the agent gets `MODEL_GATEWAY_KEY`, a virtual
  key minted for that one turn, carrying that folder's remaining daily
  budget and expiring with the run (`specs/6/23-llm-gateway.md`).

The threat the model defends against is a malicious agent in group A
trying to reach group B's data, files, or network. Separate containers
on separate networks with separate DB views close that path
structurally. The agent runs with `bypassPermissions` inside its own
container by design — the boundary is the mount set and the network
namespace, not what the agent decides to do with its shell.

Per-turn freshness solves a different problem: a compromised turn
affecting the next turn in the **same** group. The container is
already per-turn fresh — each turn runs a `docker run --rm` container
that reads one turn from stdin and exits, so no compromised process
survives. What persists across turns is the group folder
(`/home/node/`: conversation state, diary, skills, session jsonl),
re-loaded each turn. That persisted state — not a long-lived process —
is the same-group cross-turn surface; stronger per-turn isolation (a
KVM micro-VM) is the opt-in posture for it. Per-group isolation is what
makes the cross-tenant threat go away.

The cold-tier **management** surface is a second cross-tenant vector, and
folder-contained the same way. Routing rules and scheduled tasks name a
_target_ folder — the tenant whose turns fire, or whose task is
cancelled — so their shared handler (agent MCP tool + operator REST
`/v1/routes`, `/v1/tasks`) binds every target to the caller's folder
before mutating. Containment is an injected per-face predicate: the agent
socket resolves the target against the caller's `acl` rows
(`db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)` — the grant's scope
glob IS its containment), the operator REST face applies own-or-descendant
folder containment (`ownsFolder`). A folder-scoped REST operator is thus
confined to its own subtree — it cannot re-target or cancel another
tenant's routes or tasks.

## Boundaries

| Boundary            | Mechanism                                                                                                                                                                                                                                                                                                                                                      | Location                                                                                                                        |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| Group filesystem    | Per-group bind mount; `Resolver.GroupPath` rejects `..`, `\`, reserved                                                                                                                                                                                                                                                                                         | `groupfolder/folder.go`, `container/runner.go` (`buildMounts`)                                                                  |
| Group MCP socket    | Per-group mount of `ipcDir` → `/run/ipc`; `Resolver.IpcPath` rejects                                                                                                                                                                                                                                                                                           | `container/runner.go` (`buildMounts`)                                                                                           |
| MCP peer identity   | `SO_PEERCRED` peer-uid check on each accepted conn                                                                                                                                                                                                                                                                                                             | `ipc/ipc.go` (`ServeMCP`), see `ipc/SECURITY.md`                                                                                |
| Agent exec scope    | `bypassPermissions` inside container; mounts scoped to the group                                                                                                                                                                                                                                                                                               | `ant/src/index.ts`, `container/runner.go`                                                                                       |
| Additional mounts   | `ValidateAdditionalMounts` + `ValidateFilePath` blocklist/symlink guard                                                                                                                                                                                                                                                                                        | `mountsec/`                                                                                                                     |
| Web routes          | `/pub/*` + `/health` public; `/priv/*` JWT-gated (serves from `web/priv/`); `/slink/*` forwarded to webd (which 308s → `/chat/<token>/`); `/chat/*` + `/hook/*` are `auth: optional` rows to webd, which holds the token check; rest JWT                                                                                                                       | `proxyd/main.go`, `webd/server.go` (`handleSlinkRedirect`), `webd/route_token.go`                                               |
| Chat-MCP            | `/chat/<token>/mcp` — token IS the auth; it authenticates an ANONYMOUS principal whose grant set is the one folder the row records, so the shared `send`/`get_history` gate (`auth.MatchGroups`) admits no other                                                                                                                                               | `webd/mcp.go` (`handleChatTokenMCP`)                                                                                            |
| WebDAV write-block  | `.env` / `*.pem` / `.git` write-blocked; `<group>/logs/` read-only                                                                                                                                                                                                                                                                                             | `proxyd/main.go` (`davAllow`)                                                                                                   |
| Chat identity relay | proxyd stamps `X-User-*` and proves channel via ES256 `service:proxyd` bearer; webd verifies via `auth.ProxydTransit` and resolves the route token itself                                                                                                                                                                                                      | `proxyd/main.go` (`attachTransit`), `webd/route_token.go`, `webd/server.go` (`auth.ProxydTransit`)                              |
| Authn               | GitHub / Google / Discord / Telegram OAuth → JWT (1h) + refresh (30d)                                                                                                                                                                                                                                                                                          | `auth/web.go`, `auth/oauth.go`                                                                                                  |
| Login throttle      | 5 POST `/auth/login` per IP per 15min, in-memory                                                                                                                                                                                                                                                                                                               | `auth/web.go`                                                                                                                   |
| Authz               | Unified `acl` + `acl_membership` → `auth.Authorize`; a row's `params` predicates do per-tool arg gating; `auth.Delegate` bounds re-delegation to a subset of rows held WITH GRANT OPTION                                                                                                                                                                       | `auth/authorize.go`, `auth/delegate.go`, `auth/acl.go`                                                                          |
| Channel ingress     | ES256 service token (`AUTHD_SERVICE_KEY`); adapter exchanges key for `service:<daemon>` JWT on startup, presents bearer on every routd call; docker-network only                                                                                                                                                                                               | `chanlib/run.go`, `chanlib/chanlib.go` (`bearer`)                                                                               |
| Slack webhook       | proxyd forwards `/slack/*` → `slakd:8080` verbatim; `X-Slack-Signature` HMAC over `v0:<ts>:<body>` (signing secret); ±5min skew                                                                                                                                                                                                                                | `slakd/bot.go` (verify), `template/services/slakd.yaml` (route), `slakd/README.md` § Threat model                               |
| Email sender auth   | DMARC via pinned `Authentication-Results` authserv-id + operator allowlist; fail → `verb=untrusted`, never promoted to `mention`                                                                                                                                                                                                                               | `emaid/imap.go`, spec 10/17                                                                                                     |
| Mention promotion   | routd-side `verb=mention` rewrite when parent is bot-authored; one renderer across all adapters, untrusted verbs never promote                                                                                                                                                                                                                                 | `routd/loop.go`, spec 6/J                                                                                                       |
| Secrets at rest     | AES-256-GCM encrypted (`v2:` prefix, key=SHA-256(`SECRETS_KEY`); required — routd refuses to start without it, no `AUTH_SECRET` fallback); comma keyring for rotation; legacy plaintext rows sealed in place by the explicit `arizuko secret <inst> seal-legacy` verb (never on boot, never deleted)                                                           | `store/secrets.go`, `cmd/arizuko/secret.go`                                                                                     |
| Secret injection    | Container env carries model credentials ONLY: operator anchors from host `.env`, overridden by the triggering user's own `store.EnvProfileKeys` (BYOA). Capability credentials are broker-only — resolved on the host per tool call, narrowed to the keys that call declares, scrubbed from the result (`specs/5/13-ext-mcp.md`, `specs/5/14-credentials.md`). | `routd/sibling_db.go` (`EnvProfileSecrets`), `container/runner.go` (`mergeSecrets`), `routd/mcp.go` (`ResolveConnectorSecrets`) |
| Onboarding rate cap | Per-gate daily limit from `onboarding_gates` table                                                                                                                                                                                                                                                                                                             | `onbod/main.go` (`admitFromQueue`)                                                                                              |
| Network egress      | Default-deny; the spawn's network has no route out and its own proxy holds the per-folder allowlist, serving that spawn's address only                                                                                                                                                                                                                         | `egress/`, `store/network.go`, `container/egress.go`                                                                            |
| Store access        | One access key per daemon, signed as itself over TLS; the server's policy bounds each key to the owner prefixes that daemon reaches (`<owner>.<entity>`). A file instance binds one `store/<owner>/` subdir per container instead                                                                                                                              | `objstore/dynstore/env.go`, `compose/compose.go`, INSTALL.md § The store server                                                 |

Anything not in this table is not a security boundary. In particular:
socket filesystem permissions alone do not separate containers, and
host root always wins — arizuko does not defend against the host
operator. The agent container runs with `bypassPermissions`; the
boundary is the mount set, not the tool policy.

### Route tokens: capability URL + optional identity overlay

`/chat/<token>/` and `/hook/<token>` are capability URLs: the token IS
the credential (256-bit random, sha256 at rest in `route_tokens`, 404
on miss, revoke = delete the row — spec `specs/5/W-webhook-routes.md`).
Possession grants exactly one thing: append messages at the token's
JID. It does NOT grant an identity — the two axes compose
independently in `webd/route_token.go`:

- **Anonymous caller** → sender is `anon:<ip-hash>` / "Anonymous".
- **Logged-in caller** (valid JWT, proxyd-stamped `X-User-*` verified
  via `auth.ProxydTransit`) → sender is the real `sub`/name.

The same public link therefore serves both: identity comes from the
JWT session when present and degrades to anonymous when not, without
weakening the token axis (a stolen JWT doesn't reveal tokens; a leaked
token doesn't impersonate users). A third, non-credential axis — the
token's optional `context` (issuer-authored processing instructions,
rendered to the agent as `<link-context>`) — is set only at mint time
by an authorized issuer (grant-gated MCP, scoped REST, dashd admin,
CLI); the public URL cannot alter it.

### Pairing: consent is the boundary

A pairing link (`/pair/<token>`, `kind='pair'` in `route_tokens` — spec
`specs/5/31-identity-pairing.md`) writes ONE thing: the
`acl_membership(child=<channel JID>, parent=<account sub>)` edge that
makes an anonymous channel identity resolve to a verified account. A
channel identity holds no grants, ever; this edge is the only bridge to
authority, and it grants nothing new — the paired identity acts as the
human, who holds whatever they already held.

The edge is **directional**, and the direction decides who bears the
risk: whoever controls `telegram:user/999` can then act as
`google:alice`. Alice bears all of it. So the attack is **consent
phishing** — Mallory mints a link for HER OWN chat (the mint gate
permits nothing else) and sends it to Alice.

The defence has to sit where the risk sits, which is the one step the
account owner performs: the browser confirm at `POST /pair/<token>`
(`webd/pair.go`). The page names the channel identity and states the
consequence in one sentence, and the write carries a double-submit CSRF
token (`auth/csrf.go`). `GET` is side-effect-free — chat platforms
unfurl links and an unfurl bot must not spend a pairing.

What is NOT a defence: the ten-minute TTL (a minter mints again on
demand) and where the URL is pasted (the token names the JID it binds,
so relocating it only discloses the secret — the phishing case above).
A channel identity has at most one non-role parent, enforced inside the
redemption transaction, because `expandPrincipals` unions and a second
parent would silently hand the identity two humans' authority. Either
end of the edge can drop it with `unpair`; it never reaches role
membership (scoped to `added_by='pairing'`).

### Public vs private web tree separation

`<data>/web/pub/` and `<data>/web/priv/` are **separate filesystem
trees**. `/pub/<X>` URLs are public (no auth) and never serve from
`web/priv/`; `/priv/<X>` URLs require a valid JWT (via proxyd) and
never serve from `web/pub/`. Each group's container bind-mounts its
own slot into `~/public_html/` (public tree) and `~/private_html/`
(private tree); content cannot leak from one to the other except by
the agent explicitly copying bytes. Full model:
`specs/5/V-web-vhosts.md`.

## Identity header trust

`proxyd` is the **sole stamper** of identity headers
(`X-User-Sub`, `X-User-Name`, `X-User-Groups`). It strips any
client-supplied values, authenticates the user, re-stamps the headers,
then proves the channel to backends by attaching its own authd-minted
ES256 service token (`sub=service:proxyd`) as the `Authorization: Bearer`.
Backends trust the stamped headers **only** when that transit proof
verifies:

- `auth.ProxydTransit(r, ks)` — returns true when the request carries a
  valid authd ES256 bearer with `typ=service`, `sub=service:proxyd`. Both
  `webd` and `onbod` call this before acting on `X-User-*`. With no JWKS
  configured (`ks==nil`, local dev), it always returns false — callers
  gate the open-for-local-dev path on `ks==nil` separately.

No shared HMAC secret. The pin is the subject claim: without it, any
holder of a valid authd token reaching a backend directly could spoof
`X-User-*`. The `service:proxyd` subject is the load-bearing check
(`auth/middleware.go`).

### Handing the proof to a fronting proxy (`GET /auth/forward`)

An off-the-shelf proxy can route but cannot hold arizuko's bearer, so
`proxyd/forward_auth.go` answers its auth subrequest with the whole of
`auth.TransitHeaders` — the identity plus that same bearer — or the
303-to-login a direct request gets (`specs/6/22` step 3).

The proof therefore travels in a response the fronting proxy reads,
which bounds the exposure to the bearer's own life rather than to a
config file's: proxyd exchanges it from `AUTHD_SERVICE_KEY` and
re-exchanges a minute before its 15-minute expiry (`accessTTL`,
`authd/main.go`), so **a proxy host that captures one answer can present
that bearer to any backend until it expires — up to 15 minutes.** The
backend check is offline and stateless: `auth.ProxydTransit` verifies
signature, expiry and subject, and consults no `jti` list, so a captured
bearer is indistinguishable from proxyd's own until `exp`.

That is the property to hold in mind when putting a proxy in front:
whoever runs it is trusted for the window, so it belongs inside the
same trust zone as proxyd, not on a rented edge.

## Trust zones

```
┌─ host (trusted) ─────────────────────────────────────────┐
│                                                          │
│  ┌─ arizuko_<instance> (docker-compose, trusted) ──────┐ │
│  │                                                     │ │
│  │  authd routd runed onbod dashd webd proxyd timed │ │
│  │  teled   discd   slakd   bskyd   whapd   ...        │ │
│  │                                                     │ │
│  │  ┌─ agent container (per-group, partially trusted) ┐│ │
│  │  │  claude code + ant/ + skills                    ││ │
│  │  │  runs arbitrary Bash/Edit/WebFetch              ││ │
│  │  │  scoped to /run/ipc + group mounts              ││ │
│  │  └─────────────────────────────────────────────────┘│ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ external channels (untrusted) ─────────────────────┐ │
│  │  telegram, discord, whatsapp, web slink, email      │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

## Agent-authored skills

A skill the agent writes is executed by its next session, so `~/.claude/`
is the one directory where the agent's writes are also its own future
instructions. `ant/src/skillguard.ts` is a `PreToolUse` hook on
`Write`/`Edit`/`MultiEdit`: content bound for a path under `~/.claude/` is
scanned against 120 threat patterns ported verbatim from hermes-agent
(`ant/src/skillguard-patterns.ts`), and a **critical** match refuses the
write with the pattern and line named, so the agent can rewrite rather than
retry blindly.

- Lower-severity matches do not block. Only critical patterns refuse —
  for a gate on the agent's own work, blocking legitimate writes is the
  more expensive failure.
- `SKILL.md` also gets a frontmatter check (`name:`, `description:` ≤ 1024
  chars). Skill dispatch matches on those, so a skill missing either is
  invisible rather than broken.
- Nothing outside `~/.claude/` is scanned; the group home and both web
  slots are data, gated by the mounts and the web tree instead.
- **Fails open.** A scanner crash or an unrecognised tool shape allows the
  write. This is not a containment boundary — Docker, the egress
  allowlist and the gated MCP socket are. It raises the cost of one
  specific move: persisting a malicious instruction into next session.

Spec: [`specs/5/23-skill-guard.md`](specs/5/23-skill-guard.md).

## Why a tool call beats a raw socket

arizuko's credential story is not "we hide secrets well". It is that **the tool
call is a chokepoint and a raw socket is not**, and four properties hang off
that one fact. For anything the agent reaches as a tool:

1. **The credential never enters the container.** A connector's secret is
   rendered into the subprocess environment **host-side**, in routd's
   in-process MCP host (`ipc/connector.go` `renderEnv`, `ipc/extcall.go`), and
   the value is scrubbed from the result before the agent sees it. Container
   env carries no capability credential at all (BUGS `X1`, closed 2026-08-09).
   This is the same property competitors buy with a TLS-terminating proxy, and
   it costs us no CA and no man-in-the-middle.
2. **Every call passes one authorization evaluator.** `auth.Authorize` over
   `acl` rows — the row's scope glob IS the containment, read live so a revoke
   applies to the next call inside the same turn.
3. **A call can be suspended for a human.** A `hold:mcp:<tool>` rule stops the
   call at the same chokepoint and waits for approval (`5/19`). There is one
   interception point and no bypass: only one MCP server exists on the agent's
   map.
4. **Every mutation writes its audit row in its own transaction** — the call is
   attributable to a folder, a tool, and a triggering user.

A raw HTTP client inside the container — `aws`, `gh`, a script the agent just
wrote — gets **none** of the four. It never crosses the socket, so no grant, no
hold, and no per-call audit applies; only the host allowlist below does.

Two consequences we state rather than paper over:

- **Shrinking the raw-socket surface is a security measure**, not just tidiness.
  Every capability that becomes a declared connector gains all four properties
  at once. That is why the connector path is the primary one and the egress
  proxy covers the residue — DECLINED 2026-08-20 along with every other
  tier of it: credentials reach a client through the MCP broker or not at all,
  and an opaque in-container client is out of scope by decision.
- **Scrubbing is substring replacement** (`ipc/connector.go` `scrubResult`).
  It protects against a credential appearing incidentally in output; it is not
  a defence against a caller that chooses its own command and encodes the value.
  That is precisely why arizuko has no generic "run this with a credential
  injected" tool — the flexibility and the leak would be the same property.

## Network egress isolation

When `EGRESS_IMAGE` is set, agent containers attach to a Docker
`internal: true` network with no default route to the internet. The only
path out is that spawn's OWN proxy container, which runed starts before
the agent exists and stops with the turn (spec `6/24`). The proxy runs on
both the internal network and `EGRESS_UPLINK_NETWORK`; the agent runs on
the internal network only.

- Agent containers receive `HTTPS_PROXY=http://<that proxy>:3128` at spawn
  plus `NODE_OPTIONS=--require=/app/proxy-shim.js` so Node's built-in
  fetch honors the proxy (curl/wget/pip/go/npm honor it natively).
  The proxy forwards HTTP and CONNECT-tunnels HTTPS without decrypting.
- Non-cooperating clients fail closed: the internal Docker network
  has no default route, so a client that ignores `HTTPS_PROXY` cannot
  reach the internet at all. This is the enforcement; the proxy is what
  filters WHICH allowed hosts, not what confines. Never give the agent a
  second interface, and never let it share the proxy's namespace
  (`--network container:`) — the proxy is dual-homed by necessity, so its
  namespace carries the route out (measured: a container sharing a
  proxy's namespace reached an unlisted host directly).
- The proxy serves ONE address — the spawn's — and 403s every other peer,
  including the folder's other spawns. Without that, a spawn could borrow
  a concurrent elevated turn's unrestricted allowlist off the shared
  folder network.
- A proxy that fails to start or never reports healthy FAILS THE SPAWN.
  There is no fallback to an unfiltered container.
- Allowlist baked into the proxy by runed at container spawn from
  `store.ResolveAllowlist(folder)` (folder ancestry walk + dedupe).
- Default seed: `anthropic.com`, `api.anthropic.com`. Operators add
  rules via `arizuko network <instance> allow <folder> <target>`.
- Unknown source IP or unmatched host → connection closed silently.
- Egress wildcard (`"*"`) is appended only when the folder holds the
  `egress` grant, or the turn is an elevated `/root` turn
  (`routd/dispatch.go` → `RunRequest.Egress` → `container.Input.EgressOpen`).
  Without that grant a folder stays on the per-folder allowlist, whatever
  its depth — `role:member`, the default, does not carry it.

Caveats:

- The container env holds the operator anchors plus the triggering user's
  own `store.EnvProfileKeys`, which override them for that spawn
  (`routd/sibling_db.go` `EnvProfileSecrets` → `container/runner.go`
  `mergeSecrets`). That reader queries `scope_kind='user'` and keeps only
  the four model keys, so a folder capability credential cannot reach the
  container even if a future caller asks it to. The egress allowlist
  restricts _where_ the agent can reach; it does not prevent leaking a
  model credential to an allowed domain. The `litellm` package closes
  that gap by construction: the container holds `MODEL_GATEWAY_KEY`, which
  buys exactly the gateway's upstream access and nothing else — the
  provider credential never moves. routd mints that key per turn against a
  master key it alone holds, bounds it by the folder's remaining daily
  budget and the run ceiling, and deletes it when the turn ends, so a key
  read out of a container's environment buys one turn of one folder's
  tokens and then nothing.
- The tool-level secret broker is **shipped** (v0.57.0;
  `specs/5/13-ext-mcp.md`, `specs/5/14-credentials.md`): the broker
  middleware (`ipc/ipc.go` `injectSecretsAdapter`) resolves
  `user(caller.Sub)` then `folder(caller.Folder)` at tool-call time, with
  audit rows in `secret_use_log`, so a secret is spent only on the call that
  needs it rather than for the whole spawn. The connector path
  (a `[[connector]]` TOML block + a per-call HTTPS request whose Authorization header
  the broker renders) is part of the same shape; the endpoint set is operator-
  written and immutable after boot, so the agent picks a tool, never a host.
  MCP's own OAuth 2.1 is reachable now that the transport is HTTP but is not
  wired — see `specs/5/13-ext-mcp.md` §"OAuth is the next step, not this one".
- IPv6 is not redirected by the entrypoint script.

**No DNS filter.** The UDP/53 NXDOMAIN listener was deleted with the
shared proxy (spec `6/24`). It was never wired into
`container.Run`, and a single-tenant proxy already sees every destination
in the CONNECT line — a rule expressed twice is a rule that can disagree
with itself. The HTTP/CONNECT 403 in `egress/` is the enforced path.

## Per-daemon docs

- [`ipc/SECURITY.md`](ipc/SECURITY.md) — MCP channel, `SO_PEERCRED`
  check, per-group mount isolation, incident history

Other daemons do not have a dedicated file yet; guarantees are in the
table above. Add one next to the daemon when its threat model outgrows
a row.

## Audit trail and logging

All daemons log structured JSON to stdout (Go `slog` JSON handler).
Docker log driver is `none`; containers run attached under their
systemd unit, so stdout flows directly into journald.

Single command to read all service logs for an instance:

```bash
sudo journalctl -u arizuko_<instance> --since "1 hour ago" --no-pager
```

**Reading the trail**: each daemon that owns an `audit_log` serves it
read-only at `GET /v1/audit`, gated on the `audit:read` scope. That scope is
unreachable by any human bearer — a user token's scope list carries folder
globs and `auth.scopeMatches` rejects a held value without a colon, so neither
`acme/**` nor an operator's own `**` satisfies it. `service:dashd` is the sole
holder, and the page it serves (`/dash/audit/`) is operator-gated, so the trail
is operator-only by mechanism rather than convention. On routd's agent socket
the same handler is exposed as `query_audit`, default-deny behind an
`mcp:query_audit` grant and pinned to the agent's own folder subtree. Folder
bounds never derive from the JWT folder claim — an absent claim is not evidence
of operator status — only from the authorization the caller already passed.
Spec `specs/5/I-tool-call-logging.md`.

**Seeing and ending sessions**: the same mechanism gates authd's operator
surface. `GET /v1/signing_keys` (`signing_keys:read`), `GET /v1/sessions`
(`sessions:read`) and `DELETE /v1/sessions/{family_id}` (`sessions:write`) are
resource:verb scopes no human bearer can hold, held only by `service:dashd` for
its operator-gated `/dash/authd/` page. Read and kill are separate scopes, so a
surface that renders the session table does not thereby gain the ability to end
a session. Both tables additionally refuse a folder-claimed caller: a signing
key is instance-global and a session is keyed by `sub`, so there is no
predicate that could contain one, and serving such a caller everything is the
cross-tenant list-all leak. Neither read can carry a credential — `priv_pem`,
`pub_pem` and `token_hash` are named in no query and no response struct, and no
raw refresh token is persisted at all. Revoking ends the RENEWAL; the access
token already issued lives out its own ~15 min TTL, which is the short-TTL
model, not a gap. Logging out an entire fleet means retiring the active signing
key and has no wire face on purpose. Spec `specs/5/1-auth-standalone.md`.

**Access audit**: `proxyd` is the single ingress point for all HTTP
traffic. It logs every request as a structured JSON line:

```json
{
  "time": "...",
  "level": "INFO",
  "msg": "request",
  "method": "GET",
  "path": "/dash/",
  "status": 200,
  "dur": "1.2ms",
  "actor_sub": "u:alice",
  "remote": "1.2.3.4"
}
```

`actor_sub` is the authenticated identity (empty for public routes).
Because all traffic passes through `proxyd` before reaching webd,
dashd, vited, or davd, this log is the authoritative access record —
no per-backend access logging is required.

**Do not use `docker logs`** — the `none` log driver means Docker has
no stored logs. `journalctl` is the only source.

## Incident log

**2026-04-17 → 2026-04-19 — MCP token preamble outage.** Commit
`2774394` added a server-side token preamble to `ipc.ServeMCP` with no
matching client (ant's socat bridge cannot write a preamble). Every MCP
call rejected for ~48h; agents appeared amnesic. Fixed v0.29.3 (disable)
and replaced v0.29.4 with `SO_PEERCRED`. Full writeup:
[`ipc/SECURITY.md`](ipc/SECURITY.md).

## Reporting

Log to `bugs.md` with reproducer and severity. No public channel —
arizuko is single-operator per instance.
