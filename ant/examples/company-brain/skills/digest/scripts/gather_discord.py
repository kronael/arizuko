#!/usr/bin/env python3
"""Step 1a: gather Discord rows for the window and every active thread.

Gathers rows for the window and every thread active in it, from the
thread root.

usage: gather_discord.py <run-dir> [--max-pages N]
       [--thread-max-pages N] [--no-backfill]
reads  <run-dir>/window.txt
writes <run-dir>/main.xml            rows in window with no thread
           (all senders)
       <run-dir>/threads/<id>.xml    whole thread, root first; agent
           rows dropped unless a human applauded them
       <run-dir>/threads.json        thread index: span, rows,
           participants, root_found, root quote
       <run-dir>/members.json        per-human counts in window
       <run-dir>/channels.json       per-channel row counts
Read-only: inspect_messages, get_thread, fetch_history (platform
backfill into the local cache).
Resumable: a thread with <id>.ok is not fetched again.
"""
import html
import json
import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass

import config

HERE = os.path.dirname(os.path.abspath(__file__))
HOME = os.environ.get("HOME", "/home/node")
CFG = config.load()
BOT_USER = CFG["bot_user"]
AGENT_NAME = CFG["agent_name"]
HUMANS = CFG["humans"]  # sender_id -> display name
ROW_RE = re.compile(
    r"(?:<reply-to\s+id=\"([^\"]*)\"[^>]*?(?:/>|>.*?</reply-to>)\s*)?"
    r"<message\s+([^>]*?)>(.*?)</message>",
    re.S,
)
# A bot row stays only when a human applauds it: a positive reaction on
# it, or a reply to it that carries an approval word.
APPROVE_RE = re.compile(
    r"(?i)(\bok\b|\byes\b|\bagree|\bgood\b|\bgreat\b|\bnice\b|\bperfect"
    r"|\bexactly|\bcorrect|\bjj\b|\bano\b|\bsouhlas|\bpřesně"
    r"|\bpresne|\bsuper\b|\bdobr[yý]|\bdíky|\bdiky|\bparada"
    r"|\bparáda|\bskvel|\bskvěl|\U0001f44d|\U0001f525"
    r"|❤️|✅|\U0001f4af|\U0001f44f)"
)
NEG_REACT = ("\U0001f44e", "\U0001f621", "\U0001f92e", "\U0001f4a9")
REJECT_RE = re.compile(
    r"(?i)(\bagain\b|\bwrong|misunderst|\bnot\b|\bno\b|\bnope|\bdon'?t"
    r"|\bredo|\bdelete|\bfix\b|\bwtf|\bbullshit|\bshit|\bnesmysl"
    r"|\bspatn|\bšpatn|\bne\b|\bnene|\bproc\b|\bproč|\bwhy\b|\?)"
)
ATTR_RE = re.compile(r'(\w+)="([^"]*)"')


@dataclass(frozen=True, slots=True)
class Window:
    start: str
    end: str


@dataclass(frozen=True, slots=True)
class Thread:
    tid: str
    chan: str
    xml_path: str
    ok_path: str


def read_env(path):
    pairs = {}
    with open(path) as f:
        for raw_line in f:
            stripped = raw_line.strip()
            if "=" in stripped and not stripped.startswith("#"):
                key, value = stripped.split("=", 1)
                pairs[key] = value.strip("'")
    return pairs


