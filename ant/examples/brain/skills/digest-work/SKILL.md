---
name: digest-work
description: >
  Research half 1 of the digest: WHO WORKS ON WHAT. Reads GitHub (PRs,
  commits, releases of the `repo` in `~/digest.toml`) for one
  digest window and writes `<run>/work/work.md` — per person: what they
  built, finished, started, carried. Runs in parallel with
  digest-discourse; the digest skill combines both. USE when the digest
  orchestrator starts it, or "who works on what", "what got built this
  week". NOT for chat positions or conversation results (use
  digest-discourse), NOT for posting (use digest).
user-invocable: true
arg: <run-dir>
managed: local
---

# digest-work — who works on what

Input: a run dir with `window.txt` (digest step 0). Output:
`<run>/work/work.md` and `<run>/work/DONE`. Read ONLY GitHub and the repo
clone. NEVER read Discord here — the discourse half does that, and the two
meet only in the digest combine step. Scripts:
`$(dirname <this file>)/../digest/scripts`.

## Steps

1. `source <run>/window.txt; mkdir -p <run>/work`. If `<run>/work/DONE`
   exists, stop: this half is done (resume rule).
2. Gather: `<scripts>/gather_github.sh <run>`. Output in `<run>`:
   `gh_merged.json`, `gh_open.json`, `gh_carried.json`, `gh_releases.json`,
   `commits.txt`, `github.md` (has `## delta vs last period`),
   `gh_source.txt` (live `gh`, `git fetch`, a stale clone, or the message
   store). Copy `gh_source.txt` into work.md line 1.
3. Group by person: the GitHub login stays the login (do not guess a Discord
   name). `agent_github` in `~/digest.toml` is the agent account: list its PRs
   under `agent account (<login>)`, never as a person.
4. Group each person's PRs and commits by component — the first path or the
   conventional-commit scope: `feat(news-collector)` → news-collector,
   `fix(ansible)` → ansible, `docs(server)` → server.
5. For each person and component, classify every PR:
   - `done` — merged in window, opened before START (finished carry-over);
   - `built` — opened and merged in window;
   - `new` — opened in window, still open;
   - `carried` — open, opened before START (still in progress).
   Releases: per service, version range lowest → highest (`sort -V`).
6. Compare with `$PREV/work/work.md` when `PREV` is set, and with every digest
   in `$HISTORY/*.md`: which components are new for a person, which went
   quiet, which carried PRs closed, which item named in a past digest moved.
   Write these under `vs before`, each with the date it moved.
7. Write `<run>/work/work.md` in the schema below, then `touch <run>/work/DONE`.

## work.md schema

```
# work — <LABEL> — source: <gh_source.txt>
## <person>
focus: <component(s) with most PRs, ≤ 8 words>
done: #<n> <title ≤ 8 words> (merged YYYY-MM-DD); …   (opened before START)
built: <count> PRs — <component>: #<n> (MM-DD), #<n> (MM-DD); …
new: #<n> <title ≤ 8 words> (opened YYYY-MM-DD); …     (still open)
carried: #<n> <title> (since YYYY-MM-DD); …
vs before: <new component | quiet component | "no baseline">
## agent account (<agent_github>)
…same keys…
## releases
<service>: v<a> → v<b> (<count>)
## totals
merged <N> (done <a>, built <b>) · new open <c> · carried <d> · commits <e>
```

## Rules

- ALWAYS write the source from `gh_source.txt` in line 1; a fallback source
  (stale clone, message store) is not live, say so.
- ALWAYS cite a PR number and a date for every item; every number must be in
  the gather files (`mergedAt` / `createdAt`). Undated data cannot be ranked.
- ALWAYS keep counts from the files (`jq length`); NEVER count in your head.
- NEVER read Discord, diary or episodes in this skill.
- NEVER judge quality or intent; report what was built, by whom, in what state.
- NEVER post anything. The digest skill posts.
- Max 80 lines. A person with 0 PRs gets `quiet (0 PRs)`.
