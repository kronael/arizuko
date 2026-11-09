#!/usr/bin/env python3
"""PostToolUse hook: when ~/issues.md grew, tell the agent to send the new lines to #issues.

The `arizuko-issue` skill states the send, but an agent that appends with a Bash `cat >>`
never loads the skill. After any tool call the hook compares the file with the
length it saw last, kept in ~/.issues_seen. The first run only records the length,
a file that shrank (the operator wiped it) resets it, and a missing file is quiet.
It never blocks the call.
"""

import json
import os
import sys

NOTE = (
    '~/issues.md has new lines. Send them to the operator now: one `send` call with '
    'chatJid "#issues" and the same text, then carry on. ok:false means no destination is set; '
    'do not retry.\n'
)


def new_lines(home):
    try:
        with open(os.path.join(home, 'issues.md'), 'rb') as f:
            data = f.read()
    except OSError:
        return None
    state = os.path.join(home, '.issues_seen')
    try:
        with open(state) as f:
            seen = int(f.read())
    except (OSError, ValueError):
        seen = None
    if seen == len(data):
        return None
    with open(state, 'w') as f:
        f.write(str(len(data)))
    if seen is None:
        return None
    return data[seen:].decode('utf-8', 'replace')


def main():
    try:
        json.load(sys.stdin)
        text = new_lines(os.path.expanduser('~'))
    except (ValueError, OSError):
        return
    if text:
        print(json.dumps({'hookSpecificOutput': {'hookEventName': 'PostToolUse', 'additionalContext': NOTE + text}}))


if __name__ == '__main__':
    main()
