#!/usr/bin/env python3
import argparse
import enum
import json
import os
import statistics
import sys
from dataclasses import dataclass
from dataclasses import field
from datetime import UTC
from datetime import datetime
from datetime import timedelta
from pathlib import Path

NUDGE = 'This turn has no checklist.'
PROMPT = '<messages>\n<'
STEERING = '<user-steering>'
LONG_CALLS = 20
LONG_SECONDS = 120
REMINDER_CALLS = 8
REMINDER_SECONDS = 180
RUN_CEILING = timedelta(minutes=40)
CLAUDE_CHECKLIST = {'TodoWrite', 'TaskCreate', 'TaskUpdate'}


class Harness(enum.Enum):
    CLAUDE = 'claude'
    CODEX = 'codex'
    PI = 'pi'


@dataclass
class Turn:
    harness: Harness
    folder: str
    key: str
    started: datetime
    ended: datetime
    is_closed: bool = False
    calls: set[str] = field(default_factory=set)
    failed: set[str] = field(default_factory=set)
    checklist: dict[str, datetime] = field(default_factory=dict)
    items: int = 0
    done: set[str] = field(default_factory=set)
    is_progress_skill: bool = False
    is_reminded: bool = False

    def seconds(self) -> float:
        return (self.ended - self.started).total_seconds()

    def checklist_calls(self) -> list[datetime]:
        return sorted(at for call, at in self.checklist.items() if call not in self.failed)

    def is_long(self) -> bool:
        return len(self.calls) >= LONG_CALLS and self.seconds() >= LONG_SECONDS

    def is_under_reminder(self) -> bool:
        return len(self.calls) < REMINDER_CALLS and self.seconds() < REMINDER_SECONDS


@dataclass
class Sample:
    turns: list[Turn] = field(default_factory=list)
    files: int = 0
    child_files: int = 0
    child_turns: int = 0
    malformed: int = 0
    duplicates: int = 0
    active: int = 0
    outside: int = 0


def parse_time(value: str) -> datetime:
    return datetime.fromisoformat(value)


def read_records(path: Path, sample: Sample) -> list[dict]:
    records = []
    with open(path, errors='replace') as lines:
        for line in lines:
            try:
                record = json.loads(line)
            except ValueError:
                sample.malformed += 1
                continue
            if isinstance(record, dict):
                records.append(record)
            else:
                sample.malformed += 1
    return records


def as_dict(value: object) -> dict:
    if isinstance(value, dict):
        return value
    return {}


def text_of(content: object) -> str:
    if isinstance(content, str):
        return content
    if not isinstance(content, list):
        return ''
    return '\n'.join(part.get('text', '') for part in content if isinstance(part, dict) and isinstance(part.get('text'), str))


def note_todos(turn: Turn, todos: object) -> None:
    if not isinstance(todos, list):
        return
    turn.items = len(todos)
    turn.done = {str(index) for index, todo in enumerate(todos) if isinstance(todo, dict) and todo.get('status') == 'completed'}


def read_claude(path: Path, folder: str, sample: Sample) -> list[Turn]:
    turns: list[Turn] = []
    for record in read_records(path, sample):
        if record.get('isSidechain') or 'timestamp' not in record:
            continue
        at = parse_time(record['timestamp'])
        kind = record.get('type')
        message = as_dict(record.get('message'))
        content = message.get('content')
        parts = [part for part in content if isinstance(part, dict)] if isinstance(content, list) else []
        text = text_of(content)
        is_result = any(part.get('type') == 'tool_result' for part in parts)
        if kind == 'user' and not record.get('isMeta') and not is_result and PROMPT in text and not text.startswith('<task-notification>'):
            if turns:
                turns[-1].is_closed = True
            turns.append(Turn(Harness.CLAUDE, folder, str(record.get('promptId') or record.get('uuid')), at, at))
            continue
        if not turns:
            continue
        turn = turns[-1]
        turn.ended = max(turn.ended, at)
        if kind == 'user':
            for part in parts:
                if part.get('type') == 'tool_result' and part.get('is_error'):
                    turn.failed.add(str(part.get('tool_use_id')))
            if text.startswith(STEERING) and NUDGE in text:
                turn.is_reminded = True
        elif kind == 'assistant':
            for part in parts:
                if part.get('type') != 'tool_use':
                    continue
                call = str(part.get('id'))
                name = part.get('name')
                arguments = as_dict(part.get('input'))
                turn.calls.add(call)
                if name in CLAUDE_CHECKLIST:
                    turn.checklist[call] = at
                if name == 'TodoWrite':
                    note_todos(turn, arguments.get('todos'))
                elif name == 'TaskCreate':
                    turn.items += 1
                elif name == 'TaskUpdate' and arguments.get('status') == 'completed':
                    turn.done.add(str(arguments.get('taskId')))
                elif name == 'Skill' and arguments.get('skill') == 'progress':
                    turn.is_progress_skill = True
        elif kind == 'attachment':
            attachment = as_dict(record.get('attachment'))
            if attachment.get('type') == 'hook_additional_context' and NUDGE in json.dumps(attachment.get('content')):
                turn.is_reminded = True
        elif kind == 'queue-operation' and NUDGE in str(record.get('content')):
            turn.is_reminded = True
    return turns


