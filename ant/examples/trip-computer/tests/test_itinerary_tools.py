import html
import importlib.util
import json
import os
import re
import runpy
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.parse
from pathlib import Path
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1] / 'skills/itinerary/scripts'
LINT = SCRIPTS / 'trip_lint.py'
LINK = SCRIPTS / 'link_check.py'
CENSUS = SCRIPTS / 'osm_census.py'


def load(path):
    spec = importlib.util.spec_from_file_location(path.stem, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def osm_tile(cache, elements, lat=50.0, lon=14.0, radius=0.1):
    census = load(CENSUS)
    (bbox,) = [census.bbstr(t) for t in census.tiles_for(lat, lon, radius)]
    body = f'<?xml version="1.0"?><osm>{elements}</osm>'.ljust(1200)
    (cache / (bbox.replace(',', '_') + '.xml')).write_text(body)


def run_census(out, cache, *extra):
    args = ['--lat', '50.0', '--lon', '14.0', '--radius', '0.1', '--out', str(out), '--cache', str(cache), *extra]
    return subprocess.run([sys.executable, '-B', str(CENSUS), *args], capture_output=True, text=True)


class ItineraryToolsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.site = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def run_lint(self, *extra):
        return subprocess.run([sys.executable, '-B', str(LINT), str(self.site), *extra], capture_output=True, text=True)

    def test_missing_and_empty_sites_fail(self):
        self.assertNotEqual(self.run_lint().returncode, 0)
        result = subprocess.run([sys.executable, '-B', str(LINT), str(self.site / 'missing')], capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_missing_relative_database_and_asset_fail(self):
        (self.site / 'index.html').write_text('<title>Trip</title><h1>Trip</h1><a href="db/places.md">places</a><link href="assets/site.css">')
        result = self.run_lint('--rule', 'internal-links')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('db/places.md', result.stdout)
        self.assertIn('assets/site.css', result.stdout)

    def test_navigation_must_cover_days(self):
        (self.site / 'day1.html').write_text('<title>A</title><h1>A</h1><nav></nav>')
        (self.site / 'day2.html').write_text('<title>B</title><h1>B</h1><nav></nav>')
        result = self.run_lint('--rule', 'nav')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('nav has no link', result.stdout)

    def test_navigation_stays_inside_one_trip(self):
        for trip in ('t1', 't2'):
            (self.site / trip).mkdir()
        nav = '<title>D</title><h1>D</h1><nav><a href="day1.html">1</a> <a href="day2.html">2</a></nav>'
        (self.site / 't1/day1.html').write_text(nav)
        (self.site / 't1/day2.html').write_text(nav)
        (self.site / 't1/day4-alternative.html').write_text('<title>A</title><h1>A</h1><a href="day1.html">back</a>')
        (self.site / 't2/day1.html').write_text('<title>D</title><h1>D</h1><nav></nav>')
        result = self.run_lint('--rule', 'nav')
        self.assertEqual(result.returncode, 0, result.stdout)

    def test_named_route_needs_id(self):
        sys.path.insert(0, str(SCRIPTS))
        try:
            import mapyrc
            rc = mapyrc.encode([(50, 14), (50.01, 14.01)])
        finally:
            sys.path.pop(0)
        (self.site / 'index.html').write_text(f'<title>Trip</title><h1>Trip</h1><a href="https://mapy.com/planovani-trasy?rc={rc}&amp;rs=base&amp;rs=coor&amp;ri=&amp;ri=&amp;x=14&amp;y=50">route</a>')
        result = self.run_lint('--rule', 'mapy-links')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('has no id', result.stdout)

    def test_links_resolve_against_the_public_root(self):
        (self.site / 'assets').mkdir()
        (self.site / 'assets/site.css').write_text('.hub-container {}')
        for trip in ('t1', 't2'):
            (self.site / trip).mkdir()
            (self.site / trip / 'index.html').write_text('<title>T</title><h1>T</h1>')
        page = self.site / 't1/index.html'
        lint = [sys.executable, '-B', str(LINT), str(self.site / 't1'), '--root', str(self.site), '--rule', 'internal-links', '--rule', 'css-class']
        page.write_text('<link rel="stylesheet" href="../assets/site.css"><div class="hub-container"><a href="../t2/">t2</a></div>')
        result = subprocess.run(lint, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout)
        for href in ('/pub/x/t1/assets/site.css', '..//pub/x/assets/site.css'):
            page.write_text(f'<link rel="stylesheet" href="{href}">')
            self.assertNotEqual(subprocess.run(lint, capture_output=True, text=True).returncode, 0, href)

    def test_class_needs_a_linked_stylesheet(self):
        (self.site / 'index.html').write_text('<div class="hub-container"></div>')
        result = self.run_lint('--rule', 'css-class')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('hub-container', result.stdout)

    def test_mapy_entity_and_route_urls(self):
        base = 'https://mapy.com/en/turisticka?'
        valid = [base + 'source=stre&id=106188&x=16.165&y=50.414&z=17', base + 'source=traf&id=55208&x=16.16&y=50.41&z=17']
        invalid = [base + 'source=firm&x=14&y=50&z=17', base + 'x=500&y=999&z=17',
                   base + 'planovani-trasy&rc=&x=14&y=50', base + 'planovani-trasy&rc=00&rs=coor&ri=&x=14&y=50']
        for url, ok in [(u, True) for u in valid] + [(u, False) for u in invalid]:
            (self.site / 'index.html').write_text(f'<a href="{html.escape(url)}">Place</a>')
            result = self.run_lint('--rule', 'mapy-links')
            self.assertEqual(result.returncode == 0, ok, (url, result.stdout))

    def test_phone_outside_link_fails_for_any_country(self):
        (self.site / 'index.html').write_text('<title>Trip</title><h1>Trip</h1>+351 123 456 789')
        result = self.run_lint('--rule', 'tel-links')
        self.assertNotEqual(result.returncode, 0)

    def test_meal_rating_with_review_count(self):
        (self.site / 'index.html').write_text('<title>Trip</title><h1>Trip</h1><ul class="food-list"><li>Cafe open to 18:00 · Google 4.7 (123 reviews) · <a href="tel:+351123456789">phone</a> · <a href="https://google.com/maps/place/x">google</a> · <a href="https://mapy.com/x">mapy</a></li></ul>')
        self.assertEqual(self.run_lint('--rule', 'meal-lines').returncode, 0)

    def test_html_balance(self):
        (self.site / 'index.html').write_text('<main><p>text</main>')
        result = self.run_lint('--rule', 'html-balance')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unexpected </main>', result.stdout)

    def test_missing_image_stylesheet_and_script(self):
        (self.site / 'index.html').write_text('<img src="missing.jpg"><link rel="stylesheet" href="missing.css"><script src="missing.js"></script>')
        result = self.run_lint('--rule', 'internal-links')
        for name in ('missing.jpg', 'missing.css', 'missing.js'):
            self.assertIn(name, result.stdout)
        self.assertNotEqual(result.returncode, 0)

    def test_undefined_css_class(self):
        (self.site / 'index.html').write_text('<div class="not-a-real-class"></div>')
        result = self.run_lint('--rule', 'css-class')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('not-a-real-class', result.stdout)

    def test_empty_href_outside_day(self):
        (self.site / 'index.html').write_text('<a href="">empty</a>')
        result = self.run_lint('--rule', 'empty-href')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('empty href', result.stdout)
        parser = runpy.run_path(str(LINT))['_Parser']()
        parser.feed('<img src="missing.jpg"><a href="">empty</a>')
        self.assertEqual([link[0] for link in parser.links], ['missing.jpg', ''])

    def test_osm_object_url(self):
        (self.site / 'index.html').write_text('<a href="https://www.openstreetmap.org/node/nope">place</a>')
        result = self.run_lint('--rule', 'osm-links')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('invalid OSM', result.stdout)

    def test_meal_rating_names_platform(self):
        (self.site / 'index.html').write_text('<ul class="food-list"><li>Cafe open to 18:00 · 4.7</li></ul>')
        self.assertIn('without rating', self.run_lint('--rule', 'meal-lines').stdout)

    def test_meal_opening_time(self):
        (self.site / 'index.html').write_text('<ul class="food-list"><li>Cafe · Google 4.7</li></ul>')
        self.assertIn('without opening time', self.run_lint('--rule', 'meal-lines').stdout)

    def test_meal_selection_cap(self):
        (self.site / 'index.html').write_text('<div class="timeline-date">08:00 — Breakfast</div><ul class="food-list"><li>A</li><li>B</li><li>C</li></ul>')
        result = self.run_lint('--rule', 'meal-cap')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('maximum 2', result.stdout)

    def test_compatibility_entry_uses_site_checker(self):
        page = self.site / 'index.html'
        page.write_text('<title>Trip</title><h1>Trip</h1><a href="missing.html">missing</a>')
        result = subprocess.run([sys.executable, '-B', str(SCRIPTS / 'page_check.py'), str(page)], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('internal-links', result.stdout)

    def test_link_help_does_not_start_browser(self):
        result = subprocess.run([sys.executable, '-B', str(LINK), '--help'], capture_output=True, text=True, env={**os.environ, 'PATH': ''})
        self.assertEqual(result.returncode, 0)

    def test_failed_browser_is_not_verified(self):
        browser = self.site / 'agent-browser'
        browser.write_text('#!/bin/sh\nexit 7\n')
        browser.chmod(0o755)
        page = self.site / 'page.html'
        page.write_text('<a href="https://mapy.com/example">place</a>')
        result = subprocess.run([sys.executable, '-B', str(LINK), str(page)], capture_output=True, text=True, env={**os.environ, 'PATH': str(self.site)})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('FAIL browser', result.stderr)

    def test_browser_timeout_is_not_success(self):
        spec = importlib.util.spec_from_file_location('link_check', LINK)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with patch.object(module.subprocess, 'run', side_effect=subprocess.TimeoutExpired('agent-browser', 60)):
            with self.assertRaises(subprocess.TimeoutExpired):
                module.ab('snapshot')

    def test_census_rerun_keeps_enriched_rows(self):
        out, cache = self.site / 'db', self.site / 'tiles'
        out.mkdir(); cache.mkdir()
        cafe = '<node id="{}" lat="50.0" lon="14.000{}"><tag k="amenity" v="cafe"/><tag k="name" v="{}"/></node>'
        osm_tile(cache, cafe.format(1, 1, 'Cafe A') + cafe.format(2, 2, 'Cafe B'))
        (out / 'food.md').write_text('| km | name | osm |\n|---|---|---|\n| 0.0 | Cafe A VERIFIED ENRICHMENT | node/1 |\n')
        self.assertEqual(run_census(out, cache).returncode, 0)
        food = (out / 'food.md').read_text()
        self.assertIn('VERIFIED ENRICHMENT', food)
        self.assertEqual(food.count('Cafe A'), 1)
        self.assertEqual(food.count('Cafe B'), 1)

    def test_census_cell_text_stays_in_its_cell(self):
        out, cache = self.site / 'db', self.site / 'tiles'
        out.mkdir(); cache.mkdir()
        osm_tile(cache, '<node id="1" lat="50.0" lon="14.0001"><tag k="amenity" v="cafe"/><tag k="name" v="Cafe | A"/><tag k="opening_hours" v="Mo 8-12&#10;Tu off"/></node>')
        self.assertEqual(run_census(out, cache).returncode, 0)
        header, _, row = (out / 'food.md').read_text().splitlines()[2:5]
        self.assertEqual(len(re.findall(r'(?<!\\)\|', row)), header.count('|'))

    def test_census_rejects_bad_area_before_writing(self):
        for extra in (['--radius', '0'], ['--lat', '91'], ['--also', '50,200']):
            out = self.site / 'db'
            result = run_census(out, self.site / 'tiles', *extra)
            self.assertNotEqual(result.returncode, 0, extra)
            self.assertFalse(out.exists(), extra)

    def test_census_fetch_is_bounded_and_reports_what_it_saw(self):
        census = load(CENSUS)
        calls = []
        def curl(args, **_):
            calls.append(args)
            return subprocess.CompletedProcess(args, 28, '000', 'curl: (28) Connection timed out')
        with patch.object(census.subprocess, 'run', side_effect=curl):
            with self.assertRaises(SystemExit) as raised:
                census.fetch('14.0000,50.0000,14.0100,50.0100', str(self.site))
        self.assertIn('--max-time', calls[0])
        self.assertIn('Connection timed out', str(raised.exception))
        self.assertNotIn('allowlist', str(raised.exception))

    def test_census_refetches_an_old_tile(self):
        census = load(CENSUS)
        bbox = '14.0000,50.0000,14.0100,50.0100'
        tile = self.site / (bbox.replace(',', '_') + '.xml')
        tile.write_text('<?xml version="1.0"?><osm></osm>'.ljust(1200))
        old = time.time() - 30 * 86400
        os.utime(tile, (old, old))
        calls = []
        def curl(args, **_):
            calls.append(args)
            Path(args[args.index('-o') + 1]).write_text('<?xml version="1.0"?><osm></osm>')
            return subprocess.CompletedProcess(args, 0, '200', '')
        with patch.object(census.subprocess, 'run', side_effect=curl):
            census.fetch(bbox, str(self.site))
        self.assertEqual(len(calls), 1)

    def test_census_counts_a_trail_way_once_across_tiles(self):
        out, cache = self.site / 'db', self.site / 'tiles'
        out.mkdir(); cache.mkdir()
        osm_tile(cache, '<node id="1" lat="50.0" lon="14.0"/><node id="2" lat="50.001" lon="14.0"/>'
                 '<way id="10"><nd ref="1"/><nd ref="2"/></way>'
                 '<relation id="20"><member type="way" ref="10" role=""/><tag k="type" v="route"/><tag k="route" v="hiking"/></relation>')
        self.assertEqual(run_census(out, cache, '--also', '50.0,14.0', '--also-radius', '0.1').returncode, 0)
        trails = json.loads((out / 'census.json').read_text())['trails']
        self.assertEqual([t['km_in_area'] for t in trails], [0.11])

    def test_mapy_codec_round_trips_boundaries_and_rejects_half_points(self):
        mapyrc = load(SCRIPTS / 'mapyrc.py')
        for points in ([(90, 180), (0, 0)], [(-90, -180), (50.08, 14.42)], [(50.0875, 14.4208), (50.1, 14.5)]):
            back = mapyrc.decode(mapyrc.encode(points))
            self.assertEqual(len(back), len(points))
            for (lat, lon), (blat, blon) in zip(points, back):
                self.assertLess(max(abs(lat - blat), abs(lon - blon)), 1e-5, points)
        with self.assertRaises(ValueError):
            mapyrc.decode('00')

    def test_route_keeps_named_mapy_points(self):
        routes = self.site / 'routes.json'
        routes.write_text(json.dumps({'r': {'mode': 'car', 'points': [[50.0, 14.0], [50.01, 14.01, 'stre', 106188]]}}))
        result = subprocess.run([sys.executable, '-B', str(SCRIPTS / 'mapyrc.py'), str(routes)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        query = urllib.parse.parse_qs(urllib.parse.urlparse(result.stdout.split('\t')[3]).query, keep_blank_values=True)
        self.assertEqual(query['rs'], ['coor', 'stre'])
        self.assertEqual(query['ri'], ['', '106188'])
        self.assertEqual(len(load(SCRIPTS / 'mapyrc.py').decode(query['rc'][0])), 2)

    def test_trails_connection_failure_is_not_called_a_refusal(self):
        failure = urllib.error.URLError('Connection refused')
        with patch.object(sys, 'argv', ['osm_trails.py', '50', '14', '50.01', '14.01']), patch('urllib.request.urlopen', side_effect=failure):
            with self.assertRaises(SystemExit) as raised:
                runpy.run_path(str(SCRIPTS / 'osm_trails.py'))
        self.assertIn('Connection refused', str(raised.exception.code))
        self.assertNotIn('allowlist', str(raised.exception.code))


if __name__ == '__main__':
    unittest.main()
