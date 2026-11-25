import json
import os
import tempfile
import unittest
from collections.abc import Sequence
from datetime import UTC
from datetime import datetime
from datetime import timedelta
from pathlib import Path

import checklist_adoption
from checklist_adoption import Harness

START = datetime(2026, 11, 12, 10, 0, 0, tzinfo=UTC)
NOW = datetime(2026, 11, 20, tzinfo=UTC)


PROMPT = 'head\n<messages>\n<message id="1">hi</message>\n</messages>'
MANUAL = 'Inbound arrives wrapped:\n<messages>\n  <message id="...">body</message>\n</messages>'


def stamp(seconds: float) -> str:
    return (START + timedelta(seconds=seconds)).isoformat().replace('+00:00', 'Z')


def claude_prompt(prompt: str, seconds: float) -> dict:
    return {'type': 'user', 'promptId': prompt, 'timestamp': stamp(seconds), 'message': {'content': PROMPT}}


def claude_call(call: str, name: str, arguments: dict, seconds: float, is_sidechain: bool = False) -> dict:
    part = {'type': 'tool_use', 'id': call, 'name': name, 'input': arguments}
    return {'type': 'assistant', 'isSidechain': is_sidechain, 'timestamp': stamp(seconds), 'message': {'content': [part]}}


def claude_result(call: str, seconds: float, is_error: bool = False) -> dict:
    part = {'type': 'tool_result', 'tool_use_id': call, 'is_error': is_error}
    return {'type': 'user', 'timestamp': stamp(seconds), 'message': {'content': [part]}}


def claude_calls(prefix: str, count: int, first: float, last: float) -> list[dict]:
    step = (last - first) / max(count - 1, 1)
    return [claude_call(f'{prefix}-{n}', 'Bash', {}, first + n * step) for n in range(count)]


def codex(kind: str, payload_kind: str, seconds: float, **payload: object) -> dict:
    return {'type': kind, 'timestamp': stamp(seconds), 'payload': {'type': payload_kind} | payload}


def codex_user(text: str, seconds: float) -> dict:
    return codex('response_item', 'message', seconds, role='user', content=[{'type': 'input_text', 'text': text}])


def write(root: Path, relative: str, records: Sequence[dict | str], age: timedelta = timedelta(days=1)) -> None:
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text('\n'.join(record if isinstance(record, str) else json.dumps(record) for record in records) + '\n')
    modified = (NOW - age).timestamp()
    os.utime(path, (modified, modified))


TODOS = [{'content': 'read', 'status': 'completed'}, {'content': 'write', 'status': 'completed'}]


