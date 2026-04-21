# 063 — compact-memories: recognize XML-wrapped messages

`compact-memories` was classifying inbound telegram messages as
automation and producing "no user activity" summaries even on busy
days, because events typed `role:"user"` in the session JSONL are a
mix of:

- real inbound messages (wrapped as `<messages><message ...>body</message></messages>` by the gateway)
- tool-result turns (Bash stdout, Read output, etc.)

The old heuristic counted only plain-text user events and threw away
the XML-wrapped ones along with the tool results.

Fix: treat any event whose text contains a `<message ` or `<messages>`
tag as a real user message. When the DB cross-check shows inbound
messages for the target date but the transcript parser found zero,
trust the DB — the parser is wrong, not the day.
