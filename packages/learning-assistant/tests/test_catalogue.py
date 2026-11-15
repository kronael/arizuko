import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

PACKAGE = Path(__file__).resolve().parents[1]
SCRIPTS = PACKAGE / 'skills/knowledge-site/scripts'
COMPONENTS = PACKAGE / 'skills/knowledge-site/components'
FIXTURES = PACKAGE / 'tests/fixtures'
TYPES = 'guide,research,art'


class CatalogueTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        base = Path(self.tmp.name)
        shutil.copytree(FIXTURES / 'site', base / 'site')
        shutil.copytree(FIXTURES / 'www', base / 'www')
        self.site = base / 'site'
        self.www = base / 'www'

    def tearDown(self):
        self.tmp.cleanup()

    def catalogue(self, *extra, types=TYPES):
        args = [str(self.site), '--ns', 'site', '--types', types, '--title', 'Fixture', *extra]
        return subprocess.run([sys.executable, '-B', str(SCRIPTS / 'catalogue.py'), *args],
                              check=False, capture_output=True, text=True)

    def lint(self):
        args = [str(self.site), '--www', str(self.www), '--ns', 'site']
        return subprocess.run([sys.executable, '-B', str(SCRIPTS / 'site_lint.py'), *args],
                              check=False, capture_output=True, text=True)

    def archive(self):
        return (self.site / 'archive/index.html').read_text()

    def test_default_out_lists_stamped_pages_and_lints_clean(self):
        result = self.catalogue()
        self.assertEqual(result.returncode, 0, result.stderr)
        text = self.archive()
        self.assertEqual(text.count('<tr><td>'), 5)
        self.assertIn('href="../section/one.html"', text)
        self.assertIn('href="../section/"', text)
        self.assertEqual(self.lint().returncode, 0, self.lint().stdout)

    def test_groups_follow_types_order_and_dates_descend(self):
        self.catalogue(types='research,guide,art')
        text = self.archive()
        self.assertLess(text.index('id="research"'), text.index('id="guide"'))
        self.catalogue()
        text = self.archive()
        self.assertLess(text.index('id="guide"'), text.index('id="research"'))
        self.assertLess(text.index('>Two'), text.index('>One'))

    def test_redirect_stub_and_unstamped_page_are_skipped(self):
        (self.site / 'plain.html').write_text('<!doctype html><title>Plain</title><h1>Plain</h1>')
        self.catalogue()
        text = self.archive()
        self.assertNotIn('moved.html', text)
        self.assertNotIn('plain.html', text)

    def test_unknown_type_exits_one_naming_the_page(self):
        result = self.catalogue(types='guide,art')
        self.assertEqual(result.returncode, 1)
        self.assertIn('section/one.html', result.stderr)
        self.assertFalse((self.site / 'archive/index.html').exists())

    def test_out_flag_overrides_default_path(self):
        out = self.site / 'elsewhere.html'
        self.assertEqual(self.catalogue('--out', str(out)).returncode, 0)
        self.assertTrue(out.exists())
        self.assertFalse((self.site / 'archive').exists())
        self.assertIn('href="section/one.html"', out.read_text())

    def test_rerun_does_not_list_its_own_output(self):
        self.catalogue()
        first = self.archive()
        self.assertEqual(self.catalogue().returncode, 0)
        self.assertEqual(self.archive().count('<tr><td>'), first.count('<tr><td>'))

    def test_fixture_assets_are_the_package_components(self):
        for name in ('hub.css', 'hub.js'):
            self.assertEqual((FIXTURES / 'site/assets' / name).read_bytes(), (COMPONENTS / name).read_bytes())

    def test_page_skeleton_instantiated_lints_clean(self):
        page = (COMPONENTS / 'page.html').read_text()
        page = page.replace('NS:', 'site:').replace('TYPE', 'guide').replace('YYYY-MM-DD', '2026-03-04')
        (self.site / 'section/new.html').write_text(page)
        result = self.lint()
        self.assertEqual(result.returncode, 0, result.stdout)


if __name__ == '__main__':
    unittest.main()
