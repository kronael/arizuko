import contextlib
import io
import json
import math
import tempfile
import types
import unittest
import urllib.error
import urllib.parse
from datetime import UTC
from datetime import date
from datetime import datetime
from datetime import timedelta
from importlib.machinery import SourceFileLoader
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / 'skills/macrohype/scripts/macrohype.py'

CONFIG = """
user_agent = "macrohype-test/1 (test@example.org)"
window = 10
alert_z = 4.0
calm_z = 2.0

[[asset]]
name = "bitcoin"
bybit = "BTCUSDT"
wikipedia = "Bitcoin"
"""

DAY = timedelta(days=1)
START = date(2026, 1, 1)
TODAY = START + 30 * DAY
YESTERDAY = TODAY - DAY


def load():
    loader = SourceFileLoader('macrohype', str(SCRIPT))
    module = types.ModuleType('macrohype')
    loader.exec_module(module)
    return module


def to_ms(day):
    return int(datetime(day.year, day.month, day.day, tzinfo=UTC).timestamp() * 1000)


class Sources:
    def __init__(self):
        days = [START + i * DAY for i in range(60)]
        self.closes = {day: 100 + math.sin(i * 1.3) for i, day in enumerate(days)}
        self.views = {day: 1000 + round(80 * math.sin(i * 2.1)) for i, day in enumerate(days)}
        self.views_through = days[-1]
        self.down = False
        self.urls = []

    def spike(self, day, change, views):
        shift = self.closes[day - DAY] * (1 + change) / self.closes[day]
        self.closes = {d: c * shift if d >= day else c for d, c in self.closes.items()}
        self.views[day] = views

    def urlopen(self, request, timeout):
        url = urllib.parse.urlparse(request.full_url)
        self.urls.append(url)
        if self.down:
            raise urllib.error.URLError('Tunnel connection failed: 403 Forbidden')
        if url.netloc == 'api.bybit.com':
            query = urllib.parse.parse_qs(url.query)
            first = int(query['start'][0])
            last = int(query['end'][0])
            rows = [
                [str(to_ms(day)), '0', '0', '0', str(close), '0', '0']
                for day, close in sorted(self.closes.items(), reverse=True)
                if first <= to_ms(day) <= last
            ]
            body = {'retCode': 0, 'retMsg': 'OK', 'result': {'list': rows}}
        else:
            first, last = url.path.split('/')[-2:]
            items = [
                {'timestamp': f'{day:%Y%m%d}00', 'views': count}
                for day, count in sorted(self.views.items())
                if first <= f'{day:%Y%m%d}' <= last and day <= self.views_through
            ]
            if not items:
                raise urllib.error.HTTPError(request.full_url, 404, 'Not Found', {}, None)
            body = {'items': items}
        return io.BytesIO(json.dumps(body).encode())


