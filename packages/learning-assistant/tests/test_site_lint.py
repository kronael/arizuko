import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

PACKAGE = Path(__file__).resolve().parents[1]
LINT = PACKAGE / 'skills/knowledge-site/scripts/site_lint.py'
FIXTURES = PACKAGE / 'tests/fixtures'


class SiteLintTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        base = Path(self.tmp.name)
        shutil.copytree(FIXTURES / 'site', base / 'site')
        shutil.copytree(FIXTURES / 'www', base / 'www')
        self.site = base / 'site'
        self.www = base / 'www'

    def tearDown(self):
        self.tmp.cleanup()

    def run_lint(self, *extra):
        args = [str(self.site), '--www', str(self.www), '--ns', 'site', *extra]
        return subprocess.run([sys.executable, '-B', str(LINT), *args], check=False, capture_output=True, text=True)

    def edit(self, rel, old, new):
        path = self.site / rel
        text = path.read_text()
        self.assertIn(old, text)
        path.write_text(text.replace(old, new, 1))

    def findings(self, rule):
        result = self.run_lint('--rule', rule)
        return [line for line in result.stdout.splitlines() if line.startswith('ERROR')]

    def test_valid_fixture_exits_zero(self):
        result = self.run_lint()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn('0 errors', result.stdout)

    def test_unknown_rule_and_missing_site_fail(self):
        self.assertNotEqual(self.run_lint('--rule', 'nope').returncode, 0)
        result = subprocess.run([sys.executable, '-B', str(LINT), str(self.site / 'missing'), '--ns', 'site'], check=False)
        self.assertNotEqual(result.returncode, 0)

    def test_html_balance_unclosed_tag_fires(self):
        self.edit('section/one.html', '<h1>One</h1>', '<section><h1>One</h1>')
        (finding,) = self.findings('html-balance')
        self.assertIn('section/one.html', finding)
        self.assertIn('unclosed <section>', finding)

    def test_html_balance_stray_end_tag_fires(self):
        self.edit('section/one.html', '<h1>One</h1>', '<h1>One</h1></span>')
        (finding,) = self.findings('html-balance')
        self.assertIn('unexpected </span>', finding)

    def test_html_balance_reports_one_finding_per_page(self):
        self.edit('section/one.html', '<h1>One</h1>', '<div><span><h1>One</h1>')
        self.edit('section/one.html', '</div>\n</body>', '<b></div>\n</body>')
        self.assertEqual(len(self.findings('html-balance')), 1)

    def test_html_balance_optional_end_tags_pass(self):
        self.assertEqual(self.findings('html-balance'), [])

    def test_title_h1_missing_h1_fires(self):
        self.edit('section/one.html', '<h1>One</h1>', '<h2>One</h2>')
        (finding,) = self.findings('title-h1')
        self.assertIn('missing <h1>', finding)

    def test_title_h1_missing_title_fires(self):
        self.edit('section/one.html', '<title>One — Section</title>', '<title> </title>')
        (finding,) = self.findings('title-h1')
        self.assertIn('missing or empty <title>', finding)

    def test_title_may_differ_from_h1_and_stub_is_exempt(self):
        self.assertEqual(self.findings('title-h1'), [])

    def test_links_missing_page_fires(self):
        self.edit('section/one.html', 'href="./"', 'href="gone.html"')
        (finding,) = self.findings('links')
        self.assertIn('gone.html → no such page', finding)

    def test_links_missing_file_fires(self):
        self.edit('section/one.html', 'href="../assets/hub.css"', 'href="../assets/nope.css"')
        self.assertIn('no such file', self.findings('links')[0])

    def test_links_missing_fragment_fires(self):
        self.edit('index.html', 'two.html#details', 'two.html#missing')
        (finding,) = self.findings('links')
        self.assertIn('no id "missing"', finding)

    def test_links_fragment_on_shared_page_checked(self):
        self.edit('index.html', 'note.html#n1', 'note.html#n2')
        self.assertIn('no id "n2"', self.findings('links')[0])

    def test_links_empty_href_fires(self):
        self.edit('section/one.html', 'href="./"', 'href=""')
        (finding,) = self.findings('links')
        self.assertIn('empty URL', finding)

    def test_links_malformed_url_fires(self):
        self.edit('section/one.html', 'href="./"', 'href="http://[bad"')
        (finding,) = self.findings('links')
        self.assertIn('malformed URL', finding)

    def test_links_root_relative_resolves_against_www(self):
        self.assertEqual(self.findings('links'), [])
        self.edit('index.html', 'href="/shared/note.html"', 'href="/shared/gone.html"')
        (finding,) = self.findings('links')
        self.assertIn('/shared/gone.html → no such page', finding)

    def test_links_root_relative_without_www_dir_fires(self):
        shutil.rmtree(self.www)
        self.assertTrue(any('--www' in line for line in self.findings('links')))

    def test_links_non_static_prefix_and_external_are_skipped(self):
        self.edit('index.html', '/priv/inbox', '/auth/login')
        self.assertEqual(self.findings('links'), [])

    def test_css_class_undefined_fires(self):
        self.edit('section/one.html', 'class="hub-container"', 'class="hub-container ghost"')
        (finding,) = self.findings('css-class')
        self.assertIn('undefined class ghost', finding)

    def test_css_class_reported_once_per_page(self):
        self.edit('section/one.html', '<p>Back', '<p class="ghost">a</p><p class="ghost">Back')
        self.assertEqual(len(self.findings('css-class')), 1)

    def test_css_class_inline_style_defines(self):
        self.edit('section/index.html', '.layout-only { display: grid; }', '')
        (finding,) = self.findings('css-class')
        self.assertIn('undefined class layout-only', finding)

    def test_css_class_js_string_defines(self):
        self.edit('index.html', 'class="hub-container theme-accent"', 'class="hub-container theme-accent arrow"')
        self.assertEqual(self.findings('css-class'), [])
        self.edit('assets/hub.js', "'arrow'", "'other'")
        (finding,) = self.findings('css-class')
        self.assertIn('undefined class arrow', finding)

    def test_css_class_root_relative_stylesheet_defines(self):
        self.edit('index.html', '<link rel="stylesheet" href="/pub/shared/theme.css">', '')
        (finding,) = self.findings('css-class')
        self.assertIn('undefined class theme-accent', finding)

    def test_css_class_tailwind_page_is_exempt(self):
        self.assertEqual(self.findings('css-class'), [])
        self.edit('tailwind.html', '<script src="https://cdn.tailwindcss.com"></script>', '')
        self.assertIn('undefined class flex', ' '.join(self.findings('css-class')))


if __name__ == '__main__':
    unittest.main()
