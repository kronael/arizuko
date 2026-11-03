#!/usr/bin/env python3
import json
import re
import sys
import time
from pathlib import Path
from xml.sax.saxutils import escape, quoteattr, unescape

sys.dont_write_bytecode = True
from solve_nudge import new_task  # noqa: E402

USER_TAG = re.compile(r'^<user id="([^"/\n]+)" />$', re.MULTILINE)
LEVELS = (('day', re.compile(r'^\d{8}$')), ('week', re.compile(r'^\d{4}-W\d{2}$')), ('month', re.compile(r'^\d{4}-\d{2}$')))
LENSES = ('episodes', 'diary')


def read(path):
    try:
        return path.read_text(errors='replace').strip()
    except (OSError, ValueError):
        return ''


def summary(text):
    if not text.startswith('---\n'):
        return ''
    end = text.find('\n---', 4)
    if end < 0:
        return ''
    lines = text[4:end].split('\n')
    for i, line in enumerate(lines):
        if not line.startswith('summary:'):
            continue
        value = line[len('summary:'):].strip()
        if value and value not in ('|', '>'):
            return value.strip('"\'')
        block = []
        for below in lines[i + 1:]:
            if below and not below[0].isspace():
                break
            if below.strip():
                block.append(below.strip())
        return '\n'.join(block)
    return ''


def newest(home, lens, pattern):
    try:
        names = [p.stem for p in (home / lens).iterdir() if p.suffix == '.md' and pattern.match(p.stem)]
    except OSError:
        return None
    return max(names) if names else None


def memory(home):
    entries = []
    for lens in LENSES:
        for level, pattern in LEVELS:
            period = newest(home, lens, pattern)
            if period is None:
                continue
            text = summary(read(home / lens / f'{period}.md'))
            if text:
                entries.append(f'<entry lens="{lens}" level="{level}" period="{period}">\n{escape(text)}\n</entry>')
    if not entries:
        return ''
    return '<memory scope="folder">\n' + '\n'.join(entries) + '\n</memory>'


def context(prompt, home, fresh):
    layers = []
    if fresh:
        layers.append(memory(home))
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
    return '\n'.join(layer for layer in layers if layer)


def main():
    try:
        data = json.load(sys.stdin)
    except ValueError:
        return
    prompt = data.get('prompt') if isinstance(data, dict) else None
    text = context(prompt, Path.home(), new_task(data, time.time())) if isinstance(prompt, str) else ''
    if text:
        print(json.dumps({'hookSpecificOutput': {'hookEventName': 'UserPromptSubmit', 'additionalContext': text}}))


if __name__ == '__main__':
    main()