class CheckTest(unittest.TestCase):
    def setUp(self):
        self.macrohype = load()
        self.sources = Sources()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = Path(tmp.name) / 'macrohype'
        self.config = Path(tmp.name) / 'macrohype.toml'
        self.config.write_text(CONFIG)
        patcher = patch('urllib.request.urlopen', self.sources.urlopen)
        patcher.start()
        self.addCleanup(patcher.stop)

    def check(self, today):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = self.macrohype.check(self.config, self.home, today)
        return code, out.getvalue()

    def status(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = self.macrohype.status(self.config, self.home)
        return code, out.getvalue()

    def test_quiet_watchlist_prints_nothing(self):
        # An ordinary day on the first run posts nothing and records the day.
        self.assertEqual(self.check(TODAY), (0, ''))
        state = json.loads((self.home / 'state.json').read_text())
        self.assertEqual(
            state['assets']['bitcoin'],
            {'day': YESTERDAY.isoformat(), 'price': 'calm', 'attention': 'calm'},
        )

    def test_price_and_attention_spike_prints_one_hype_line(self):
        # A 9% drop with four times the usual views turns the asset to hype.
        self.sources.spike(YESTERDAY, -0.09, 4000)
        code, out = self.check(TODAY)
        self.assertEqual(code, 0)
        self.assertEqual(len(out.splitlines()), 1)
        self.assertTrue(out.startswith(f'bitcoin: quiet -> hype | {YESTERDAY} | price -9.0%'), out)
        self.assertIn('views 4000 (+', out)

    def test_price_spike_alone_is_a_move(self):
        # A price move the crowd does not follow is a move, not hype.
        self.sources.spike(YESTERDAY, 0.09, 1000)
        self.assertIn('bitcoin: quiet -> move |', self.check(TODAY)[1])

    def test_attention_spike_alone_is_chatter(self):
        # Views without a price move are chatter.
        self.sources.spike(YESTERDAY, 0.001, 4000)
        self.assertIn('bitcoin: quiet -> chatter |', self.check(TODAY)[1])

    def test_same_day_again_is_silent_and_fetches_nothing(self):
        # A second run before the next daily close repeats no line and makes
        # no request: every closed day is on disk.
        self.sources.spike(YESTERDAY, -0.09, 4000)
        self.check(TODAY)
        self.sources.urls.clear()
        self.assertEqual(self.check(TODAY), (0, ''))
        self.assertEqual(self.sources.urls, [])

    def test_next_day_fetches_only_the_missing_day(self):
        # One day later each source is asked for that one day and nothing older.
        self.check(TODAY)
        self.sources.urls.clear()
        self.check(TODAY + DAY)
        bybit, wikipedia = self.sources.urls
        query = urllib.parse.parse_qs(bybit.query)
        self.assertEqual(query['start'], [str(to_ms(TODAY))])
        self.assertEqual(query['end'], [str(to_ms(TODAY))])
        self.assertTrue(wikipedia.path.endswith(f'/Bitcoin/daily/{TODAY:%Y%m%d}/{TODAY:%Y%m%d}'))

    def test_return_to_quiet_is_silent_and_the_next_spike_alerts_again(self):
        # The day after a spike posts nothing; a later spike posts a new line.
        self.sources.spike(YESTERDAY, -0.09, 4000)
        self.check(TODAY)
        self.assertEqual(self.check(TODAY + DAY), (0, ''))
        self.sources.spike(TODAY + 2 * DAY, 0.12, 9000)
        self.assertEqual(self.check(TODAY + 2 * DAY), (0, ''))
        code, out = self.check(TODAY + 3 * DAY)
        self.assertIn(f'bitcoin: quiet -> hype | {TODAY + 2 * DAY}', out)

    def test_missed_days_are_judged_in_order(self):
        # A run that comes three days late still posts the spike it missed.
        self.check(TODAY)
        self.sources.spike(TODAY, -0.09, 4000)
        code, out = self.check(TODAY + 3 * DAY)
        self.assertEqual(code, 0)
        self.assertEqual(len(out.splitlines()), 1)
        self.assertIn(f'bitcoin: quiet -> hype | {TODAY}', out)

    def test_late_pageviews_delay_the_day_without_an_error(self):
        # Wikimedia has not published yesterday yet: the day waits, silently.
        self.sources.spike(YESTERDAY, -0.09, 4000)
        self.sources.views_through = YESTERDAY - DAY
        self.assertEqual(self.check(TODAY), (0, ''))
        self.sources.views_through = YESTERDAY
        self.assertIn(f'bitcoin: quiet -> hype | {YESTERDAY}', self.check(TODAY)[1])

    def test_score_between_the_thresholds_keeps_the_heat(self):
        # Between calm_z and alert_z a series keeps the heat it had.
        config = self.macrohype.read_config(self.config)
        heat = self.macrohype.Heat
        self.assertIs(self.macrohype.next_heat(heat.HOT, 3.0, config), heat.HOT)
        self.assertIs(self.macrohype.next_heat(heat.CALM, 3.0, config), heat.CALM)
        self.assertIs(self.macrohype.next_heat(heat.CALM, 4.0, config), heat.HOT)
        self.assertIs(self.macrohype.next_heat(heat.HOT, 1.9, config), heat.CALM)

    def test_source_failure_prints_once_then_recovery_prints_once(self):
        # A refused source fails every run, posts the failure once and posts
        # the recovery once.
        self.check(TODAY)
        self.sources.down = True
        code, out = self.check(TODAY + DAY)
        self.assertEqual(code, 1)
        self.assertEqual(out.splitlines(), ['macrohype: Failed: <urlopen error Tunnel connection failed: 403 Forbidden>'])
        self.assertEqual(self.check(TODAY + DAY), (1, ''))
        self.sources.down = False
        self.assertEqual(self.check(TODAY + DAY), (0, 'macrohype: recovered\n'))
        self.assertEqual(self.check(TODAY + DAY), (0, ''))

    def test_stale_source_fails(self):
        # Pageviews that stop arriving are a failure after three days.
        self.check(TODAY)
        self.sources.views_through = YESTERDAY
        code, out = self.check(TODAY + 5 * DAY)
        self.assertEqual(code, 1)
        self.assertEqual(out, f'macrohype: Failed: bitcoin views: newest day {YESTERDAY} is stale\n')

    def test_unknown_symbol_fails_with_the_exchange_reason(self):
        # Bybit answers 200 with an error code for a symbol it does not list.
        answer = io.BytesIO(json.dumps({'retCode': 10001, 'retMsg': 'params error: Symbol Is Invalid', 'result': {}}).encode())
        with patch('urllib.request.urlopen', return_value=answer):
            code, out = self.check(TODAY)
        self.assertEqual(code, 1)
        self.assertEqual(out, 'macrohype: Failed: bybit BTCUSDT: params error: Symbol Is Invalid\n')

    def test_missing_config_fails(self):
        # Without the group config the run fails and names the file.
        self.config.unlink()
        code, out = self.check(TODAY)
        self.assertEqual(code, 1)
        self.assertEqual(out, f'macrohype: Failed: config {self.config}: No such file or directory\n')
        self.assertEqual(self.sources.urls, [])

    def test_config_without_an_asset_fails(self):
        # An empty watchlist is a broken config, not a quiet one.
        self.config.write_text(CONFIG.split('[[asset]]')[0])
        code, out = self.check(TODAY)
        self.assertEqual(code, 1)
        self.assertEqual(out, f"macrohype: Failed: config {self.config}: no 'asset' table\n")

    def test_config_without_a_threshold_fails(self):
        # Every threshold is the deployment's choice: none has a default.
        self.config.write_text(CONFIG.replace('alert_z = 4.0\n', ''))
        code, out = self.check(TODAY)
        self.assertEqual(code, 1)
        self.assertIn('alert_z', out)

    def test_status_prints_the_phase_from_disk(self):
        # status answers from the judged day on disk and makes no request.
        self.sources.spike(YESTERDAY, -0.09, 4000)
        self.check(TODAY)
        self.sources.urls.clear()
        code, out = self.status()
        self.assertEqual(code, 0)
        self.assertTrue(out.startswith(f'bitcoin: hype | {YESTERDAY} | price -9.0%'), out)
        self.assertEqual(self.sources.urls, [])

    def test_status_before_the_first_check_fails(self):
        # status never invents a phase for an asset no run has judged.
        self.assertEqual(self.status(), (1, 'macrohype: Failed: bitcoin: no judged day, run check\n'))


if __name__ == '__main__':
    unittest.main()