class ChecklistAdoption(unittest.TestCase):
    def collect(self, root: Path) -> checklist_adoption.Sample:
        return checklist_adoption.collect([root], START - timedelta(days=1), NOW, NOW)

    # A long turn with a checklist, its forked copy, a child transcript and a
    # malformed line give one long turn with a checklist and three skips.
    def test_long_turn_with_checklist_counts_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            records = (
                [claude_prompt('p1', 0), claude_call('todo', 'TodoWrite', {'todos': TODOS}, 30), claude_result('todo', 31)]
                + [claude_call('skill', 'Skill', {'skill': 'progress'}, 32)]
                + claude_calls('c', 18, 40, 130)
                + [claude_call('child', 'Bash', {}, 50, is_sidechain=True)]
            )
            write(root, 'a/.claude/projects/-home-node/one.jsonl', records + ['{broken'])
            write(root, 'a/.claude/projects/-home-node/fork.jsonl', records)
            write(root, 'a/.claude/projects/-home-node/one/subagents/agent.jsonl', records)
            sample = self.collect(root)
            self.assertEqual((len(sample.turns), sample.duplicates, sample.child_files, sample.malformed), (1, 1, 1, 1))
            turn = sample.turns[0]
            self.assertEqual((turn.folder, len(turn.calls), turn.is_long(), turn.is_progress_skill), ('a', 20, True, True))
            self.assertEqual([(at - turn.started).total_seconds() for at in turn.checklist_calls()], [30.0])
            self.assertEqual((turn.items, len(turn.done)), (2, 2))
            self.assertIn('long with a checklist 1 of 1 (100%)', checklist_adoption.report(sample, START, NOW))

    # A long turn whose only checklist call failed is listed as an omission,
    # with its reminder; a short turn with a checklist is unnecessary use.
    def test_omission_and_unnecessary_use(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            nudge = {'type': 'attachment', 'timestamp': stamp(181), 'attachment': {'type': 'hook_additional_context', 'content': ['<user-steering>\nThis turn has no checklist. Plan.\n</user-steering>']}}
            write(root, 'b/.claude/projects/-home-node/two.jsonl', (
                [claude_prompt('p2', 0), claude_call('todo', 'TodoWrite', {'todos': TODOS}, 5), claude_result('todo', 6, is_error=True), nudge]
                + claude_calls('c', 20, 10, 200)
                + [claude_prompt('p3', 1000), claude_call('quick', 'TodoWrite', {'todos': TODOS[:1]}, 1005)]
            ))
            sample = self.collect(root)
            text = checklist_adoption.report(sample, START, NOW)
            self.assertEqual((len(sample.turns), sample.duplicates), (2, 0))
            self.assertIn('long with a checklist 0 of 1 (0%)', text)
            self.assertIn('checklist under the reminder thresholds (unnecessary) 1', text)
            self.assertIn('claude b calls 21 seconds 200 reminded yes', text)

    # A turn still being written is excluded: the last Claude segment of a
    # fresh file and a Codex turn without task_complete.
    def test_open_turns_are_excluded(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write(root, 'c/.claude/projects/-home-node/live.jsonl', [claude_prompt('p4', 0)] + claude_calls('c', 20, 1, 200), age=timedelta(minutes=5))
            write(root, 'c/.codex/sessions/2026/11/12/rollout-open.jsonl', [codex('event_msg', 'task_started', 0, turn_id='t1'), codex_user(PROMPT, 1)])
            sample = self.collect(root)
            self.assertEqual((len(sample.turns), sample.active), (0, 2))

    # Codex reaches update_plan through its exec tool; a subagent rollout and a
    # worker the agent started itself are child work; pi has no checklist and
    # its long turn is an omission.
    def test_codex_and_pi(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            calls = [codex('response_item', 'custom_tool_call', 10 + n, call_id=f'x{n}', name='exec', input='text(1)') for n in range(19)]
            plan = codex('response_item', 'custom_tool_call', 5, call_id='plan', name='exec', input='await tools.update_plan({plan:[]})')
            steer = codex('response_item', 'message', 181, role='developer', content=[{'type': 'input_text', 'text': '<user-steering>\nThis turn has no checklist.\n</user-steering>'}])
            done = codex('event_msg', 'task_complete', 300, turn_id='t2')
            write(root, 'd/.codex/sessions/2026/11/12/rollout-a.jsonl', [codex('event_msg', 'task_started', 0, turn_id='t2'), codex_user(PROMPT, 1), plan, steer] + calls + [done])
            write(root, 'd/.codex/sessions/2026/11/12/rollout-b.jsonl', [codex('session_meta', 'meta', 0, thread_source='subagent'), codex('event_msg', 'task_started', 0, turn_id='t3')])
            write(root, 'd/.codex/sessions/2026/11/12/rollout-c.jsonl', [codex('event_msg', 'task_started', 0, turn_id='t4'), codex_user(MANUAL, 1), plan] + calls + [done])
            pi_calls = [{'type': 'message', 'timestamp': stamp(5 + n * 10), 'message': {'role': 'assistant', 'content': [{'type': 'toolCall', 'id': f'p{n}'}]}} for n in range(20)]
            write(root, 'e/.pi/sessions/s.jsonl', [{'type': 'message', 'id': 'u1', 'timestamp': stamp(0), 'message': {'role': 'user', 'content': [{'type': 'text', 'text': PROMPT}]}}] + pi_calls)
            sample = self.collect(root)
            by_harness = {turn.harness: turn for turn in sample.turns}
            self.assertEqual((len(sample.turns), sample.child_files, sample.child_turns), (2, 1, 1))
            self.assertEqual((len(by_harness[Harness.CODEX].checklist_calls()), by_harness[Harness.CODEX].is_reminded, by_harness[Harness.CODEX].is_long()), (1, True, True))
            self.assertEqual((by_harness[Harness.PI].is_long(), by_harness[Harness.PI].checklist_calls()), (True, []))


if __name__ == '__main__':
    unittest.main()
