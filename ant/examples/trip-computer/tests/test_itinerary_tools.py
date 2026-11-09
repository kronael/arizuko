import importlib.util
import os
import runpy
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1] / 'skills/itinerary/scripts'
LINT = SCRIPTS / 'trip_lint.py'
LINK = SCRIPTS / 'link_check.py'


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


if __name__ == '__main__':
    unittest.main()
