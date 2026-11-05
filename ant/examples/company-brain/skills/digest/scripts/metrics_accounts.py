#!/usr/bin/env python3
"""Metric: accounts in use.

Estimate from Discord, until a real count exists.

usage: metrics_accounts.py <run-dir> [--days 30] [--since DATE]
reads  <run-dir>/window.txt (END)
writes <run-dir>/metrics_accounts.json
       {named, negative, trading, accounts:[...], definition}
See DEFINITION below for the accounts-trading definition. --since
caps how far back the chat history scan goes; it defaults to
END - 180 days.
Read-only (inspect_messages). Bot rows are ignored.
"""
import datetime as dt
import json
import os
import re
import sys

import config
from gather_discord import Mcp, is_human, load_channels, parse_rows, read_env

SINCE_DEFAULT_DAYS = 180
ACCOUNT_RE = re.compile(config.load()["account_regex"], re.I)
NEGATIVE_RE = re.compile(r"(?i)breach|odpoj|disconnect|max loss|zru[sš]|closed")
ISO_FORMAT = "%Y-%m-%dT%H:%M:%SZ"
MAX_PAGES = 80
DEFINITION = (
    "distinct accounts matching `account_regex`, named by a HUMAN in the last "
    "--days before END,\n"
    "minus accounts whose latest mention (≤ END) says breach / "
    "odpojil / max loss / disconnect / closed."
)


def get_opt(args, name):
    return args[args.index(name) + 1] if name in args else None


def parse_args(args):
    run = args[0]
    days_opt = get_opt(args, "--days")
    days = int(days_opt) if days_opt else 30
    return run, days, get_opt(args, "--since")


def record_hit(hits, acc, row, ctx):
    prev = hits.get(acc)
    if not prev or row["time"] > prev["last"]:
        hits[acc] = {
            "last": row["time"],
            "ctx": ctx,
            "neg": bool(NEGATIVE_RE.search(ctx)),
            "first": (prev or {}).get("first", row["time"]),
        }
    elif row["time"] < prev["first"]:
        prev["first"] = row["time"]


def scan_rows(hits, rows, end):
    for row in rows:
        if not is_human(row) or row["time"] > end:
            continue
        for match in ACCOUNT_RE.finditer(row["body"]):
            acc = match.group(0).lower()
            idx = match.start()
            ctx = row["body"][max(0, idx - 70) : idx + 70].replace("\n", " ")
            record_hit(hits, acc, row, ctx)


def scan_channel(mcp, jid, end, since, hits):
    """Page one channel backwards from end, updating hits in place."""
    before = end
    for _ in range(MAX_PAGES):
        res = mcp.call(
            "inspect_messages", chat_jid=jid, limit=100, before=before
        )
        rows = parse_rows(res.get("messages", ""))
        if not rows:
            break
        scan_rows(hits, rows, end)
        oldest = min(row["time"] for row in rows)
        if oldest == before or oldest < since:
            break
        before = oldest


def main():
    run, days, since_opt = parse_args(sys.argv[1:])
    end = read_env(os.path.join(run, "window.txt"))["END"]
    end_dt = dt.datetime.fromisoformat(end.replace("Z", "+00:00"))
    since = since_opt or (
        end_dt - dt.timedelta(days=SINCE_DEFAULT_DAYS)
    ).strftime("%Y-%m-%d")
    cut = (end_dt - dt.timedelta(days=days)).strftime(ISO_FORMAT)
    channels = [jid for jid, _name, _sig in load_channels()]
    mcp = Mcp()
    hits = {}
    try:
        for jid in channels:
            scan_channel(mcp, jid, end, since, hits)
    finally:
        mcp.close()
    recent = {acc: hit for acc, hit in hits.items() if hit["last"] >= cut}
    negative = sorted(acc for acc, hit in recent.items() if hit["neg"])
    trading = sorted(acc for acc in recent if acc not in negative)
    out = {
        "end": end,
        "days": days,
        "named_all": len(hits),
        "named": len(recent),
        "negative": negative,
        "trading": len(trading),
        "accounts": trading,
        "definition": DEFINITION,
    }
    with open(os.path.join(run, "metrics_accounts.json"), "w") as f:
        json.dump(out, f, indent=1, ensure_ascii=False)
    print(
        f"accounts trading ≈ {len(trading)} (named {len(recent)} in "
        f"{days}d, negative {len(negative)}: {', '.join(negative)}; "
        f"named since {since}: {len(hits)})"
    )


if __name__ == "__main__":
    main()
