# Chat routing

How a message from a chat platform (Slack, Telegram, Discord, …) reaches
a group, and **whether it makes you reply**. This is the `routes` table —
separate from web routing (`web-routing.md`), which uses direct
`web:<folder>` addressing and **no route table**.

## The route table

A flat list of rules. Each inbound message is tested against every rule
**in `seq` order (lower first); the first rule whose predicates all match
wins.** There is no other priority.

| Column   | Meaning                                            |
| -------- | -------------------------------------------------- |
| `seq`    | evaluation order — lower = earlier = higher priority |
| `match`  | space-separated `key=glob` predicates, AND'd        |
| `target` | `<folder>[#<mode>]` — group + optional mode          |

Match keys: `platform`, `room`, `chat_jid`, `sender`, `verb`. Globs use
`path.Match` (`*` doesn't cross `/`). `key=*` means present; `key=` means
absent; omit the key to not filter on it. Empty `match` matches everything.

## Modes — this is what decides whether you speak

The `#<mode>` fragment on `target` selects ingestion mode:

| target            | mode    | effect                                                       |
| ----------------- | ------- | ------------------------------------------------------------ |
| `atlas/general`   | trigger | stores under the folder **and fires a turn** (you reply)     |
| `atlas/general#observe` | observe | stores under the folder, **no turn** — you see it as `<observed>` context only |

**A bare folder target fires a turn on EVERY matching message.** If you
want a channel where you only reply when @-mentioned, you do NOT use a
bare target — you stack two rules (next section). Misreading a bare
target as "observe" is the classic mistake.

`verb=mention` is a *match predicate*, not a mode: the gateway promotes
an @mention (or a reply/reaction to your message) to `verb=mention`
before routing, so a `verb=mention` rule fires only on mentions.

## Canonical pattern: mention-only channel

Reply on @mention, stay silent (observe) otherwise — the correct shape
for a busy channel:

```
seq  match                                  target
9    chat_jid=slack:W/channel/C verb=mention  atlas/general
100  chat_jid=slack:W/channel/C               atlas/general#observe
```

The low-seq mention rule wins for mentions and fires a turn; everything
else falls through to the observe catch-all. **Order matters:** if a bare
`chat_jid=slack:W/channel/C → atlas/general` sat at a lower seq, it would
match first and fire on everything, making both rules above dead. For a
**fully silent** channel (no replies at all, even on mention), use a
single `#observe` rule and drop the mention row.

## Read the table — the tools tell you the mode

`list_routes` / `inspect_routing` annotate every row so you don't have to
parse fragments yourself:

- `mode`: `trigger` | `observe`
- `fires_turn`: true/false
- `triggers_on`: `every message` | `verb=mention` | …
- `explain`: one line, e.g. *"fires a turn on EVERY message → atlas/general"*
- `shadowed_by`: id of an earlier rule that intercepts this one (so it
  never fires — fix or delete it)

Before claiming a channel is observe-only, read `explain`. If it says
"EVERY message", it is a trigger, not observe.

## Editing routes

- `add_route` — append one rule (targeted change; preferred).
- `delete_route` — drop one rule by id.
- `set_routes` — bulk-overwrite your subtree (only for full rewrites).

You can only target your own folder subtree. `chat_jid=web:*` is rejected
(web uses direct addressing).

## Beyond the table

These resolve **before** the route table, in order — keep them in mind
when a message doesn't land where the table predicts:

1. **Engagement** — an active reply/`engage` window pins the chat to the
   engaged folder and fires a turn, bypassing routes (even `#observe`).
2. **Reply chain** — a reply to your message routes back to the folder
   that produced it (`routed_to`).
3. **Sticky** — `@folder` / `#topic` lock a chat; `@` / `#` clear them.
4. **`@name` / `#topic` prefix** at the very start of a message delegates
   to a child group or runs a topic-scoped session.

Full reference (operator side): repo `ROUTING.md`. Spec:
`specs/5/B-route-mode-ingestion.md`.
