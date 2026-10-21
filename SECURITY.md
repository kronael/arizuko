# Security

This file holds the security model of arizuko and the boundaries that enforce
it. A daemon with a large attack surface keeps its own `SECURITY.md` next to
its source. This file is the index and the model that crosses daemons.

## Three isolation axes separate groups, users and daemons

1. **Group isolation.** Each group has its own bind mounts. No path inside the
   guest of one group resolves to a file or socket of another group.
2. **User isolation.** A user reaches a group only through `acl` allow rows,
   directly or through `acl_membership`. OAuth login becomes a JWT at the
   `proxyd` edge, and `auth.Authorize` enforces the rows.
3. **Daemon isolation.** Adapters reach `routd` only over the internal Docker
   network, with ES256 service tokens that `authd` mints. Each daemon reaches
   only the tables that its own store access key allows. A server policy limits
   the key to the owners that the daemon needs.

### The group is the tenant boundary

The unit of isolation is the group, not the turn. Each group runs in its own
KVM guest. The guest has its own filesystem, its own tap, its own view of the
database and its own egress allowlist. The groups `solo/inbox` and `corp/eng`
cannot see the files, sockets, network state or messages of each other. The VM
boundary, the virtio-fs shares and the scoping of the MCP socket to one folder
enforce this. No policy inside the agent is involved.

Each group has these isolated parts:

- **Filesystem.** Only `groups/<folder>/` mounts at `/home/node/`.
  `groupfolder.Resolver` rejects `..` and reserved paths, see `groupfolder/folder.go` and `buildMounts` in `runed/mounts.go`.
- **Network.** The guest has one interface, a tap. The rule set of the tap
  accepts the proxy of that guest and drops every other packet. Egress passes
  through the proxy of that turn and is checked against the allowlist of the
  folder, see `runed/vmnet.go` and `egress/`.
- **Database view.** The MCP socket at `ipc/<folder>/gated.sock` carries the
  identity of the group, from `SO_PEERCRED` and the folder in the socket path.
  Every tool call resolves through `auth.Authorize` against that folder and
  never against a claim of the caller, see `ipc/ipc.go` and `auth/middleware.go`.
