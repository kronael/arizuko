#!/usr/bin/env python3
import json
import re
import sys
from pathlib import Path
from xml.sax.saxutils import quoteattr, unescape

USER_TAG = re.compile(r'^<user id="([^"/\n]+)" />$', re.MULTILINE)


def read(path):
    try:
        return path.read_text(errors='replace').strip()
    except (OSError, ValueError):
        return ''


def context(prompt, home):
    layers = []
    work = read(home / 'work.md')
    if work:
        layers.append(f'<knowledge layer="work">\n{work}\n</knowledge>')
    tag = USER_TAG.search(prompt)
    if tag:
        sender = unescape(tag.group(1), {'&quot;': '"'})
        body = read(home / 'users' / f'{sender}.md')
        if body:
            path = quoteattr(f'~/users/{sender}.md')
            layers.append(f'<user-memory id={quoteattr(sender)} path={path}>\n{body}\n</user-memory>')
    return '\n'.join(layers)


def main():
    try:
        data = json.load(sys.stdin)
    except ValueError:
        return
    prompt = data.get('prompt') if isinstance(data, dict) else None
    text = context(prompt, Path.home()) if isinstance(prompt, str) else ''
    if text:
        print(json.dumps({'hookSpecificOutput': {'hookEventName': 'UserPromptSubmit', 'additionalContext': text}}))


if __name__ == '__main__':
    main()