class Mcp:
    def __init__(self):
        root = os.path.join(HOME, "tmp", "digest")
        os.makedirs(root, exist_ok=True)
        self.cfg = os.path.join(root, "arizuko.mcp.json")
        if not os.path.exists(self.cfg):
            sock = os.environ.get(
                "ARIZUKO_MCP_SOCKET", "/run/ipc/gated.sock"
            )
            cfg = {
                "mcpServers": {
                    "arizuko": {
                        "command": "socat",
                        "args": [f"UNIX-CONNECT:{sock}", "-"],
                    }
                }
            }
            with open(self.cfg, "w") as f:
                json.dump(cfg, f)
        self.sess = f"@dg{os.getpid()}"
        subprocess.run(
            ["mcpc", "connect", f"{self.cfg}:arizuko", self.sess],
            check=True,
            capture_output=True,
        )

    def close(self):
        subprocess.run(
            ["mcpc", self.sess, "close"], capture_output=True, check=False
        )

    def call(self, tool, **kw):
        args = [f"{k}:={json.dumps(v)}" for k, v in kw.items()]
        err = ""
        for attempt in range(3):
            proc = subprocess.run(
                ["mcpc", "--json", self.sess, "tools-call", tool, *args],
                capture_output=True,
                text=True,
                check=False,
            )
            if proc.returncode == 0 and proc.stdout.strip():
                try:
                    txt = json.loads(proc.stdout)["content"][0]["text"]
                    return json.loads(txt)
                except (KeyError, IndexError, ValueError) as exc:
                    err = f"parse: {exc}: {proc.stdout[:200]}"
            else:
                err = proc.stderr[:200] or proc.stdout[:200]
            time.sleep(1 + attempt)
        raise RuntimeError(f"mcp {tool} failed: {err}")


def parse_rows(xml):
    rows = []
    for m in ROW_RE.finditer(xml or ""):
        row = dict(ATTR_RE.findall(m.group(2)))
        row["reply_to"] = m.group(1) or ""
        row["body"] = html.unescape(m.group(3)).strip()
        row["raw"] = m.group(0)
        rows.append(row)
    return rows


def is_human(row):
    sid = row.get("sender_id", "")
    return (
        sid.startswith("discord:user/")
        and sid != BOT_USER
        and row.get("bot") != "true"
        and row.get("sender") != "timed"
    )


def is_agent(row):
    sid = row.get("sender_id", "")
    return (
        row.get("bot") == "true"
        or sid == BOT_USER
        or (
            not sid.startswith("discord:user/")
            and row.get("sender", "").startswith(AGENT_NAME)
        )
    )


def find_endorsements(rows):
    """Map agent-row key to a list of 'member @time: how' for applause."""
    keys = {}
    for row in rows:
        if is_agent(row):
            for key in (row.get("id"), row.get("platform_id")):
                if key:
                    keys[key] = row
    out = {}
    for row in rows:
        if not is_human(row) or row.get("reply_to") not in keys:
            continue
        target = keys[row["reply_to"]]
        target_key = target.get("platform_id") or target.get("id")
        who = HUMANS.get(row.get("sender_id"), row.get("sender", "?"))
        body = row.get("body", "")
        if row.get("verb") in ("like", "dislike"):
            if row.get("verb") == "like" and not any(
                neg in body for neg in NEG_REACT
            ):
                out.setdefault(target_key, []).append(
                    f"{who} @{row['time']}: reacted {body[:8]}"
                )
        elif APPROVE_RE.search(body[:200]) and not REJECT_RE.search(body[:200]):
            out.setdefault(target_key, []).append(
                f'{who} @{row["time"]}: replied "{body[:60]}"'
            )
    return out


def keep_rows(rows):
    """Drop agent rows unless applauded; mark kept ones endorsed_by=..."""
    endorsements = find_endorsements(rows)
    kept, dropped = [], 0
    for row in rows:
        if is_agent(row):
            target_key = row.get("platform_id") or row.get("id")
            if target_key not in endorsements:
                dropped += 1
                continue
            endorsed_by = "; ".join(endorsements[target_key])
            row["raw"] = row["raw"].replace(
                "<message ",
                f'<message endorsed_by="{html.escape(endorsed_by)}" ',
                1,
            )
        kept.append(row)
    return kept, dropped


