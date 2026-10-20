# chanreg

chanreg is routd's side of the channel protocol: the registry of adapters and
the HTTP client that calls them. The adapter side, and the protocol figure, is
[`chanlib/README.md`](../chanlib/README.md).

## Purpose

The registry tracks each registered adapter by name with its URL, JID
prefixes, capabilities, session token and verified service principal. A
health loop pings each adapter's `/health` every 30 s; `ok` and `stale`
count as healthy, and 3 consecutive failures deregister the adapter.
`HTTPChannel` is the routd-side egress client. It is stateless and holds no
queue: it makes one attempt and returns the result. routd owns the durable
record and the retry, the `pending` bot row and its 30 s sweep.

## A name belongs to the first principal that registers it

`POST /v1/channels/register` admits any valid `service:` bearer. The bearer
names the daemon, `service:teled`, while the registration `name` is the
account's `CHANNEL_NAME`: two accounts of one adapter exchange the same
token, so the principal cannot tell them apart and the name does.
`RegisterWithOrigin` therefore locks a name to the verified principal of its
first registration. A later registration of that name by a different
principal is 409; the same principal may register it again after its bearer
rotates. The source IP is recorded with the entry and is not compared. Any
seeded service principal may claim a free name first; taking over a held one
is refused.

An adapter URL must be `http` or `https` and must not name a public address,
unless `CHANNEL_REGISTER_ALLOW_PUBLIC=1` (`validateAdapterURL`).

## Inbound identity is checked against the registration

routd's `POST /v1/messages` checks two fields against the caller's entry
(`routd/server.go`):

- `chat_jid` must start with one of the entry's prefixes (`Owns`). A
  `web:`, `hook:` or bare-folder JID is internal and skips the check.
- `sender` must share the entry's platform scheme (`OwnsScheme`). The check
  is coarser on purpose: prefixes are chat prefixes and a sender is
  `telegram:user/<id>`, so the two share only the scheme. It is not nested
  inside the `chat_jid` check, because the sender is what `Authorize` reads:
  an adapter that could assert another platform's user would escalate. A
  bare sender and an `anon:` sender are exempt.

## Capabilities gate every verb

routd calls an adapter verb only when the adapter registered its capability.
Otherwise `HTTPChannel` returns `UnsupportedError` with no network call, and
the agent gets the hint. Two capabilities differ: without `send_text` a send
is an error, and without `buttons` routd folds the options into text lines
and sends them. The adapter never has to answer for a verb it does
not have, and routd never learns support from platform errors.

| Capability      | Gates                                       |
| --------------- | ------------------------------------------- |
| `send_text`     | `send`, `reply`                             |
| `buttons`       | options drawn as buttons                    |
| `send_file`     | `send_file`                                 |
| `send_voice`    | `send_voice`                                |
| `typing`        | `/typing`                                   |
| `fetch_history` | `/v1/history`                               |
| `post`          | `post`                                      |
| `like`          | `like`                                      |
| `dislike`       | `dislike`                                   |
| `delete`        | `delete`                                    |
| `edit`          | `edit`                                      |
| `pin`           | `pin_message`, `unpin_message`, `unpin_all` |
| `fwd`           | `forward`                                   |
| `quote`         | `quote`                                     |
| `repost`        | `repost`                                    |

An unknown capability is ignored. Which adapter advertises which verb is the
one table in [`EXTENDING.md` § Verb support matrix](../EXTENDING.md#verb-support-matrix).

## Public API

- `New() *Registry`
- `(*Registry).SetBearer(src)`, `Bearer()` — routd's `service:routd` token source
- `(*Registry).Register(name, url, prefixes, caps) (token string, err error)`
- `(*Registry).RegisterWithOrigin(name, url, prefixes, caps, originIP, principal)`
- `(*Registry).Deregister(name)`, `Get(name)`, `ByToken(token)`, `ByPrincipal(sub)`, `All()`
- `(*Registry).Resolve(name, jid)` — the named adapter, else `ForJID(jid)`,
  the first adapter whose prefix owns the JID
- `(*Registry).StartHealthLoop(ctx)`
- `NewHTTPChannel(e *Entry, bearer func(context.Context) (string, error)) *HTTPChannel`
- `(*HTTPChannel).Send`, `SendFile`, `SendVoice`, `Typing`, `FetchHistory`,
  `PostRoundDone`, and the social verbs
- `Entry` — per-adapter row; `HasCap`, `Owns`, `OwnsScheme`

## Dependencies

- `core`, `chanlib`, `audit`

## Files

- `chanreg.go` — Registry, Entry, lookup, URL validation
- `health.go` — periodic health pinger
- `httpchan.go` — `HTTPChannel` (the egress client over HTTP)

## Related docs

- [`chanlib/README.md` § The channel protocol](../chanlib/README.md#the-channel-protocol-register-health-inbound-send)
- [ROUTING.md § Reply Routing](../ROUTING.md#reply-routing) — which adapter carries a reply
