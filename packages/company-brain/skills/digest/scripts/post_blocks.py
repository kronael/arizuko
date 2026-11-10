#!/usr/bin/env python3
"""Step 7: post blocks.json to #main by editing stored slot ids in place.

usage: post_blocks.py <run-dir> [--dry-run | --apply | --preview <chat-jid>] [--redo]
--preview sends each block once to <chat-jid> (a review thread, never #main)
and leaves the state file untouched.
Plan: slot with stored id -> edit; slot without id -> reuse an orphaned
stored id (edit) before send; ids still left over -> delete. Writes
state: slots, last_end, threads, posted_run. Default is --dry-run.
Refuses --apply when state.posted_run == RUN_ID or the run dir name
unless --redo.
"""
import datetime
import json
import os
import subprocess
import sys

import config
from gather_discord import read_env

HOME = os.environ.get("HOME", "/home/node")
MINUTES = config.load()["post_jid"]  # the one chat the digest posts to
GONE_MARKERS = ("Unknown Message", "10008", "404")


def build_plan(blocks, slots):
    wanted = [b["slot"] for b in blocks]
    orphans = [k for k in slots if k not in wanted]  # state order
    plan, new_slots = [], {}
    for b in blocks:
        if b["slot"] in slots:
            plan.append(("edit", b["slot"], slots[b["slot"]], b["text"], ""))
            new_slots[b["slot"]] = slots[b["slot"]]
        elif orphans:
            old = orphans.pop(0)
            note = f"reused {old}"
            plan.append(("edit", b["slot"], slots[old], b["text"], note))
            new_slots[b["slot"]] = slots[old]
        else:
            plan.append(("send", b["slot"], None, b["text"], ""))
    for old in orphans:
        plan.append(("delete", old, slots[old], "", ""))
    return plan, new_slots


def print_plan(plan):
    for op, slot, msg_id, text, note in plan:
        first_line = text.splitlines()[0][:50] if text else ""
        print(f"{op:6} {slot:45} id={msg_id or '-':22} {note} | {first_line}")


def connect(session):
    cfg = os.path.join(HOME, "tmp", "digest", "arizuko.mcp.json")
    subprocess.run(
        ["mcpc", "connect", f"{cfg}:arizuko", session],
        check=True,
        capture_output=True,
    )


def call(session, tool, **kw):
    args = [f"{k}:={json.dumps(v)}" for k, v in kw.items()]
    proc = subprocess.run(
        ["mcpc", "--json", session, "tools-call", tool, *args],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr[:300] or proc.stdout[:300])
    envelope = json.loads(proc.stdout)
    text = (envelope.get("content") or [{}])[0].get("text", "")
    if envelope.get("isError"):
        raise RuntimeError(text[:300])
    try:
        return json.loads(text)  # send returns {"id": ...}
    except ValueError:
        return {"text": text}  # edit/delete return plain "ok"


def new_message_id(result):
    return str(result.get("id") or result.get("platform_id") or result)


def apply_plan(session, plan, new_slots):
    """Run the plan against Discord; continue past a failure, collecting it.

    Returns the list of "Failed: ..." messages for ops that did not land.
    """
    failures = []
    for op, slot, msg_id, text, _note in plan:
        if op == "edit":
            try:
                call(
                    session,
                    "edit",
                    chatJid=MINUTES,
                    targetId=msg_id,
                    content=text,
                )
            except Exception as exc:
                if not any(marker in str(exc) for marker in GONE_MARKERS):
                    failures.append(f"Failed: edit {slot} {msg_id}: {exc}")
                    continue
                # message gone on Discord -> send fresh
                result = call(session, "send", chatJid=MINUTES, text=text)
                new_slots[slot] = new_message_id(result)
                new_id = new_slots[slot]
                print(f"edit {slot} failed ({exc}); sent new id {new_id}")
        elif op == "send":
            result = call(session, "send", chatJid=MINUTES, text=text)
            new_slots[slot] = new_message_id(result)
        elif op == "delete":
            try:
                call(session, "delete", chatJid=MINUTES, targetId=msg_id)
            except Exception as exc:
                failures.append(f"Failed: delete {slot} {msg_id}: {exc}")
    return failures


def write_state(run, state_path, env, new_slots):
    state = {
        "slots": new_slots,
        "last_end": env["END"],
        "posted_run": os.path.basename(os.path.normpath(run)),
        "threads": {},
    }
    threads_path = os.path.join(run, "threads.json")
    if os.path.exists(threads_path):
        with open(threads_path) as f:
            threads = json.load(f)
        state["threads"] = {t["id"]: t["last"] for t in threads}
    with open(state_path, "w") as f:
        json.dump(state, f, indent=1)
    hist_dir = env.get("HISTORY") or os.path.join(os.environ.get("HOME", "/home/node"), ".digest-history")
    os.makedirs(hist_dir, exist_ok=True)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    with open(os.path.join(run, "blocks.json")) as f:
        posted = json.load(f)
    with open(os.path.join(hist_dir, f"{env['RUN_ID']}.md"), "w") as f:
        f.write(f"# digest {env['RUN_ID']} — {env.get('LABEL', '')} — window {env['START']} → {env['END']} — posted {stamp}\n\n")
        for b in posted:
            f.write(b["text"].rstrip() + "\n\n")


def main():
    run = sys.argv[1]
    apply = "--apply" in sys.argv[2:]
    redo = "--redo" in sys.argv[2:]
    env = read_env(os.path.join(run, "window.txt"))
    state_path = env["STATE"]
    if os.path.exists(state_path):
        with open(state_path) as f:
            state = json.load(f)
    else:
        state = {}
    slots = state.get("slots", {})
    with open(os.path.join(run, "blocks.json")) as f:
        blocks = json.load(f)
    already_posted = (env["RUN_ID"], os.path.basename(os.path.normpath(run)))
    if apply and state.get("posted_run") in already_posted and not redo:
        print(f"refuse: run {env['RUN_ID']} already posted; pass --redo")
        sys.exit(3)
    if "--preview" in sys.argv[2:]:
        target = sys.argv[sys.argv.index("--preview") + 1]
        if target == MINUTES:
            print("refuse: --preview never posts to #main; use --apply")
            sys.exit(2)
        session = f"@pv{os.getpid()}"
        connect(session)
        try:
            for block in blocks:
                result = call(session, "send", chatJid=target, text=block["text"])
                print(f"preview {block['slot']} -> {target} id={new_message_id(result)}")
        finally:
            subprocess.run(["mcpc", session, "close"], capture_output=True, check=False)
        sys.exit(0)
    plan, new_slots = build_plan(blocks, slots)
    print_plan(plan)
    if not apply:
        n_edit = sum(p[0] == "edit" for p in plan)
        n_send = sum(p[0] == "send" for p in plan)
        n_delete = sum(p[0] == "delete" for p in plan)
        print(
            f"dry-run: {n_edit} edit, {n_send} send, {n_delete} delete; "
            f"state {state_path} untouched"
        )
        sys.exit(0)

    session = f"@pb{os.getpid()}"
    connect(session)
    try:
        failures = apply_plan(session, plan, new_slots)
    finally:
        subprocess.run(
            ["mcpc", session, "close"], capture_output=True, check=False
        )
    write_state(run, state_path, env, new_slots)
    print(f"applied; state written {state_path}")
    if failures:
        for failure in failures:
            print(failure)
        sys.exit(1)


if __name__ == "__main__":
    main()