def fetch_pages(mcp, tool, stop_before, max_pages, **kw):
    """Page a tool backwards with before=oldest until oldest < stop_before.

    Returns rows dict by id.
    """
    out, before, seen_oldest = {}, None, None
    for _ in range(max_pages):
        call_args = dict(kw, limit=100)
        if before:
            call_args["before"] = before
        res = mcp.call(tool, **call_args)
        rows = parse_rows(res.get("messages", ""))
        for row in rows:
            key = row.get("id") or row.get("platform_id") or (
                row["time"] + row.get("sender", "")
            )
            out.setdefault(key, row)
        oldest = res.get("oldest") or (
            min(row["time"] for row in rows) if rows else ""
        )
        if not rows or not oldest or oldest == seen_oldest:
            break
        seen_oldest = oldest
        if stop_before and oldest < stop_before:
            break
        before = oldest
    return out


def gather_channels(mcp, channels, window, max_pages, backfill):
    allrows, chan_stats = {}, {}
    for jid, name, sig in channels:
        if backfill and sig == "high":
            mcp.call("fetch_history", chat_jid=jid, limit=200)
        rows = fetch_pages(
            mcp, "inspect_messages", window.start, max_pages, chat_jid=jid
        )
        for row in rows.values():
            row["_chan"] = jid
            row["_chan_name"] = name
        allrows.update(rows)
        win = [
            row for row in rows.values()
            if window.start <= row["time"] <= window.end
        ]
        chan_stats[name] = {
            "jid": jid,
            "fetched": len(rows),
            "in_window": len(win),
            "human_in_window": sum(is_human(row) for row in win),
        }
    return allrows, chan_stats


def gather_thread(mcp, thread, allrows, tmax):
    trows = {
        key: row
        for key, row in allrows.items()
        if row.get("thread") == thread.tid or row.get("id") == thread.tid
    }
    if not os.path.exists(thread.ok_path):
        fetched = fetch_pages(
            mcp,
            "get_thread",
            None,
            tmax,
            chat_jid=thread.chan,
            topic=thread.tid,
        )
        for row in fetched.values():
            row["_chan"] = thread.chan
        trows.update(fetched)
    else:
        with open(thread.xml_path) as f:
            parsed = parse_rows(f.read())
        trows.update(
            {row.get("id", str(i)): row for i, row in enumerate(parsed)}
        )
    return sorted(trows.values(), key=lambda row: row["time"])


def build_thread_meta(thread, rows, dropped, window):
    root = next((row for row in rows if row.get("id") == thread.tid), None)
    humans = [row for row in rows if is_human(row)]
    inwin = [
        row for row in rows if window.start <= row["time"] <= window.end
    ]
    participants = sorted(
        {
            HUMANS.get(row.get("sender_id"), row.get("sender", "?"))
            for row in humans
        }
    )
    quote_source = (
        (root or (humans[0] if humans else rows[0])) if rows else None
    )
    last_human = None
    if humans:
        last = humans[-1]
        last_human = {
            "sender": HUMANS.get(last.get("sender_id"), last.get("sender")),
            "time": last["time"],
            "quote": last["body"][:160].replace("\n", " "),
        }
    quote = (
        quote_source["body"][:120].replace("\n", " ") if quote_source else ""
    )
    return {
        "id": thread.tid,
        "channel": thread.chan,
        "rows": len(rows),
        "human_rows": len(humans),
        "in_window_rows": len(inwin),
        "first": rows[0]["time"] if rows else "",
        "last": rows[-1]["time"] if rows else "",
        "participants": participants,
        "root_found": root is not None,
        "agent_rows_dropped": dropped,
        "root_quote": quote,
        "last_human": last_human,
        "file": thread.xml_path,
    }


def write_thread_xml(thread, rows, window):
    with open(thread.xml_path, "w") as f:
        f.write(
            f"<!-- thread {thread.tid} channel {thread.chan} rows {len(rows)} "
            f"span {rows[0]['time'] if rows else ''} -> "
            f"{rows[-1]['time'] if rows else ''} "
            f"window {window.start}..{window.end} -->\n"
            "<messages>\n"
        )
        for row in rows:
            f.write(row["raw"] + "\n")
        f.write("</messages>\n")


def load_channels():
    return config.channels(CFG)


