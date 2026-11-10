---
name: macrohype
description: >
  Daily price and attention phases for a watchlist: one line when an asset
  enters move, chatter or hype, silence otherwise. USE on the macrohype cron,
  "/macrohype check", "/macrohype status", "what is the hype on X", "is X
  hot". NOT for intraday prices, news or a trade recommendation.
user-invocable: true
arg: "check | status"
---

# macrohype

The script judges each closed UTC day of each asset in `~/macrohype.toml`:

```sh
python3 ~/.claude/skills/macrohype/scripts/macrohype.py check
python3 ~/.claude/skills/macrohype/scripts/macrohype.py status
```

Two daily series feed it: the Bybit daily close and the English Wikipedia page
views of the asset's article. Each series gets a median-based z-score against its own
last `window` days. Price is hot when its absolute z-score reaches `alert_z`;
views are hot when their z-score reaches `alert_z`. A hot series turns calm
below `calm_z`.

| price | views | phase |
| ----- | ----- | ------- |
| calm | calm | quiet |
| hot | calm | move |
| calm | hot | chatter |
| hot | hot | hype |

## check — the scheduled run

1. Run `check` once.
2. When it prints lines, post them unchanged as ONE message to the chat of the
   task. Add no comment, no greeting and no analysis.
3. When it prints nothing and stderr is empty, post nothing: emit no status
   and call no delivery tool. A non-zero exit code alone changes nothing.
4. When it prints nothing and stderr holds a Python traceback, post the last
   line of the traceback as ONE message.

`check` prints a line only for a change: an asset that enters `move`, `chatter`
or `hype`, a failure that is new, and `macrohype: recovered` after a failure.
A return to `quiet`, an unchanged phase and a repeated failure print nothing.
NEVER run `check` a second time to "confirm" an empty output.

## status — a question from a person

Run `status` and answer from its lines. It reads the days on disk and makes no
request. NEVER state a phase, a score or a number that `status` did not print.
When it fails, give its `Failed:` line and stop.

A phase describes one closed day. It is not a forecast and not a trade signal;
say so when a person asks what to do with it.

## Setup facts

- Config: `~/macrohype.toml` (`$MACROHYPE_CONFIG`). Copy
  `macrohype.example.toml` from this skill there once. Every key is required.
- State: `~/macrohype/` (`$MACROHYPE_HOME`): `data/<asset>.closes.json`,
  `data/<asset>.views.json`, `state.json`. A closed day on disk is never
  fetched again.
- Egress: `api.bybit.com` and `wikimedia.org`. A `403 Forbidden` tunnel error
  in a `Failed:` line is a missing egress rule; give the operator the host.
