#!/usr/bin/env python3
import argparse
import json
import math
import os
import statistics
import sys
import tomllib
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from datetime import UTC
from datetime import date
from datetime import datetime
from datetime import timedelta
from enum import StrEnum
from itertools import pairwise
from pathlib import Path

BYBIT_KLINE = 'https://api.bybit.com/v5/market/kline'
PAGEVIEWS = (
    'https://wikimedia.org/api/rest_v1/metrics/pageviews/per-article'
    '/en.wikipedia/all-access/user'
)
TIMEOUT = 20
DAY = timedelta(days=1)
MAX_LAG = 3 * DAY
MAD_TO_SIGMA = 1.4826


class MonitorError(Exception):
    pass


class Heat(StrEnum):
    CALM = 'calm'
    HOT = 'hot'


class Phase(StrEnum):
    QUIET = 'quiet'
    MOVE = 'move'
    CHATTER = 'chatter'
    HYPE = 'hype'


@dataclass(frozen=True, slots=True)
class Asset:
    name: str
    bybit: str
    wikipedia: str


@dataclass(frozen=True, slots=True)
class Config:
    user_agent: str
    window: int
    alert_z: float
    calm_z: float
    assets: list[Asset]


@dataclass(frozen=True, slots=True)
class AssetState:
    day: date
    price: Heat
    attention: Heat

    @property
    def phase(self) -> Phase:
        if self.price is Heat.HOT and self.attention is Heat.HOT:
            return Phase.HYPE
        if self.price is Heat.HOT:
            return Phase.MOVE
        if self.attention is Heat.HOT:
            return Phase.CHATTER
        return Phase.QUIET


@dataclass(frozen=True, slots=True)
class State:
    error: str
    assets: dict[str, AssetState]


@dataclass(frozen=True, slots=True)
class Reading:
    day: date
    change: float
    price_z: float
    views: int
    views_ratio: float
    attention_z: float


def read_config(path: Path) -> Config:
    try:
        raw = tomllib.loads(path.read_text())
        assets = [Asset(**entry) for entry in raw.pop('asset')]
        return Config(assets=assets, **raw)
    except OSError as error:
        raise MonitorError(f'config {path}: {error.strerror}') from error
    except KeyError as error:
        raise MonitorError(f'config {path}: no {error} table') from error
    except (tomllib.TOMLDecodeError, TypeError) as error:
        raise MonitorError(f'config {path}: {error}') from error


