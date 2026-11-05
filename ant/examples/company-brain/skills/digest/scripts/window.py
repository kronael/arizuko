#!/usr/bin/env python3
"""Step 0: compute the window and the run dir.

Writes <run>/window.txt (KEY=VALUE, sourceable).
usage: window.py [--week] [--state FILE] [--end RFC3339] [--out DIR]
START = max(END - 24h|7d, state.last_end) so a dead run is covered by
the next one (MISSED=1).
PREV = run dir of the last posted run (state.posted_run) or the newest
earlier run with combined.md; empty means use the previous posted
blocks (state slots) as the baseline.
"""
import datetime as dt
import json
import os
import sys

HOME = os.environ.get("HOME", "/home/node")
ROOT = os.environ.get("DIGEST_ROOT", os.path.join(HOME, "tmp", "digest"))
ISO_FORMAT = "%Y-%m-%dT%H:%M:%SZ"


def get_opt(args, name):
    return args[args.index(name) + 1] if name in args else None


def to_iso(moment):
    return moment.strftime(ISO_FORMAT)


def load_state(state_path):
    """Return (prev_run_dir, last_end). A read or parse problem raises."""
    prev = ""
    with open(state_path) as f:
        state = json.load(f)
    posted_run = state.get("posted_run")
    if posted_run and os.path.isdir(os.path.join(ROOT, posted_run)):
        prev = os.path.join(ROOT, posted_run)
    return prev, state.get("last_end")


def main():
    args = sys.argv[1:]
    week = "--week" in args
    end_opt = get_opt(args, "--end")
    end = (
        dt.datetime.fromisoformat(end_opt.replace("Z", "+00:00"))
        if end_opt
        else dt.datetime.now(dt.UTC).replace(microsecond=0)
    )
    span = dt.timedelta(days=7) if week else dt.timedelta(hours=24)
    default_state_name = (
        ".weekly-digest-state.json" if week else ".digest-state.json"
    )
    state_path = get_opt(args, "--state") or os.path.join(
        HOME, default_state_name
    )
    run_id = end.strftime("%G-W%V") if week else end.strftime("%Y%m%d")
    start = end - span
    missed = 0
    prev = ""
    if os.path.exists(state_path):
        prev, last_end = load_state(state_path)
        if last_end and last_end < to_iso(start):
            start = dt.datetime.fromisoformat(last_end.replace("Z", "+00:00"))
            missed = 1
    label = f"{start.strftime('%d %b')}–{end.strftime('%d %b')}"
    out = get_opt(args, "--out") or os.path.join(ROOT, run_id)
    os.makedirs(os.path.join(out, "threads"), exist_ok=True)
    if not prev:  # newest earlier run of the same mode that reached combined.md
        mode_runs = sorted(
            entry
            for entry in os.listdir(ROOT)
            if ("-W" in entry) == week
            and entry < run_id
            and os.path.exists(os.path.join(ROOT, entry, "combined.md"))
        )
        prev = os.path.join(ROOT, mode_runs[-1]) if mode_runs else ""
    txt = (
        f"MODE={'week' if week else 'day'}\n"
        f"RUN_ID={run_id}\n"
        f"START={to_iso(start)}\n"
        f"END={to_iso(end)}\n"
        f"LABEL='{label}'\n"
        f"STATE={state_path}\n"
        f"OUT={out}\n"
        f"MISSED={missed}\n"
        f"PREV={prev}\n"
        f"HISTORY={os.path.join(HOME, '.digest-history')}\n"
    )
    with open(os.path.join(out, "window.txt"), "w") as f:
        f.write(txt)
    print(txt, end="")


if __name__ == "__main__":
    main()