def read_codex(path: Path, folder: str, sample: Sample) -> list[Turn]:
    turns: list[Turn] = []
    prompted: set[str] = set()
    for record in read_records(path, sample):
        payload = as_dict(record.get('payload'))
        kind = payload.get('type')
        if record.get('type') == 'session_meta' and (payload.get('thread_source') == 'subagent' or isinstance(payload.get('source'), dict)):
            sample.child_files += 1
            return []
        if 'timestamp' not in record:
            continue
        at = parse_time(record['timestamp'])
        if record.get('type') == 'event_msg' and kind == 'task_started':
            turns.append(Turn(Harness.CODEX, folder, str(payload.get('turn_id')), at, at))
            continue
        if not turns:
            continue
        turn = turns[-1]
        turn.ended = max(turn.ended, at)
        if record.get('type') == 'event_msg' and kind == 'task_complete':
            turn.is_closed = True
        elif kind in ('function_call', 'custom_tool_call', 'local_shell_call'):
            call = str(payload.get('call_id'))
            body = str(payload.get('input') or payload.get('arguments') or '')
            turn.calls.add(call)
            if payload.get('name') == 'update_plan':
                turn.checklist[call] = at
                try:
                    plan = json.loads(body).get('plan')
                except ValueError:
                    plan = None
                note_todos(turn, plan)
            elif 'tools.update_plan(' in body:
                turn.checklist[call] = at
            if 'skills/progress' in body:
                turn.is_progress_skill = True
        elif kind == 'message' and payload.get('role') in ('developer', 'user'):
            text = text_of(payload.get('content'))
            if text.startswith(STEERING) and NUDGE in text:
                turn.is_reminded = True
            if PROMPT in text:
                prompted.add(turn.key)
    sample.child_turns += sum(1 for turn in turns if turn.key not in prompted)
    return [turn for turn in turns if turn.key in prompted]


def read_pi(path: Path, folder: str, sample: Sample) -> list[Turn]:
    turns: list[Turn] = []
    for record in read_records(path, sample):
        message = as_dict(record.get('message'))
        if record.get('type') != 'message' or 'timestamp' not in record:
            continue
        at = parse_time(record['timestamp'])
        content = message.get('content')
        text = text_of(content)
        if message.get('role') == 'user' and PROMPT in text:
            if turns:
                turns[-1].is_closed = True
            turns.append(Turn(Harness.PI, folder, str(record.get('id')), at, at))
            continue
        if not turns:
            continue
        turn = turns[-1]
        turn.ended = max(turn.ended, at)
        if message.get('role') == 'assistant' and isinstance(content, list):
            for part in content:
                if isinstance(part, dict) and part.get('type') == 'toolCall':
                    turn.calls.add(str(part.get('id')))
        if message.get('role') == 'user' and NUDGE in text:
            turn.is_reminded = True
    return turns


READERS = (
    ('.claude/projects', Harness.CLAUDE, read_claude),
    ('.codex/sessions', Harness.CODEX, read_codex),
    ('.pi/sessions', Harness.PI, read_pi),
)


