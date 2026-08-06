# 188 — the audit trail is readable, and you can read your own

Every daemon that records an audit trail now publishes it. Before this, only
routd's rows could be read; runed's and authd's existed and were correct but
needed a shell on the machine to see.

## What changed for you

You have a new tool, **`query_audit`**. It answers "what did I do last turn"
and "what happened in my folder" without you keeping your own journal.

```
query_audit(category="mutation", limit=20)
query_audit(actor="google:114alice")
query_audit(folder="atlas/support/launch")
```

It returns one row per state-changing call plus every denial and error, newest
first: who acted, on what, from which surface, and how it went. Arguments come
back in `params_summary` with anything credential-shaped already replaced by a
marker like `<redacted:64chars>`.

Filters: `folder`, `category` (`authn`, `authz`, `access`, `mutation`,
`system`, `network`, `channel`, `agent`, `secret`, `scheduler`), `actor`
(substring), `limit` (default 50, max 200), `before_id` to page backwards.

## What it will not do

**It is scoped to your folder and everything under it.** Asking for another
folder does not widen it — you get your own rows regardless of what you pass.
That is deliberate: the tool is your memory, not a window onto the instance.

**It is read-only, and there is nothing to add.** The log is append-only and
each row is written inside the transaction of the act it records. There is no
way to create, edit or delete a row, and that is the property that makes the
record worth anything.

**Your own reads do not appear in it.** Only changes get a row, so calling
`query_audit` repeatedly does not pollute what you are reading. A call that
gets *refused* is recorded.

**You may not have it.** The tool is default-deny behind an `mcp:query_audit`
grant at your folder. If it is not in your tool list, your operator has not
granted it — ask rather than working around it.

## For operators

`/dash/audit/` now shows routd's, runed's and authd's trails in one table with
a source column, so "who killed that run" is answerable from the dashboard.
Each daemon serves `GET /v1/audit`; the dashboard asks them over HTTP rather
than opening their database files.

Spec: `specs/5/I-tool-call-logging.md`.
