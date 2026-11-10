#!/usr/bin/env python3
"""Step 4 gate: validate blocks.json against the one-message digest format.

usage: check_format.py <run-dir>      prints OK or the list of failures (exit 1).
Format: blocks.json holds ONE block, slot "digest". Its lines, in order:
  1  **<window>: <3-word theme>**
  2  numbers joined by ·
  then  ✓ finished  + started  (optional, 0 to MAX_PER_MARKER lines each)
        ~ changed  ? open question  (set in stone: 1 to MAX_PER_MARKER each)
  then  one block per human in members.json: `<@id> <priority>` (that
        person's most important item) and 0 to 3 lines `· <claim>`
        (MAX_PER_PERSON lines per person)
Every content line is a full claim (>= MIN_CLAIM_WORDS words, tag and symbols
not counted) and <= MAX_LINE_WORDS words, in Simplified Technical English: a
topic tag such as "detection risk" is a defect. Length is cut by dropping
lines, never by cutting a claim to a tag: MAX_LINES lines, MAX_CHARS chars. Every #<n> exists in the gather files and is written as a link with its scope, `[#n <scope>](<url>)`
(scope from pr_scope.json; `<>` stops the Discord embed). No agent speaker.
No '#' header. critique.md present.
"""
import json
import os
import re
import sys

import config

MIN_CLAIM_WORDS = 6
MAX_LINE_WORDS = 13
MAX_PER_MARKER = 2
MAX_PER_PERSON = 4
MAX_CHARS = 1900
THEME_WORDS = 3
TAG_RE = re.compile(r"<@(\d+)>")
MARKERS = ("✓", "+", "~", "?")
WORD_ONLY_RE = re.compile(r"[•✓+~?★·—–\->→:]+")
PR_RE = re.compile(r"#(\d{2,5})\b")
PR_LINK_RE = re.compile(r"\[#(\d{2,5}) ([^\]\s]+)\]\(<(https://github\.com/[^>\s]+/pull/(\d+))>\)")
AGENT = re.escape(config.load()["agent_name"])
AGENT_SPEAKER_RE = re.compile(
    rf"(?i)\b({AGENT}|the bot|the agent|agent's)\b\s*[:@(]"
    rf"|\b{AGENT} (said|says|claims|proposed|recommends)"
)


def load_json(path, default=None):
    if not os.path.exists(path):
        return default
    with open(path) as f:
        return json.load(f)


def collect_known_prs(run):
    prs = set()
    for name in ("gh_merged.json", "gh_open.json", "gh_carried.json"):
        rows = load_json(os.path.join(run, name), default=[])
        prs |= {str(row.get("number")) for row in rows or []}
    commits_path = os.path.join(run, "commits.txt")
    if os.path.exists(commits_path):
        with open(commits_path) as f:
            prs |= set(re.findall(r"\(#(\d+)\)", f.read()))
    return prs


def count_words(line):
    stripped = TAG_RE.sub(" ", re.sub(r"[*]", " ", line))
    return len([w for w in stripped.split() if not WORD_ONLY_RE.fullmatch(w)])