def collect(roots: list[Path], since: datetime, until: datetime, now: datetime) -> Sample:
    sample = Sample()
    best: dict[tuple[Harness, str], Turn] = {}
    for root in roots:
        for path in sorted(root.rglob('*.jsonl')):
            relative = path.relative_to(root).as_posix()
            for marker, harness, read in READERS:
                if '/' + marker + '/' not in '/' + relative:
                    continue
                if '/subagents/' in relative:
                    sample.child_files += 1
                    break
                sample.files += 1
                folder = relative.split('/' + marker + '/')[0]
                turns = read(path, folder, sample)
                is_stale = now - datetime.fromtimestamp(os.path.getmtime(path), UTC) > RUN_CEILING
                if turns and is_stale and harness is not Harness.CODEX:
                    turns[-1].is_closed = True
                for turn in turns:
                    known = best.get((harness, turn.key))
                    if known:
                        sample.duplicates += 1
                    if not known or (turn.is_closed, len(turn.calls)) > (known.is_closed, len(known.calls)):
                        best[(harness, turn.key)] = turn
                break
    for turn in best.values():
        if not turn.is_closed:
            sample.active += 1
        elif not since <= turn.started < until:
            sample.outside += 1
        else:
            sample.turns.append(turn)
    sample.turns.sort(key=lambda turn: turn.started)
    return sample


def share(part: int, whole: int) -> str:
    if not whole:
        return '0 of 0'
    return f'{part} of {whole} ({100 * part // whole}%)'


def report(sample: Sample, since: datetime, until: datetime) -> str:
    turns = sample.turns
    long_turns = [turn for turn in turns if turn.is_long()]
    with_list = [turn for turn in turns if turn.checklist_calls()]
    lines = [
        f'window {since.isoformat()} to {until.isoformat()}',
        f'files read {sample.files}; child transcripts skipped {sample.child_files}; Codex turns without the turn prompt skipped {sample.child_turns}; malformed lines {sample.malformed}',
        f'copied turns dropped {sample.duplicates}; open turns excluded {sample.active}; turns outside the window {sample.outside}',
        f'closed parent turns {len(turns)}',
    ]
    for harness in Harness:
        mine = [turn for turn in turns if turn.harness is harness]
        mine_long = [turn for turn in mine if turn.is_long()]
        lines.append(
            f'{harness.value}: turns {len(mine)}; long {len(mine_long)}; '
            f'long with a checklist {share(sum(1 for turn in mine_long if turn.checklist_calls()), len(mine_long))}; '
            f'long reminded {share(sum(1 for turn in mine_long if turn.is_reminded), len(mine_long))}; '
            f'progress skill used {sum(1 for turn in mine if turn.is_progress_skill)}'
        )
    lines.append(f'checklist in any turn {share(len(with_list), len(turns))}')
    lines.append(f'checklist under the reminder thresholds (unnecessary) {sum(1 for turn in with_list if turn.is_under_reminder())}')
    lines.append(f'reminded turns {sum(1 for turn in turns if turn.is_reminded)}; reminded then a checklist {sum(1 for turn in with_list if turn.is_reminded)}')
    waits = [(turn.checklist_calls()[0] - turn.started).total_seconds() for turn in with_list]
    if waits:
        lines.append(f'seconds to the first checklist: median {statistics.median(waits):.0f}, min {min(waits):.0f}, max {max(waits):.0f}')
    sized = [turn for turn in with_list if turn.items]
    if sized:
        lines.append(
            f'checklist items at turn end: median {statistics.median(turn.items for turn in sized):.0f}; '
            f'one item {sum(1 for turn in sized if turn.items == 1)}; '
            f'every item completed {share(sum(1 for turn in sized if len(turn.done) >= turn.items), len(sized))}'
        )
    lines.append('long turns without a checklist:')
    for turn in long_turns:
        if not turn.checklist_calls():
            started = turn.started.isoformat(timespec='seconds')
            reminded = 'yes' if turn.is_reminded else 'no'
            lines.append(f'  {started} {turn.harness.value} {turn.folder} calls {len(turn.calls)} seconds {turn.seconds():.0f} reminded {reminded}')
    return '\n'.join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description='Report native checklist use in the closed parent turns of stored harness transcripts.')
    parser.add_argument('roots', nargs='+', type=Path, help='groups directory of an instance')
    parser.add_argument('-s', '--since', required=True, type=parse_time)
    parser.add_argument('-u', '--until', type=parse_time)
    arguments = parser.parse_args()
    now = datetime.now(UTC)
    since = arguments.since.replace(tzinfo=arguments.since.tzinfo or UTC)
    until = arguments.until.replace(tzinfo=arguments.until.tzinfo or UTC) if arguments.until else now
    print(report(collect(arguments.roots, since, until, now), since, until))
    return 0


if __name__ == '__main__':
    sys.exit(main())
