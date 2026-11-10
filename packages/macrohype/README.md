# MacroHype package

A market attention monitor for a group: one line in the chat when an asset on the watchlist enters a new phase, silence otherwise.
The [macrohype skill](skills/macrohype/SKILL.md) holds the rules and one stdlib-only [script](skills/macrohype/scripts/macrohype.py).
The package ships no persona, so it adds to a group that already has one.
The [package guide](../../EXTENDING.md) explains package selection and installation.

## What it measures

Each asset has two daily series: the Bybit daily close and the page views of its English Wikipedia article.
Each closed UTC day gets a z-score against the `window` days before it, computed from their median and their median absolute deviation.
The price series is the daily log return; the views series is the logarithm of the count.
A series turns hot at `alert_z` and calm again below `calm_z`.

| price | views | phase   |
| ----- | ----- | ------- |
| calm  | calm  | quiet   |
| hot   | calm  | move    |
| calm  | hot   | chatter |
| hot   | hot   | hype    |

`check` prints a line when an asset enters `move`, `chatter` or `hype`, when a failure is new, and when a failure ends.
It prints nothing for a return to `quiet`, an unchanged phase or a repeated failure.
A phase describes one closed day. It is not a forecast.

## Install

```sh
arizuko package add -g FOLDER catalog:macrohype
cp packages/macrohype/skills/macrohype/macrohype.example.toml GROUPS_DIR/FOLDER/macrohype.toml
arizuko network allow FOLDER api.bybit.com
arizuko network allow FOLDER wikimedia.org
```

Use `-i/--instance NAME` to select another configured instance.
Edit `macrohype.toml`: set `user_agent` to a client name and your contact address, as Wikimedia asks of automated clients, then the watchlist and the thresholds. Every key is required.
The guest needs `python3` 3.11 or newer. The script has no dependency outside the standard library.

Schedule one run a day, after Wikimedia has published the previous day. Put the row in a file and apply it:

```yaml
scheduled_tasks:
  - id: macrohype-check
    owner: FOLDER
    chat_jid: CHAT_JID
    prompt: /macrohype check
    cron: 0 12 * * *
    status: active
    context_mode: isolated
```

```sh
arizuko apply task.yml
```

The series are daily, so a second run on the same day fetches nothing and prints nothing.
A run that comes late judges every day it missed, in order.

## State

`~/macrohype/` in the group holds `data/<asset>.closes.json`, `data/<asset>.views.json` and `state.json`; `MACROHYPE_HOME` selects another directory and `MACROHYPE_CONFIG` another config path.
A closed day on disk is never fetched again. The first run fetches `window` + 5 days per series.
A source whose newest day is more than three days behind the last closed day fails the run.

## Verification

Run the local tests from the repository root:

```sh
python3 -B -X dev -W error -m unittest discover -s packages/macrohype/tests
```

The tests replace both sources with in-memory series and send no request. They cover each phase, the silent runs, the one-day fetch, a late run, late page views, and the failure and recovery lines.
[eval/macrohype.toml](eval/macrohype.toml) holds the agent cases: an empty run stays silent, a printed line is posted unchanged, and no phase is invented without a config.
