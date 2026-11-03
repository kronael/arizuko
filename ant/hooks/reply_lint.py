#!/usr/bin/env python3
"""PreToolUse hook: deny a chat reply over the Response Style ceiling in ant/CLAUDE.md.

Counts the outgoing `text` with code fences removed. Over the ceiling the call is
denied with the measured counts, so the agent rewrites it shorter; after DENIALS
denials in one turn the next try goes through, because a long reply is sometimes
right. The count is read from the turn's transcript; the hook keeps no state. A `send_file` caption is not counted: the file is where long
content goes. It keeps no state and always exits 0 (exit 2 would block the call).
"""

import json
import re
import sys

CEILING_CHARS = 500
CEILING_LINES = 6
DENIALS = 2
MARK = 'Reply too long:'
FENCE = re.compile(r'```.*?(```|\Z)', re.DOTALL)


def measure(text):
    prose = FENCE.sub('', text).strip()
    return len(prose), len([line for line in prose.splitlines() if line.strip()])


def denials(path):
    count = 0
    try:
        with open(path) as f:
            for line in f:
                try:
                    entry = json.loads(line)
                except ValueError:
                    continue
                content = (entry.get('message') or {}).get('content')
                if entry.get('type') == 'user' and isinstance(content, str):
                    count = 0
                elif MARK in line:
                    count += 1
    except (OSError, TypeError):
        return 0
    return count


def verdict(data):
    text = (data.get('tool_input') or {}).get('text')
    if not isinstance(text, str):
        return None
    chars, lines = measure(text)
    if chars <= CEILING_CHARS and lines <= CEILING_LINES:
        return None
    seen = denials(data.get('transcript_path') or '')
    if seen >= DENIALS:
        return None
    return (
        f'{MARK} {chars} chars / {lines} lines, ceiling {CEILING_CHARS} chars / '
        f'{CEILING_LINES} lines (code fences excluded). Rewrite it shorter, keeping every fact, '
        'link and number; or send the long part with send_file and keep a short reply. '
        f'Denial {seen + 1} of {DENIALS}: if the length is needed, the try after the last denial goes through.'
    )


def main():
    try:
        data = json.load(sys.stdin)
        reason = verdict(data) if isinstance(data, dict) else None
    except Exception:  # noqa: BLE001 - any failure must allow the call
        return
    if reason:
        print(json.dumps({'hookSpecificOutput': {
            'hookEventName': 'PreToolUse',
            'permissionDecision': 'deny',
            'permissionDecisionReason': reason,
        }}))


if __name__ == '__main__':
    main()