- **Secrets.** AES-256-GCM encrypts secrets at rest. The environment of the
  guest holds model credentials and each key that a `shell:env:<KEY>` grant
  names. The broker resolves every other capability credential on the host for
  each tool call. With
  the `litellm` package, the model credential stays out of the guest as well.
  [Secret injection](#secret-injection) draws
  both doors.

The model defends against a malicious agent in group A that tries to reach the
data, files or network of group B. Separate guests, separate taps and separate
database views close that path. The agent runs with `bypassPermissions` inside
its own guest by design. The boundary is the set of mounts and the tap, not the
decisions of the agent in its shell.

`runed` runs as root. It holds `/dev/kvm`, `/dev/vhost-vsock` and
`/dev/net/tun`, and the capabilities `NET_ADMIN` and `SYS_ADMIN`. QEMU inherits
the root credential when `runed` starts it. That privilege does not cross the
tap or the virtio-fs share between group A and group B. A compromised guest
meets the VM boundary and not a shared container kernel. A second control
limits what a guest can write into its own share. `virtiofsd` exports the
writable share from a child process that runs as the uid of the agent. The
guest cannot create a host file with another owner, whatever uid the guest
states. [`runed/README.md`](runed/README.md) has the details.

A fresh guest for each turn solves a different problem. It stops a compromised
turn from affecting the next turn in the same group. Each turn boots one KVM
guest, reads one turn over vsock and powers off, so no compromised process
survives. What persists across turns is the group folder at `/home/node/`: the
conversation state, the diary, the skills and the session transcript. The next
turn loads them again. That stored state is the attack surface between turns of
one group. Per-group isolation removes the threat between tenants.

The management interface of the stored resources is a second route between
tenants, and it is limited to the folder in the same way. A routing rule and a
scheduled task name a target folder. The target is the tenant whose turns fire
or whose task is cancelled. The shared handler (the agent MCP tool and the
operator REST paths `/v1/routes` and `/v1/tasks`) binds every target to the
folder of the caller before it changes anything.

Each interface injects its own containment check:

- The agent socket checks the target against the `acl` rows of the caller with
  `db.AuthorizeTurn(sender, turn-folder, target, "mcp:"+tool, params)`. The
  scope glob of the grant is the containment.
- The operator REST interface allows the caller its own folder and the
  descendants of it, see `ownsFolder` in `routd/server.go`.

A REST operator that has a folder claim therefore stays inside its own subtree.
It cannot re-target or cancel the routes or tasks of another tenant. Only an
empty folder claim widens a REST list or delete to every folder. A named
top-level tenant has depth zero, as root has, so a check by depth would let one
tenant list or delete the rows of a sibling.

### The sender cannot lend the agent authority

A turn has two principals: the folder it runs in and the person who sent the
message. The grants of the folder confer in full. The grants of the sender add
an allow only inside the subtree of the turn folder, and only on the exact
action. Without that limit, the agent of a folder would hold the reach of
everyone who talks to it. A shared chat would then be a place to borrow
authority. That attack is the confused deputy.

A deny counts on both principals, so the deny of a person refuses the turn that
the person triggered, wherever it reaches. The rows are read on every call.
A revoke therefore takes effect at the next tool call inside a live turn, and
the turn carries no token whose scope could outlive it. The figure and the
rules are in [`auth/README.md`](auth/README.md#authorization-is-one-row-and-one-question).

## Each boundary has one mechanism and one place in the code

| Boundary            | Mechanism                                                                                                                                                                                                                                                                                                                                       | Location                                                                                                                     |
| ------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| Group filesystem    | Per-group bind mount; `Resolver.GroupPath` rejects `..`, `\`, reserved                                                                                                                                                                                                                                                                          | `groupfolder/folder.go`, `runed/mounts.go` (`buildMounts`)                                                                   |
| Group MCP socket    | Bridged over vsock, never mounted: a uid-1000 bridge dials the group's `ipcDir` socket, CID-pinned to that guest                                                                                                                                                                                                                                | `runed/vmctl.go`, `ipc/SECURITY.md`                                                                                          |
| MCP peer identity   | `SO_PEERCRED` peer-uid check on each accepted conn                                                                                                                                                                                                                                                                                              | `ipc/ipc.go` (`ServeMCP`), see `ipc/SECURITY.md`                                                                             |
| Agent exec scope    | `bypassPermissions` inside the guest; mounts scoped to the group                                                                                                                                                                                                                                                                                | `ant/src/claude.ts`, `runed/mounts.go`                                                                                       |
| Additional mounts   | `mountsec.ValidateAdditionalMounts` accepts a mount only under a `MOUNT_ALLOWED_ROOTS` root and forces it read-only. It applies no blocklist and no symlink resolution (open: BUGS `L496`). `ValidateFilePath` adds both for MCP file tools only                                                                                                | `mountsec/`                                                                                                                  |
| Web routes          | `/pub/*` + `/health` public; `/priv/*` JWT-gated (serves from `web/priv/`); `/slink/*` forwarded to webd (which 308s → `/chat/<token>/`); `/chat/*` + `/hook/*` are `auth: optional` rows to webd, which holds the token check; rest JWT                                                                                                        | `proxyd/main.go`, `webd/server.go` (`handleSlinkRedirect`), `webd/route_token.go`                                            |
| Chat-MCP            | `/chat/<token>/mcp` — token IS the auth; it authenticates an ANONYMOUS principal whose grant set is the one folder the row records, so the shared `send`/`get_history` gate (`auth.MatchGroups`) admits no other                                                                                                                                | `webd/mcp.go` (`handleChatTokenMCP`)                                                                                         |
| WebDAV write-block  | `.env` / `*.pem` / `.git` write-blocked; `<group>/logs/` read-only                                                                                                                                                                                                                                                                              | `proxyd/main.go` (`davAllow`)                                                                                                |
| Chat identity relay | proxyd stamps `X-User-*` and proves channel via ES256 `service:proxyd` bearer; webd verifies via `auth.ProxydTransit` and resolves the route token itself                                                                                                                                                                                       | `proxyd/main.go` (`attachTransit`), `proxyd/forward_auth.go`, `webd/route_token.go`, `webd/server.go` (`auth.ProxydTransit`) |
| Authn               | GitHub / Google / Discord / Telegram OAuth, and Dex when configured → JWT (15 min) + refresh (30 d)                                                                                                                                                                                                                                             | `authd/oauth.go`, `authd/main.go` (`accessTTL`, `refreshTTL`)                                                                |
| Authz               | Unified `acl` + `acl_membership` → `auth.Authorize`; a row's `params` predicates do per-tool arg gating; `auth.Delegate` bounds re-delegation to a subset of rows held WITH GRANT OPTION                                                                                                                                                        | `auth/authorize.go`, `auth/delegate.go`, `auth/acl.go`                                                                       |
| Channel ingress     | ES256 service token (`AUTHD_SERVICE_KEY`); adapter exchanges key for `service:<daemon>` JWT on startup, presents bearer on every routd call; docker-network only. A channel name is locked to the first principal that registers it; an inbound `chat_jid` must sit under the adapter's prefixes and its `sender` under the adapter's scheme    | `chanlib/run.go`, `chanreg/chanreg.go`, `routd/server.go`, `chanreg/README.md`                                               |
| Slack webhook       | proxyd forwards `/slack/*` → `slakd:8080` verbatim; `X-Slack-Signature` HMAC over `v0:<ts>:<body>` (signing secret); ±5min skew                                                                                                                                                                                                                 | `slakd/bot.go` (verify), `template/services/slakd.yaml` (route), `slakd/README.md` § Threat model                            |
| Email sender auth   | DMARC via pinned `Authentication-Results` authserv-id + operator allowlist; fail → `verb=untrusted`, never promoted to `mention`                                                                                                                                                                                                                | `emaid/imap.go`                                                                                                              |
| Mention promotion   | routd-side `verb=mention` rewrite when parent is bot-authored; one renderer across all adapters, untrusted verbs never promote                                                                                                                                                                                                                  | `routd/server.go`                                                                                                            |
| Secrets at rest     | AES-256-GCM encrypted (`v2:` prefix, key=SHA-256(`SECRETS_KEY`); required — routd refuses to start without it, no `AUTH_SECRET` fallback); comma keyring for rotation; legacy plaintext rows sealed in place by the explicit `arizuko secret <inst> seal-legacy` verb (never on boot, never deleted)                                            | `store/secrets.go`, `cmd/arizuko/secret.go`                                                                                  |
| Secret injection    | The guest's env carries model credentials ONLY: operator anchors from host `.env`, overridden by the triggering user's own `store.EnvProfileKeys` (BYOA). Capability credentials are broker-only — resolved on the host per tool call, narrowed to the keys that call declares, scrubbed from the result. [Secret injection](#secret-injection) | `routd/sibling_db.go` (`EnvProfileSecrets`), `runed/secrets.go` (`mergeSecrets`), `routd/mcp.go` (`ResolveConnectorSecrets`) |
| Onboarding rate cap | Per-gate daily limit from `onboarding_gates` table                                                                                                                                                                                                                                                                                              | `onbod/main.go` (`admitFromQueue`)                                                                                           |
| Network egress      | Default-deny for TCP: the turn's tap has no route but to its own proxy, which holds the per-folder allowlist and serves that turn's address only. UDP/53 has nowhere to go ([No DNS filter](#the-guest-has-no-resolver-so-there-is-no-dns-filter)) and IPv6 is taken off the tap before the link comes up                                       | `egress/`, `store/network.go`, `runed/vmnet.go`                                                                              |
| Store access        | One access key per daemon, signed as itself over TLS; the server's policy bounds each key to the owner prefixes that daemon reaches (`<owner>.<entity>`). A daemon with the address and no key refuses to start                                                                                                                                 | `objstore/dynstore/env.go`, `compose/compose.go`, INSTALL.md § The store server                                              |

Anything that is not in this table is not a security boundary. In particular,
the permissions of a socket file do not separate guests, and host root always
wins. arizuko does not defend against the host operator. The agent guest runs
with `bypassPermissions`, so the boundary is the set of mounts and not the tool
policy.

`forward` is not a privacy boundary. The gate checks `target_jid` only. It
never compares the source message with the audience of the target, see `ipc/ipc.go`. An agent can relay a message from a private chat into a public
chat, so the persona holds the rule against that crossing.

### A route token is a capability URL with an optional identity

`/chat/<token>/` and `/hook/<token>` are capability URLs. The token is the
credential. It is 256 random bits, stored as a sha256 hash in `route_tokens`.
An unknown token returns 404, and a revoke deletes the row. Possession of the
token grants one thing: the right to append messages at the JID of the token.
It does not grant an identity. `webd/route_token.go` composes the two
independently:

- **An anonymous caller** gets the sender `anon:<ip-hash>` and the name
  "Anonymous".
- **A logged-in caller** has a valid JWT, and `proxyd` stamps the `X-User-*`
  headers, which `auth.ProxydTransit` verifies. The sender is the real `sub`
  and name of the caller.

One public link therefore serves both. The identity comes from the JWT session
when one exists, and falls back to anonymous when none exists. The token axis
stays strong in both cases. A stolen JWT does not reveal tokens, and a leaked
token does not impersonate a user.

A third axis is not a credential. The token has an optional `context`, which
is processing instructions from the issuer. The turn shows them to the agent
as `<link-context>`. An authorized issuer sets the context when it mints the
token. The issuer is a grant-gated MCP tool, a scoped REST call, the dashd
admin page or the CLI. The public URL cannot change the context.

<a id="pairing-consent-is-the-boundary"></a>

### Pairing needs the consent of the account owner

A pairing link has the path `/pair/<token>` and the kind `pair` in
`route_tokens`. It writes one thing. It writes the edge `acl_membership(child=<channel JID>, parent=<account
sub>)`. The edge makes an anonymous channel identity resolve to a verified
account. A channel identity holds no grants, ever. The edge is the only bridge
to authority, and it grants nothing new. The paired identity acts as the human,
and the human keeps every grant the human already held.

The edge has a direction, and the direction decides who carries the risk.
Whoever controls `telegram:user/999` can then act as `google:alice`, so Alice
carries all of the risk. The attack is consent phishing. Mallory mints a link
for her own chat, because the mint gate permits nothing else, and sends the
link to Alice.

The defence must sit where the risk sits. That place is the one step that the
account owner performs, the browser confirm at `POST /pair/<token>`, see `webd/pair.go`. The page names the channel identity and states the
consequence in one sentence. The write carries a double-submit CSRF token, see `auth/csrf.go`. `GET` has no side effects, because chat platforms unfurl
links, and an unfurl bot must not spend a pairing.

Two things are not a defence. The ten-minute lifetime is not a defence, because
a minter mints again on demand. The place where the URL is pasted is not a
defence either, because the token names the JID that it binds. Moving the URL
only discloses the secret, which is the phishing case above.

A channel identity has at most one parent that is not a role. The redemption
transaction enforces this. `expandPrincipals` merges parents, so a second
parent would silently give the identity the authority of two humans. Either
end of the edge can remove it with `unpair`. The removal reaches only edges
with `added_by='pairing'`, never role membership.

Only a platform identity can be paired. `issue_pairing_link` refuses a JID that
starts with `anon:`, `web:` or `hook:`, see `pairingTargetFolder` in `routd/route_tokens_resource.go`. `anon:<ip-hash>` names whoever holds the
address next. `web:` and `hook:` name a public endpoint and not a person.
Pairing never merges two accounts. It links one channel identity to one
account. Two people can race to redeem one link, and only one can win, because
the token delete is conditional. The batch of the loser, with its edge, is
refused. [`onbod/README.md`](onbod/README.md#from-an-unrouted-chat-to-a-routed-one)
draws the whole flow from the held chat to the route.

### The public and private web trees are separate

`<data>/web/pub/` and `<data>/web/priv/` are separate filesystem trees. The
URLs under `/pub/` are public and never serve from `web/priv/`. The URLs under
`/priv/` need a valid JWT through `proxyd` and never serve from `web/pub/`. The
guest of each group mounts its own slot at `~/public_html/` for the public tree
and at `~/private_html/` for the private tree. Content moves from one tree to
the other only when the agent copies the bytes.

A request to `/priv/<path>` passes only when the caller holds an allow grant
whose scope covers the folder that owns the slot, see `proxyd/main.go` and `auth.MatchSlot`. `proxyd` reads the scopes of the caller from `acl` on every
request, through `groupsForSub` and then `auth.UserScopes`. It uses the same
glob matcher as the folder gate. This design adds no verb, no mount setting and no separate ACL
table.

- **The URL decides the folder.** The check uses the folder that the request
  path names. It never uses a value that the agent supplies. The agent owns
  what it writes into its slot, and the platform decides who reads it.
- **A grant shares a slot.** To let the users of another group read a slot,
  grant them a scope that covers the owning folder. To revoke, delete that row.
  A mount or a copy is never the way to share.
- **A slot is not a folder.** Slot trees nest on disk. The `~/private_html` of
  folder `atlas` holds the bytes of the slot of `atlas/search`. `MatchSlot`
  therefore admits every path below a prefix that a scope covers. That rule is
  filesystem containment, and it applies only to web slots. A folder decision
  uses `MatchGroups`, where a grant on `atlas` does not cover `atlas/search`.
- **`/pub` does no grant lookup.** The vhost of a world serves its own
  `/pub/<world>/` and never the shared `/pub/` root.

<a id="identity-header-trust"></a>

## Only proxyd stamps identity headers

`proxyd` is the only daemon that stamps the identity headers `X-User-Sub`,
`X-User-Name` and `X-User-Groups`. It strips any value that a client supplies,
authenticates the user and stamps the headers again. Then it proves the channel
to the backends. For that proof it attaches its own ES256 service token, which
`authd` mints, as the `Authorization: Bearer` header. The token has
`sub=service:proxyd`. A backend trusts the stamped headers only when this proof
verifies:

- `auth.ProxydTransit(r, ks)` returns true when the request carries a valid
  authd ES256 bearer with `typ=service` and `sub=service:proxyd`. `webd` and
  `onbod` call it before they act on `X-User-*`. With no JWKS configured,
  which is `ks==nil` in local development, it always returns false. The callers
  decide separately whether to allow the open path for local development, and
  they decide on `ks==nil`.

No shared HMAC secret exists. The pin is the subject claim. Without the pin,
any holder of a valid authd token could reach a backend directly and spoof
`X-User-*`. The subject `service:proxyd` is the check that carries the load, see `auth/middleware.go`.

<a id="handing-the-proof-to-a-fronting-proxy-get-authforward"></a>

### A fronting proxy receives the proof for up to 15 minutes

An off-the-shelf proxy can route, but it cannot hold the bearer of arizuko.
`proxyd/forward_auth.go` therefore answers the auth subrequest of that proxy on
`GET /auth/forward`. The answer has either all of `auth.TransitHeaders`, which
is the identity plus the same bearer, or the 303 to the login page that a
direct request gets.

The proof travels in a response that the fronting proxy reads. The exposure is
the life of the bearer, not the life of a config file. `proxyd` exchanges the
bearer from `AUTHD_SERVICE_KEY` and exchanges it again one minute before the
15-minute expiry, see `accessTTL` in `authd/main.go`. **A proxy host that captures
one answer can present that bearer to any backend until it expires, up to 15
minutes.** The backend check is offline and stateless. `auth.ProxydTransit`
verifies the signature, the expiry and the subject, and it consults no `jti`
list. A captured bearer is therefore the same as a bearer of `proxyd` until
`exp`.

A fresh bearer for each answer does not shorten the window. `authd` would mint
it with the same 15-minute life, and every request would add a network call to
`authd`. `proxyd` therefore hands out the bearer it holds. It never caches the
authorization decision itself.

The operator of a fronting proxy is trusted for that window. Put the proxy
inside the same trust zone as `proxyd`, and not on a rented edge.

## Trust zones

```
┌─ host (trusted) ─────────────────────────────────────────┐
│                                                          │
│  ┌─ arizuko_<instance> (docker-compose, trusted) ──────┐ │
│  │  authd routd runed onbod dashd webd proxyd timed    │ │
│  │  teled   discd   slakd   bskyd   whapd   ...        │ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ KVM guest (per-turn, partially trusted) ───────────┐ │
│  │  claude code + ant/ + skills                        │ │
│  │  runs arbitrary Bash/Edit/WebFetch                  │ │
│  │  scoped to its virtio-fs shares + its own tap       │ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  ┌─ external channels (untrusted) ─────────────────────┐ │
│  │  telegram, discord, whatsapp, web slink, email      │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

## The skill guard scans what the agent writes to ~/.claude

The next session of the agent executes a skill that the agent writes. The
directory `~/.claude/` is therefore the one directory where the writes of the
agent are also its own future instructions. `ant/src/skillguard.ts` is a
`PreToolUse` hook on `Write`, `Edit` and `MultiEdit`. It scans content for a
path under `~/.claude/` against 120 threat patterns. The patterns come from
hermes-agent, in `ant/src/skillguard-patterns.ts`, and three of them are
narrowed locally. A **critical** match refuses the write and names the pattern
and the line, so the agent can rewrite the file and not retry blindly.

- Lower-severity matches do not block. Only critical patterns refuse. A gate on
  the own work of the agent costs more when it blocks legitimate writes. That
  budget covers the 120 heuristic patterns only. Every structural finding
  refuses, whatever its severity, because a `SKILL.md` without `name:` is
  broken and not suspicious.
- `SKILL.md` also gets a frontmatter check. It needs `name:` and a
  `description:` of at most 1024 characters. Skill dispatch matches on those
  fields, so a skill without either is invisible. A YAML block scalar,
  such as `description: >`, is measured on the lines below the key and never
  on the marker.
- The guard judges the file that the write would produce. It applies an `Edit`
  or a `MultiEdit` in memory to the current file first, so a safe fragment
  cannot complete a payload that the whole file holds. It checks frontmatter
  only when the text is the whole file, because a fragment has no frontmatter.
- The scan reads logical lines. It joins a shell continuation (`curl \` and the
  next line) first. That closes the split-line evasion that was demonstrated.
  It does not close the class.
- `Bash` and the `apply_patch` tool of codex write without the write tools, so
  the `PreToolUse` matcher never fires for them. A `skill-rescan` row runs after
  every tool, see `ant/src/policy.ts`. It rescans the files under
  `~/.claude/skills` that changed since the last scan, and it renames a
  dangerous file to `<file>.quarantined`, out of discovery. codex and pi also
  scan the skills directory when they load it, see `ant/src/skills-scan.ts`.
- The guard checks content and not the origin of the content. It has one trust
  level and no list of trusted repositories, because the hook sees the text of
  one write and nothing else.
- Nothing outside `~/.claude/` is scanned. The home of the group and both web
  slots are data, and the mounts and the web tree gate them.
- **The guard fails open.** A crash of the scanner or an unknown tool shape
  allows the write. The guard is not a containment boundary. The VM boundary,
  the egress allowlist and the gated MCP socket are. The guard raises the cost
  of one move: storing a malicious instruction for the next session.

## A package install is bounded before it writes

A package installs rows, routes and files that the operator did not write.
Install therefore bounds the authority of the package before any write, see `parsePkgManifests` in `cmd/arizuko/packages.go`. Without these bounds,
installing a package would hand its author the instance.

- **Core routes are reserved.** A route cannot take a path that core arizuko
  serves. The reserved set comes from `compose.coreProxydRoutes`, so no second
  list exists. `arizuko generate` applies the same rule, because
  `packages upgrade` rewrites a fragment without applying its rows again. One
  exception exists. A daemon that the release itself ships can serve its own
  `/dash/<name>/`.
- **`--folder` bounds the grants.** Every grant scope that a package declares
  must lie inside the folders named at install. This is the same
  subset-of-held rule as the delegation of a person. `**` and `*` are always
  refused, because an operator is exactly such a row. An install without
  `--folder` has a bound of nothing, so a package that declares any grant is
  refused.
- **Install stamps the owner and never reads it.** Install writes
  `owner = package:<name>` on every route and `granted_by = package:<name>` on
  every grant. A manifest that names its own route owner is refused. The owner
  `arizuko` would make the row look core-shipped, and the owner `operator`
  would remove the row from every later `generate`.
- A grant with a predicate on a `folder:` principal is refused. The agent
  socket carries no JWT claims, so that row could never match.

<a id="secret-injection"></a>

## A credential reaches work through one of two doors

Door 1 puts the credential in the environment of the guest at spawn. Door 2
spends the credential on the host, inside one tool call. The door depends on
which of three kinds the key is. The three kinds share no abstraction, and each
has its own spender.

<!-- twin:secrets -->

```
┌───────────────────────┐ ┌───────────────────────┐ ┌───────────────────────┐
│ env-profile key       │ │ capability credential │ │ infra anchor          │
│ the five model keys:  │ │ GITHUB_TOKEN, ...     │ │ SECRETS_KEY, bot      │
│ host .env anchors     │ │ secrets row: folder   │ │ tokens,               │
│ under the caller's    │ │ walk, the user's row  │ │ AUTHD_SERVICE_KEY:    │
│ own user row, BYOA    │ │ wins; a pasted PAT    │ │ host .env only        │
└───────────┬───────────┘ │ or surrogate OAuth    │ └───────────┬───────────┘
            │             └───────────┬───────────┘             │
            ▼                         ▼                         ▼
┌───────────────────────┐ ┌───────────────────────┐ ┌───────────────────────┐
│ EnvProfileSecrets,    │ │ ConnectorSecrets per  │ │ daemon env: never a   │
│ mergeSecrets: the     │ │ tool call, declared   │ │ row, never in a guest │
│ user's key wins       │ │ keys only; OAuth      │ └───────────────────────┘
└───────────┬───────────┘ │ refresh near expiry   │
            │             │ or after a 401        │
            │ door 1:     └───────────┬───────────┘
            │ spawn                   │ door 2: call
            ▼                         ▼
┌───────────────────────┐ ┌───────────────────────┐    ┌──────────────┐
│ guest env, for the    │ │ routd renders the     ├───►│ upstream API │
│ whole turn            │ │ header, sends the     │◄───┤              │
└───────────────────────┘ │ request, scrubs the   │    └──────────────┘
                          │ result for the agent  │
                          └───────────────────────┘
┌───────────────────────────────────────────────────────────────────────────┐
│ door 1 with MODEL_GATEWAY_URL set: spawnSecrets drops the five model      │
│ keys, routd mints MODEL_GATEWAY_KEY for the turn, and the litellm         │
│ container keeps the credential. A shell:env:<KEY> grant adds that one     │
│ folder key to door 1.                                                     │
└───────────────────────────────────────────────────────────────────────────┘
```

<!-- /twin -->

- **Env-profile keys** are the five model keys in `store.EnvProfileKeys`:
  `ANTHROPIC_API_KEY`, `ANTHROPIC_OAUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`,
  `OPENAI_API_KEY` and `CODEX_API_KEY`. The harness subprocess spends them,
  and no tool call exists to attach a broker to. They are therefore a credential
  that a table stores and the guest also receives. The store
  refuses one at folder scope.
- **Capability credentials** are tokens for one service, such as
  `GITHUB_TOKEN`, at folder scope or user scope. They enter the environment
  of the guest only through a `shell:env:<KEY>` grant.
- **Infrastructure anchors** are `SECRETS_KEY`, `AUTHD_SERVICE_KEY` and bot
  tokens. They live in the host `.env` only and are never a `secrets` row.

A folder secret and a daemon environment variable can have the same name and
hold different values. `GH_TOKEN` in `.env` belongs to a daemon. `GH_TOKEN` in
the row of a folder is the value that a connector sends. Identify a credential
by the door that uses it.

### Door 1 puts the credential in the environment at spawn

`EnvProfileSecrets` in `routd/sibling_db.go` reads the own `scope_kind='user'`
rows of the caller and keeps the model keys only. The reader does the
narrowing and not the call site, so a later caller cannot widen it with one
line. `mergeSecrets` in `runed/secrets.go` lays that map over the operator
anchors that `runed` reads from its own environment. `arizuko generate` writes
every key of `store.EnvProfileKeys` into `env/runed.env`, see `writeEnvFiles` in `compose/compose.go`. A key that is absent there reaches no guest.

A `shell:env:<KEY>` grant adds one folder credential to door 1, see `ShellEnvSecrets`. A CLI that the agent drives from its shell has no tool
call to broker. The match is exact. The `(*, **)` row of an operator does not
confer the grant, and `shell:env:*` is the explicit row for all keys. The model
keys never travel this way, even under `shell:env:*`. The scope of the token at
the issuing service is the real bound, because the agent can spend the token
in the shell with any client.

With a model gateway, which means `MODEL_GATEWAY_URL` and the `litellm`
package, door 1 drops the model keys from both sources. `spawnSecrets` does
this. `routd` mints one
virtual key for each turn. The run ceiling is its lifetime, the folder is its
team and the caller is its user. `routd` deletes the key when the turn ends.
The key has no budget of its own. The team of the folder and the user of the
person hold the caps. The gateway container reads the real credential from the
`.env` of the instance. A key that someone reads out of a guest therefore buys
one turn of one folder. `runed` drops a model key that a user owns, with a warning in its log, because
nothing translates it into a gateway key, see BUGS `L404`. [`routd/README.md`](routd/README.md#the-model-gateway) states the
rules for budget, meter and failure.

### Door 2 spends the credential inside a tool call

`ConnectorSecrets(folder, callerSub, required)` resolves only the keys that the
connector or REST tool declares. A folder row resolves by a walk up the
ancestors, and the deepest folder wins. The own user row of the caller wins
over the folder row. The in-process MCP host of `routd` renders the values
into the headers, query or body of the request (`RenderSecrets` in
`ipc/connector.go`, and `ipc/extcall.go`). It scrubs every resolved value from
the result. The operator writes `connectors.toml`, and `routd` reads it once at
boot. That file is the destination allowlist of door 2. The agent picks a tool
and never a host.

`callerSub` is the sender of a human turn. For a timed or system turn it is
`service:routd`, see `turnCallerSub` in `routd/dispatch.go`. It is not the
authorization principal, which is `folder:<path>`. A scheduled task therefore
finds no row of a person. A cron task that needs a capability credential needs
a key at folder scope.

Each resolved key writes one `secret_use_log` row and the `audit_log` row that
reports it, in one write, see `LogSecretUse` in `store/secret_use_log.go`. The row
holds the key, the scope, the status and the latency, and never the value. The
scope is `user`, `folder` or `missing`. It is read after any OAuth refresh, so
a key that the refresh dropped reads as `missing`. Connector discovery also
spends the credential, and it logs as `<connector>_tools/list`.

A transport error reaches the agent as an error that names the connector. The
REST path scrubs the resolved values from that text. Neither path returns the
rendered headers.

### Surrogate OAuth writes the same row

"Connect" at `/dash/me/connections` runs an OAuth flow as the user. It writes
the access token and the refresh token into the same user-scoped `secrets` row
that a pasted PAT would use. Only the writer differs, so the broker needs no
second path. The broker refreshes at call time and never in a worker, see `routd/surrogate_refresh.go`:

- The broker refreshes a token that has less than 60 seconds left. It forces
  one refresh after a 401, because the optimistic `expires_in` of a provider
  can leave a dead token that looks healthy.
- When the provider rejects a refresh token, the broker clears the refresh
  token and the expiry of the row. It drops the key from the call and returns
  an error that tells the user to connect again. A temporary failure keeps the
  stale token.
- Connecting grants nothing. An `mcp:<connector>:*` or `ext:<service>:*` grant
  still gates the tool.

### SECRETS_KEY seals every row

Every value is AES-256-GCM with a `v2:` prefix. `routd` refuses to start
without `SECRETS_KEY`, and every store write refuses without a key. A fallback
to plaintext would write every secret readable on disk while the health checks
pass. `SECRETS_KEY` is a comma-separated keyring. The first key seals, and the
other keys only decrypt. To rotate, put the new key first and keep the old key
after it. Write each secret again, so that the new key seals it. Remove the old
key only after every row is written again. A `v2:` value that no key opens is
an error and never plaintext.

## A tool call is a chokepoint and a raw socket is not

The credential model does not rest on hiding secrets well. It rests on one
fact. **A tool call is a chokepoint, and a raw socket is not.** Four properties
follow from it. They hold for everything that the agent reaches as a tool:

1. **The credential never enters the guest.** The in-process MCP host of
   `routd` renders the secret of a connector into the outbound request on the
   host, see `RenderSecrets` in `ipc/connector.go` and `CallExtTool` in `ipc/extcall.go`. The host scrubs the value from the result before the
   agent sees it. The environment of the guest holds no capability credential.
   This needs no TLS termination and no CA in the middle.
2. **Every call passes one authorization evaluator.** `auth.Authorize` reads
   the `acl` rows. The scope glob of the row is the containment. The rows are
   read live, so a revoke applies to the next call inside the same turn.
3. **A call can wait for a human.** A `hold:mcp:<tool>` rule stops the call at
   the same chokepoint and waits for approval. There is one point of
   interception and no bypass, because only one MCP server exists on the map of
   the agent. The agent socket mounts no interface of `pending_actions`,
   because an agent that could approve would approve its own call. `CheckHold`
   fails open on an unreadable `acl`. It holds nothing and logs an error. The
   call then meets `Authorize`, which denies on the same unreadable table.
   Whether an unreadable `acl` should hold the call is an open decision, see BUGS `L92`.
4. **Every change writes its audit row in its own transaction.** The call is
   attributable to a folder, a tool and a triggering user.

The agent holds exactly one MCP server, the socket of `routd`. The tools of a
connector register on it as local tools named `<connector>_<tool>`. Each
harness is pinned, so a file that the agent writes cannot add a second server.
A server that the harness dials itself never crosses the socket, so no hold,
grant or audit row applies to it:

- claude: `strictMcpConfig: true` makes the `mcpServers` of the turn the only
  server map, so `.mcp.json` in `/home/node` is not read, see `ant/src/claude.ts`.
- codex: `--ignore-user-config` skips `$HOME/.codex/config.toml`, which the
  agent can write, see `ant/src/codex.ts`.
- pi: pi has no MCP client, and its harness profile declares `tools: 'none'`.
  The arizuko verbs never reach a pi turn, see `ant/src/pi.ts`.

A raw HTTP client inside the guest gets none of the four properties. Examples
are `aws`, `gh` and a script that the agent just wrote. The client never
crosses the socket, so no grant, no hold and no per-call audit applies. Only
the host allowlist in the next section applies.

Two consequences follow:

- **A smaller raw-socket surface is a security measure** and not tidiness. A
  capability that becomes a declared connector gains all four properties at
  once. The connector path is the main path, and the egress proxy covers the
  rest. A credential reaches a client through the MCP broker or not at all. An
  opaque in-guest client that holds a credential is out of scope by decision.
- **Scrubbing is substring replacement**, see `scrubCallResult` in `ipc/connector.go` and `scrubSecrets` in `ipc/extcall.go`. It protects against
  a credential that appears by chance in output. It does not defend against a
  caller that chooses its own command and encodes the value. For this reason
  arizuko has no generic tool that runs a command with a credential injected.
  The flexibility and the leak would be the same property.

<a id="network-egress-isolation"></a>

## A turn reaches the network only through its own proxy

Isolation is not a setting. The turn runs in a KVM guest whose only network
interface is one tap. The rule set of the tap confines the guest, and the proxy
behind the tap filters host names. `runed` starts the proxy before the guest
exists, as one process for each turn. The proxy ends with the turn. No switch
and no unfiltered mode exist, and `runed` refuses to start when the proxy
binary is absent.

<!-- twin:egress -->

```
┌──────────────────────────────┐
│ KVM guest                    │  a raw socket to any address,
│ no resolver, one tap,        ├──────────────────────► dropped by the tap
│ HTTPS_PROXY = its own proxy  │  DNS on UDP/53, IPv6
└──────────────┬───────────────┘
               │ TCP to the proxy port: the one accepted path
               ▼
┌─────────────────────────────────────────────────────────────┐
│ tap rule set on the host side, iptables                     │
│ ACCEPT guest ip ► host ip on the proxy port, and the reply  │
│ DROP every other packet on the tap: INPUT, OUTPUT, FORWARD  │
│ disable_ipv6 on the tap before the link comes up            │
└──────────────┬──────────────────────────────────────────────┘
               ▼
┌─────────────────────────────────────────────────────────────┐
│ egress proxy: one process per turn, healthy before boot     ├──► 403 to
│ --client: the guest's address only                          │    any other
│ --allow: network_rules of the folder and its ancestors,     │    peer, or a
│ resolved by routd at dispatch, plus the gateway host        │    host off
│ --allow-all: egress grant, /root turn or a `*` row          │    the list
│ CONNECT spliced, never decrypted; dials the checked address │
└──────────────┬──────────────────────────────────────────────┘
               ▼
          allowed host
```

<!-- /twin -->

- The guest receives `HTTP_PROXY` and `HTTPS_PROXY` set to its own proxy,
  `NO_PROXY=localhost,127.0.0.1`, and
  `NODE_OPTIONS=--require=/app/proxy-shim.js`. The shim makes the built-in
  fetch of Node use the proxy, see `proxyEnv` in `runed/vmnet.go`. curl, wget,
  pip, go and npm use the proxy without help.
- A client that ignores `HTTPS_PROXY` reaches nothing. A process that sets its
  own routes also reaches nothing, because the rules sit on the host side of
  the tap, see `tapRules` in `runed/vmnet.go`. A tap with a partial rule set fails
  the turn, because it would be a guest with a route that nobody granted.
- The proxy serves one address, the address of that guest, and answers 403 to
  every other peer. Without this pin, a second guest on the same host could
  borrow the unrestricted allowlist of a concurrent elevated turn.
- A host that is not on the list gets 403, see `egress/proxy.go`. An IP literal
  matches nothing, because the allowlist holds names.
- A proxy that fails to start, or never reports healthy, fails the turn.
- `routd` resolves the allowlist at dispatch and carries it in
  `RunRequest.EgressAllowlist` (`dispatchRun` in `routd/dispatch.go`, which
  walks the folder ancestry and removes duplicates). `runed` builds that list
  into the proxy at turn start. It adds the host of the model gateway, because
  every turn with a gateway fails without it, see `EgressRules` in `container/egress.go`.
- The default seed is `anthropic.com` and `api.anthropic.com`
  , see `store/seed.go`. An operator adds rules with
  `arizuko network <instance> allow <folder> <target>`.
- The wildcard `"*"` is added when the folder holds the `egress` grant, when
  the turn is an elevated `/root` turn, or when the operator wrote a `*` row with
  `arizuko network`. Without that grant, a folder stays
  on its allowlist, whatever its depth. The default `role:member` does not
  carry the grant.
- The proxy never terminates TLS. arizuko declines MITM egress. Header
  injection was the only job that needed TLS interception, and a CA inside
  every guest costs more than the rest that it covers. A credential reaches a
  client through the broker or not at all. See
  [Secret injection](#secret-injection).

The allowlist limits where the agent reaches over TCP. It does not stop a
model credential in the environment of the guest from leaking to an allowed
host. The `litellm` package closes that gap, because the guest then holds a
gateway key for one turn and not the credential. arizuko does not wire the
own OAuth 2.1 of MCP.

### The guest has no resolver, so there is no DNS filter

The guest ships no resolver. A host name has a meaning only inside a `CONNECT`
line to the proxy, so a DNS query has nowhere to go. The tap drops UDP/53 with
every other packet that is not addressed to the proxy. A resolver that the
agent could reach would be a side channel for arbitrary query names, whatever
the `network_rules` allowlist of the folder said. No resolver exists, so
nothing needs a filter.

### The tap has one address family because the rule set has one

The rule set of the tap is `iptables`, so it says nothing about IPv6. The guest
kernel carries a full IPv6 stack. `runed` therefore takes the address family
off the device. It writes `net/ipv6/conf/<tap>/disable_ipv6` before the link
comes up, see `sealIPv6` in `runed/vmnet.go`. A write that it cannot make fails
the turn.

Without that step, the guest and the host side of the tap each take a
link-local address. The guest then reaches every IPv6 listener in the network
namespace of `runed`, including the `:8080` of `runed`, through a table that
has no drop rules. `runed` writes the setting itself and does not rely on the
default of a container runtime, because a boundary that rests on the default of
another party is not a boundary.

## Only the ipc daemon has its own security file

[`ipc/SECURITY.md`](ipc/SECURITY.md) covers the MCP channel, the `SO_PEERCRED`
check, the mount isolation of each group and the incident history. The
guarantees of the other daemons are in the table above. Add a file next to a
daemon when its threat model outgrows a row.

## Logs and audit rows show who did what

All daemons log structured JSON to standard output with the Go `slog` JSON
handler. The Docker log driver is `none`. Containers run attached under their
systemd unit, so standard output flows directly into journald.

One command reads all service logs of an instance:

```bash
sudo journalctl -u arizuko_<instance> --since "1 hour ago" --no-pager
```

**The audit trail.** Each daemon that owns an `audit_log` serves it read-only
at `GET /v1/audit`, gated on the `audit:read` scope. No human bearer can hold
that scope. The scope list of a user token carries folder globs, and
`auth.scopeMatches` rejects a held value without a colon. Neither `acme/**` nor
the own `**` of an operator satisfies it. Only `service:dashd` holds it. The
page that `dashd` serves, `/dash/audit/`, is limited to operators. The trail is
therefore operator-only by mechanism and not by convention. On the agent socket
of `routd`, the same handler is exposed as `query_audit`. It is denied by
default, needs an `mcp:query_audit` grant, and is pinned to the folder subtree
of the agent. The folder bounds never come from the folder claim of the JWT. An
absent claim is not evidence of operator status. The bounds come only from the
authorization that the caller already passed.

**Sessions and signing keys.** The same mechanism gates the operator interface
of `authd`:

- `GET /v1/signing_keys` needs `signing_keys:read`.
- `GET /v1/sessions` needs `sessions:read`.
- `DELETE /v1/sessions/{family_id}` needs `sessions:write`.

These scopes have the form resource and verb. No human bearer can hold them.
Only `service:dashd` holds them, for its operator-gated `/dash/authd/` page.
Read and kill are separate scopes. A surface that shows the session table does
not gain the ability to end a session. Both tables also refuse a caller that
has a folder claim. A signing key is global to the instance, and a session is
keyed by `sub`. No predicate could contain either, and serving such a caller
everything would be the cross-tenant list-all leak.

Neither read can carry a credential. `priv_pem`, `pub_pem` and `token_hash`
appear in no query and no response struct, and no raw refresh token is stored.
A revoke ends the renewal. The access token that `authd` already issued lives
out its own TTL of about 15 minutes. This is the short-TTL model and not a gap.
No revocation list exists, because the verifiers stay offline. To log out a
whole fleet, retire the active signing key. That action has no wire interface
on purpose. See [`authd/README.md`](authd/README.md#keys-mints-and-logins).

**The access audit.** `proxyd` is the single ingress point for all HTTP
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

`actor_sub` is the authenticated identity. It is empty for public routes. All
traffic passes through `proxyd` before it reaches webd, dashd, vited or davd,
so this log is the authoritative access record. No backend needs its own
access log.

Do not use `docker logs`. The `none` log driver means that Docker stores no
logs. `journalctl` is the only source.

## Report a problem in BUGS.md

Log the problem to `BUGS.md` with a reproducer and a severity. No public
channel exists, because each arizuko instance has one operator.