def check_lines(lines, members, text):
    fails = []
    title = re.match(r"^\*\*[^:*]+:\s*(.+?)\*\*$", lines[0].strip()) if lines else None
    if not title or len(title.group(1).split()) != THEME_WORDS:
        fails.append(f"line 1 must be **<window>: <3-word theme>**: {lines[0][:60] if lines else ''}")
    if len(lines) > 1 and "·" not in lines[1]:
        fails.append("line 2 must be the numbers line, items joined by ·")
    body = [ln.strip() for ln in lines[2:]]
    star = []
    if any(ln.startswith("★ ") for ln in body):
        fails.append("no ★ line: priorities are one per person, the first line of each block")
    marker_lines = []
    while body and body[0][:1] in MARKERS and body[0][1:2] == " ":
        marker_lines.append(body.pop(0))
    marks = [ln[0] for ln in marker_lines]
    for m in ("~", "?"):
        if m not in marks:
            fails.append(f"a '{m} ' line is required: ~ = what changed vs last period and why, ? = the open question that blocks work or money")
    order = [m for m in MARKERS for _ in range(marks.count(m))]
    if marks != order:
        fails.append(f"✓ + ~ ? lines must keep that order (got {''.join(marks)})")
    for m in MARKERS:
        if marks.count(m) > MAX_PER_MARKER:
            fails.append(f"more than {MAX_PER_MARKER} '{m}' lines; drop the least important")
    blocks, cur = [], None
    for ln in body:
        tag = TAG_RE.match(ln)
        if tag:
            cur = [tag.group(1), ln]
            blocks.append(cur)
        elif ln.startswith("· ") and cur:
            cur.append(ln)
        else:
            fails.append(f"person line must start with <@id> or '· ': {ln[:60]}")
    tagged = {b[0] for b in blocks}
    for name, m in members.items():
        uid = m.get("sender_id", "").rsplit("/", 1)[-1]
        if uid not in tagged:
            fails.append(f"missing person block '<@{uid}> …' ({name})")
    for b in blocks:
        if len(b) - 1 > MAX_PER_PERSON:
            fails.append(f"<@{b[0]}> has {len(b) - 1} lines > {MAX_PER_PERSON}")
    for ln in star + marker_lines + [ln for b in blocks for ln in b[1:]]:
        n = count_words(ln)
        if n < MIN_CLAIM_WORDS:
            fails.append(f"not a claim, a tag ({n} words): {ln[:60]}")
        if n > MAX_LINE_WORDS:
            fails.append(f"line has {n} words > {MAX_LINE_WORDS}; say it shorter, keep the claim: {ln[:60]}")
    max_lines = 2 + MAX_PER_MARKER * len(MARKERS) + MAX_PER_PERSON * max(len(members), 1)
    if len(lines) > max_lines:
        fails.append(f"{len(lines)} lines > {max_lines}; drop whole lines, never words")
    if len(text) > MAX_CHARS:
        fails.append(f"{len(text)} chars > {MAX_CHARS} (Discord); drop whole lines")
    return fails


def check_text(text, prs, scopes):
    fails = []
    if AGENT_SPEAKER_RE.search(text):
        fails.append("agent as a speaker - the agent is not a participant")
    if re.search(r"^#", text, re.MULTILINE):
        fails.append("'#' header")
    rest = text
    for m in PR_LINK_RE.finditer(text):
        n, scope, url, url_n = m.groups()
        if n != url_n:
            fails.append(f"PR link #{n} points at pull/{url_n}")
        if n not in prs:
            fails.append(f"PR #{n} not in gather files")
        want = scopes.get(n, {})
        if want and scope != want.get("scope"):
            fails.append(f"PR #{n} scope is '{scope}', pr_scope.json says '{want.get('scope')}'")
        if want and url != want.get("url"):
            fails.append(f"PR #{n} url differs from pr_scope.json")
        if not want:
            fails.append(f"PR #{n} missing from pr_scope.json (rerun gather_github.sh)")
        rest = rest.replace(m.group(0), " ")
    for n in PR_RE.findall(rest):
        fails.append(f"PR #{n} must be a link with its scope: [#{n} <scope>](<https://github.com/<repo>/pull/{n}>)")
    return fails


def main():
    run = sys.argv[1]
    blocks = load_json(os.path.join(run, "blocks.json"), default=[])
    members = load_json(os.path.join(run, "members.json"), default={})
    fails = []
    if len(blocks) != 1 or blocks[0].get("slot") != "digest":
        fails.append('blocks.json must hold exactly one block with slot "digest"')
    text = blocks[0]["text"] if blocks else ""
    lines = [line for line in text.strip().splitlines() if line.strip()]
    fails += check_lines(lines, members, text)
    scopes = load_json(os.path.join(run, "pr_scope.json"), default={}) or {}
    fails += check_text(text, collect_known_prs(run), scopes)
    if not os.path.exists(os.path.join(run, "critique.md")):
        fails.append("critique.md missing — run the blind review (step 3)")
    if fails:
        print("\n".join("FAIL " + f for f in fails))
        sys.exit(1)
    print(f"OK lines={len(lines)} chars={len(text)}")


if __name__ == "__main__":
    main()