def replace_file(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    part = path.with_suffix('.part')
    part.write_text(text)
    part.replace(path)


def read_state(path: Path) -> State:
    if not path.exists():
        return State(error='', assets={})
    raw = json.loads(path.read_text())
    assets = {
        name: AssetState(
            day=date.fromisoformat(entry['day']),
            price=Heat(entry['price']),
            attention=Heat(entry['attention']),
        )
        for name, entry in raw['assets'].items()
    }
    return State(error=raw['error'], assets=assets)


def write_state(path: Path, state: State) -> None:
    assets = {
        name: {
            'day': entry.day.isoformat(),
            'price': entry.price.value,
            'attention': entry.attention.value,
        }
        for name, entry in state.assets.items()
    }
    raw = {'error': state.error, 'assets': assets}
    replace_file(path, json.dumps(raw, indent=1) + '\n')


def read_series(path: Path) -> dict[date, float]:
    if not path.exists():
        return {}
    raw = json.loads(path.read_text())
    return {date.fromisoformat(day): value for day, value in raw.items()}


def write_series(path: Path, series: dict[date, float]) -> None:
    raw = {day.isoformat(): series[day] for day in sorted(series)}
    replace_file(path, json.dumps(raw, indent=0) + '\n')


def http_get(url: str, user_agent: str) -> bytes:
    request = urllib.request.Request(url, headers={'User-Agent': user_agent})
    with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
        return response.read()


def to_epoch_ms(day: date) -> int:
    midnight = datetime(day.year, day.month, day.day, tzinfo=UTC)
    return int(midnight.timestamp() * 1000)


def fetch_closes(
    symbol: str, first: date, last: date, user_agent: str
) -> dict[date, float]:
    query = urllib.parse.urlencode({
        'category': 'linear',
        'symbol': symbol,
        'interval': 'D',
        'start': to_epoch_ms(first),
        'end': to_epoch_ms(last),
        'limit': 1000,
    })
    payload = json.loads(http_get(f'{BYBIT_KLINE}?{query}', user_agent))
    if payload['retCode'] != 0:
        reason = payload['retMsg']
        raise MonitorError(f'bybit {symbol}: {reason}')
    return {
        datetime.fromtimestamp(int(row[0]) / 1000, UTC).date(): float(row[4])
        for row in payload['result']['list']
    }


def fetch_views(
    article: str, first: date, last: date, user_agent: str
) -> dict[date, float]:
    title = urllib.parse.quote(article, safe='')
    url = f'{PAGEVIEWS}/{title}/daily/{first:%Y%m%d}/{last:%Y%m%d}'
    try:
        payload = json.loads(http_get(url, user_agent))
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
        return {}
    return {
        date.fromisoformat(item['timestamp'][:8]): float(item['views'])
        for item in payload['items']
    }


def first_missing_day(
    series: dict[date, float], last_closed: date, window: int
) -> date:
    if not series:
        return last_closed - (window + 1) * DAY - MAX_LAG
    return max(series) + DAY


def require_fresh(
    series: dict[date, float], last_closed: date, label: str
) -> None:
    if not series:
        raise MonitorError(f'{label}: the source returned no days')
    newest = max(series)
    if newest < last_closed - MAX_LAG:
        raise MonitorError(f'{label}: newest day {newest} is stale')


def refresh_closes(
    home: Path, asset: Asset, config: Config, last_closed: date
) -> dict[date, float]:
    path = home / 'data' / f'{asset.name}.closes.json'
    closes = read_series(path)
    first = first_missing_day(closes, last_closed, config.window)
    if first <= last_closed:
        fresh = fetch_closes(asset.bybit, first, last_closed, config.user_agent)
        closes = closes | fresh
        write_series(path, closes)
    require_fresh(closes, last_closed, f'{asset.name} closes')
    return closes


def refresh_views(
    home: Path, asset: Asset, config: Config, last_closed: date
) -> dict[date, float]:
    path = home / 'data' / f'{asset.name}.views.json'
    views = read_series(path)
    first = first_missing_day(views, last_closed, config.window)
    if first <= last_closed:
        fresh = fetch_views(
            asset.wikipedia, first, last_closed, config.user_agent
        )
        views = views | fresh
        write_series(path, views)
    require_fresh(views, last_closed, f'{asset.name} views')
    return views


def median_z(sample: float, baseline: list[float], label: str) -> float:
    median = statistics.median(baseline)
    mad = statistics.median([abs(value - median) for value in baseline])
    if mad == 0:
        raise MonitorError(f'{label}: the baseline is flat')
    return (sample - median) / (MAD_TO_SIGMA * mad)


def read_day(
    asset: Asset,
    closes: dict[date, float],
    views: dict[date, float],
    day: date,
    window: int,
) -> Reading:
    past_closes = [closes[d] for d in sorted(closes) if d <= day]
    past_views = [views[d] for d in sorted(views) if d <= day]
    if len(past_closes) < window + 2 or len(past_views) < window + 1:
        raise MonitorError(f'{asset.name}: {day} lacks {window} earlier days')
    returns = [
        math.log(after / before)
        for before, after in pairwise(past_closes[-(window + 2):])
    ]
    logs = [math.log1p(count) for count in past_views[-(window + 1):]]
    return Reading(
        day=day,
        change=past_closes[-1] / past_closes[-2] - 1,
        price_z=median_z(returns[-1], returns[:-1], f'{asset.name} closes'),
        views=int(past_views[-1]),
        views_ratio=math.expm1(logs[-1] - statistics.median(logs[:-1])),
        attention_z=median_z(logs[-1], logs[:-1], f'{asset.name} views'),
    )


def next_heat(heat: Heat, score: float, config: Config) -> Heat:
    if score >= config.alert_z:
        return Heat.HOT
    if score < config.calm_z:
        return Heat.CALM
    return heat


def describe(reading: Reading) -> str:
    return (
        f'{reading.day}'
        f' | price {reading.change:+.1%} (z {reading.price_z:+.1f})'
        f' | views {reading.views} ({reading.views_ratio:+.0%} vs median,'
        f' z {reading.attention_z:+.1f})'
    )


def judge_asset(
    asset: Asset,
    known: AssetState | None,
    closes: dict[date, float],
    views: dict[date, float],
    config: Config,
) -> tuple[AssetState, list[str]]:
    newest = min(max(closes), max(views))
    state = known or AssetState(
        day=newest - DAY, price=Heat.CALM, attention=Heat.CALM
    )
    days = [d for d in sorted(closes) if state.day < d <= newest and d in views]
    lines: list[str] = []
    for day in days:
        reading = read_day(asset, closes, views, day, config.window)
        before = state.phase
        state = AssetState(
            day=day,
            price=next_heat(state.price, abs(reading.price_z), config),
            attention=next_heat(state.attention, reading.attention_z, config),
        )
        if state.phase is not before and state.phase is not Phase.QUIET:
            change = f'{before} -> {state.phase}'
            lines.append(f'{asset.name}: {change} | {describe(reading)}')
    return state, lines


def do_check(
    config: Config, home: Path, known: dict[str, AssetState], today: date
) -> tuple[dict[str, AssetState], list[str]]:
    last_closed = today - DAY
    assets = dict(known)
    lines: list[str] = []
    for asset in config.assets:
        closes = refresh_closes(home, asset, config, last_closed)
        views = refresh_views(home, asset, config, last_closed)
        state, changes = judge_asset(
            asset, known.get(asset.name), closes, views, config
        )
        assets[asset.name] = state
        lines.extend(changes)
    return assets, lines


def check(config_path: Path, home: Path, today: date) -> int:
    state_path = home / 'state.json'
    state = read_state(state_path)
    try:
        config = read_config(config_path)
        assets, lines = do_check(config, home, state.assets, today)
    except (MonitorError, OSError) as error:
        failure = f'macrohype: Failed: {error}'
        if failure != state.error:
            print(failure)
        write_state(state_path, State(error=failure, assets=state.assets))
        return 1
    if state.error:
        print('macrohype: recovered')
    for line in lines:
        print(line)
    write_state(state_path, State(error='', assets=assets))
    return 0


def status(config_path: Path, home: Path) -> int:
    try:
        config = read_config(config_path)
        state = read_state(home / 'state.json')
        for asset in config.assets:
            judged = state.assets.get(asset.name)
            if not judged:
                raise MonitorError(f'{asset.name}: no judged day, run check')
            closes = read_series(home / 'data' / f'{asset.name}.closes.json')
            views = read_series(home / 'data' / f'{asset.name}.views.json')
            reading = read_day(asset, closes, views, judged.day, config.window)
            print(f'{asset.name}: {judged.phase} | {describe(reading)}')
    except MonitorError as error:
        print(f'macrohype: Failed: {error}')
        return 1
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(
        prog='macrohype',
        description='Daily price and attention phases for a watchlist. '
        'check fetches the missing days and prints one line per phase change; '
        'status prints the phase of every asset from the days on disk.',
    )
    parser.add_argument('command', choices=['check', 'status'])
    args = parser.parse_args()
    home = Path(os.environ.get('MACROHYPE_HOME', '~/macrohype')).expanduser()
    config_path = Path(
        os.environ.get('MACROHYPE_CONFIG', '~/macrohype.toml')
    ).expanduser()
    if args.command == 'check':
        return check(config_path, home, datetime.now(UTC).date())
    return status(config_path, home)


if __name__ == '__main__':
    sys.exit(main())
