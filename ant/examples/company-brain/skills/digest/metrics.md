# digest metrics — memory for this skill

The numbers line (digest step 2e) reads these. Append one row per posted run
(digest step 5). Keep definitions stable; when a definition changes, write
the date and the reason.

## Definitions

- `accounts` — accounts in use, ESTIMATE from Discord
  (`scripts/metrics_accounts.py`): distinct matches of `account_regex` named by a
  human in the 30 days before END, minus accounts whose latest mention says
  breach / odpojil / max loss / disconnect / closed. Replace with a real count when the team has a source.
- `prs` — PRs merged in the window (`jq length gh_merged.json`).
- `open` — PRs opened in the window and still open (`gh_open.json`).
- `msgs` — human Discord messages in the window (`members.json` sum).
- `threads` — threads with a human row in the window (`threads.json`).

## History

| run | accounts (named/neg) | prs | open | msgs | threads | note |
| --- | --- | --- | --- | --- | --- | --- |