def build_members(window_rows):
    members = {}
    for sid, name in HUMANS.items():
        members[name] = {
            "sender_id": sid,
            "rows": 0,
            "threads": [],
            "channels": {},
        }
    for row in window_rows:
        if not is_human(row):
            continue
        name = HUMANS.get(row["sender_id"], row.get("sender", row["sender_id"]))
        member = members.setdefault(
            name,
            {
                "sender_id": row["sender_id"],
                "rows": 0,
                "threads": [],
                "channels": {},
            },
        )
        member["rows"] += 1
        chan_name = row["_chan_name"]
        member["channels"][chan_name] = member["channels"].get(chan_name, 0) + 1
        if row.get("thread") and row["thread"] not in member["threads"]:
            member["threads"].append(row["thread"])
    return members


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        sys.exit(2)
    run = args[0]
    max_pages = (
        int(args[args.index("--max-pages") + 1])
        if "--max-pages" in args
        else 30
    )
    tmax = (
        int(args[args.index("--thread-max-pages") + 1])
        if "--thread-max-pages" in args
        else 20
    )
    backfill = "--no-backfill" not in args
    env = read_env(os.path.join(run, "window.txt"))
    window = Window(start=env["START"], end=env["END"])
    tdir = os.path.join(run, "threads")
    os.makedirs(tdir, exist_ok=True)
    channels = load_channels()
    mcp = Mcp()
    try:
        allrows, chan_stats = gather_channels(
            mcp, channels, window, max_pages, backfill
        )
        window_rows = [
            row for row in allrows.values()
            if window.start <= row["time"] <= window.end
        ]
        # threads with a human row in the window
        tids = {}
        for row in window_rows:
            if is_human(row) and row.get("thread"):
                tids.setdefault(row["thread"], row["_chan"])
        index = []
        for tid, chan in sorted(tids.items()):
            thread = Thread(
                tid=tid,
                chan=chan,
                xml_path=os.path.join(tdir, f"{tid}.xml"),
                ok_path=os.path.join(tdir, f"{tid}.ok"),
            )
            raw_rows = gather_thread(mcp, thread, allrows, tmax)
            rows, dropped = keep_rows(raw_rows)
            meta = build_thread_meta(thread, rows, dropped, window)
            index.append(meta)
            write_thread_xml(thread, rows, window)
            with open(thread.ok_path, "w") as f:
                f.write(window.end)
        with open(os.path.join(run, "main.xml"), "w") as f:
            f.write(
                f"<!-- untreaded rows {window.start}..{window.end} -->\n"
                "<messages>\n"
            )
            untreaded = sorted(
                (row for row in window_rows if not row.get("thread")),
                key=lambda row: row["time"],
            )
            mrows, mdrop = keep_rows(untreaded)
            f.write(f"<!-- agent rows dropped (not applauded): {mdrop} -->\n")
            for row in mrows:
                f.write(row["raw"] + "\n")
            f.write("</messages>\n")
        members = build_members(window_rows)
        with open(os.path.join(run, "threads.json"), "w") as f:
            json.dump(index, f, indent=1, ensure_ascii=False)
        with open(os.path.join(run, "members.json"), "w") as f:
            json.dump(members, f, indent=1, ensure_ascii=False)
        with open(os.path.join(run, "channels.json"), "w") as f:
            json.dump(chan_stats, f, indent=1)
        member_rows = {name: m["rows"] for name, m in members.items()}
        human_total = sum(is_human(row) for row in window_rows)
        print(
            f"window {window.start}..{window.end}: rows={len(window_rows)} "
            f"human={human_total} threads={len(index)} "
            f"members={member_rows}"
        )
        for t in index:
            print(
                f"  thread {t['id']} rows={t['rows']} "
                f"human={t['human_rows']} "
                f"{t['first'][:16]}->{t['last'][:16]} {t['participants']} "
                f"root={t['root_found']} | {t['root_quote'][:60]}"
            )
    finally:
        mcp.close()


if __name__ == "__main__":
    main()
