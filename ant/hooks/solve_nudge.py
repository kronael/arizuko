#!/usr/bin/env python3
"""UserPromptSubmit hook: nudge /solve on a session's first prompt and after an idle gap.

"First" is a transcript with no user or assistant entry; "idle" is the newest
such entry older than IDLE_SECONDS. The current prompt is not in the transcript
yet when the hook runs. The hook keeps no state and never blocks the prompt.
prompt_context.py imports new_task, so both hooks fire on the same prompt.
"""

import json
import sys
import time
from datetime import datetime

IDLE_SECONDS = 1800
FIRST = (
    'New task (first prompt this session): run /solve before acting — it '
    'classifies the message, recalls context and matches skills. Skip only if '
    'this prompt directly continues work already in context.'
)
IDLE = (
    'New task? This prompt follows an idle gap: run /solve before acting '
    'unless it continues the work already in context.'
)


def last_entry(path):
    last = None
    try:
        with open(path) as f:
            for line in f:
                try:
                    entry = json.loads(line)
                except ValueError:
                    continue
                if entry.get('type') in ('user', 'assistant') and entry.get('timestamp'):
                    last = entry['timestamp']
    except OSError:
        return None
    if last is None:
        return None
    return datetime.fromisoformat(last.replace('Z', '+00:00')).timestamp()


def new_task(data, now):
    last = last_entry(data.get('transcript_path') or '')
    return last is None or now - last >= IDLE_SECONDS


def nudge(data, now):
    if '/solve' in (data.get('prompt') or ''):
        return None
    if last_entry(data.get('transcript_path') or '') is None:
        return FIRST
    if new_task(data, now):
        return IDLE
    return None


def main():
    try:
        data = json.load(sys.stdin)
    except ValueError:
        return
    text = nudge(data, time.time()) if isinstance(data, dict) else None
    if text:
        print(json.dumps({'hookSpecificOutput': {'hookEventName': 'UserPromptSubmit', 'additionalContext': text}}))


if __name__ == '__main__':
    main()
