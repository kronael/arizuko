import os
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
        shutil.copytree(FIXTURES / 'www', base / 'www')
        shutil.copytree(FIXTURES / 'site', base / 'www/site')
        self.site = base / 'www/site'
        self.www = base / 'www'

    def tearDown(self):
        self.tmp.cleanup()

    def run_lint(self, *extra):
        args = [str(self.site), '--www', str(self.www), '--ns', 'site', *extra]
        return subprocess.run([sys.executable, '-B', str(LINT), *args], check=False, capture_output=True, text=True, timeout=20)

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
        result = self.run_lint('--rule', 'links', '--www', str(self.www / 'missing'))
        self.assertTrue(any('--www' in line for line in result.stdout.splitlines()))

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
        with (self.site / 'assets/hub.js').open('a') as js:
            js.write("document.body.classList.add('arrow');\n")
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

    def test_css_class_code_language_marker_is_not_undefined(self):
        self.edit('section/one.html', '<p>Back', '<pre><code class="language-python">x</code></pre><p>Back')
        self.assertEqual(self.findings('css-class'), [])

    def test_css_class_page_with_remote_stylesheet_is_exempt(self):
        self.edit('section/one.html', 'class="hub-container"', 'class="hub-container ghost"')
        self.assertEqual(len(self.findings('css-class')), 1)
        self.edit('section/one.html', '</head>', '<link rel="stylesheet" href="https://cdn.example.org/lib.css"></head>')
        self.assertEqual(self.findings('css-class'), [])

    def test_page_meta_bad_type_fires(self):
        self.edit('section/one.html', 'name="site:type" content="research"', 'name="site:type" content="Research1"')
        (finding,) = self.findings('page-meta')
        self.assertIn('section/one.html', finding)
        self.assertIn('site:type', finding)

    def test_page_meta_missing_type_fires(self):
        self.edit('section/one.html', '<meta name="site:type" content="research">\n', '')
        (finding,) = self.findings('page-meta')
        self.assertIn('site:type', finding)

    def test_page_meta_invalid_date_fires(self):
        self.edit('section/one.html', 'content="2026-02-01"', 'content="2026-02-30"')
        (finding,) = self.findings('page-meta')
        self.assertIn('site:date', finding)

    def test_page_meta_uses_namespace_and_exempts_stub(self):
        result = subprocess.run(
            [sys.executable, '-B', str(LINT), str(self.site), '--www', str(self.www), '--ns', 'other', '--rule', 'page-meta'],
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertIn('other:type missing', result.stdout)
        self.assertNotIn('moved.html', result.stdout)

    def test_redirect_stub_missing_target_fires(self):
        self.edit('moved.html', 'url=section/one.html', 'url=section/gone.html')
        self.edit('moved.html', 'href="section/one.html">', 'href="section/gone.html">')
        (finding,) = self.findings('redirect-stub')
        self.assertIn('moved.html', finding)
        self.assertIn('no such page', finding)

    def test_redirect_stub_body_link_mismatch_fires(self):
        self.edit('moved.html', '<a href="section/one.html">', '<a href="section/two.html">')
        (finding,) = self.findings('redirect-stub')
        self.assertIn('does not link the refresh target', finding)

    def test_meta_refresh_without_url_is_not_a_stub(self):
        self.edit('moved.html', 'content="0; url=section/one.html"', 'content="60"')
        self.assertEqual(self.findings('redirect-stub'), [])
        self.assertTrue(any('moved.html' in line and 'missing <h1>' in line for line in self.findings('title-h1')))

    def add_to_one(self, head='', body=''):
        self.edit('section/one.html', '</head>', head + '</head>')
        self.edit('section/one.html', '</div>\n</body>', body + '</div>\n</body>')

    def test_vhost_resolves_root_relative_links_like_the_server(self):
        self.add_to_one(body='<a href="/section/two.html">a</a><a href="/pub/section/two.html">b</a><a href="/shared/note.html">c</a>')
        plain = [line for line in self.findings('links') if line.startswith('ERROR section/one.html')]
        self.assertTrue(any('/section/two.html' in line for line in plain))
        self.assertFalse(any('/shared/note.html' in line for line in plain))
        out = self.run_lint('--rule', 'links', '--vhost', 'site').stdout.splitlines()
        vhost = [line for line in out if line.startswith('ERROR section/one.html')]
        self.assertEqual(len(vhost), 1, vhost)
        self.assertIn('/shared/note.html', vhost[0])

    def test_vhost_follows_a_stub_to_the_slot(self):
        self.edit('moved.html', 'url=section/one.html', 'url=/section/one.html')
        self.edit('moved.html', '<a href="section/one.html">', '<a href="/section/one.html">')
        result = self.run_lint('--rule', 'redirect-stub', '--vhost', 'site')
        self.assertEqual(result.returncode, 0, result.stdout)

    def test_vhost_must_be_a_folder_under_www(self):
        self.assertNotEqual(self.run_lint('--vhost', 'nope').returncode, 0)

    def test_css_class_skipped_when_a_local_stylesheet_does_not_resolve(self):
        self.add_to_one(head='<link rel="stylesheet" href="../assets/nope.css">', body='<p class="invented">x</p>')
        self.assertEqual(self.findings('css-class'), [])
        self.assertEqual(len(self.findings('links')), 1)

    def test_odd_files_do_not_crash_or_hang(self):
        os.symlink('missing-target.html', self.site / 'dangling.html')
        os.symlink('loop.html', self.site / 'loop.html')
        os.mkfifo(self.site / 'pipe.html')
        self.add_to_one(body=f'<a href="{"a" * 300}.html">long</a><a href="%00.html">nul</a>')
        result = self.run_lint()
        self.assertNotIn('Traceback', result.stderr)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(len(self.findings('links')), 2)

    def test_relative_link_outside_www_is_a_finding(self):
        self.add_to_one(body='<a href="../../../../../../../../etc/hostname">x</a><a href="../../../../../../../../etc/x.html#a">y</a>')
        found = self.findings('links')
        self.assertEqual(len(found), 2, found)
        self.assertTrue(all('leaves --www' in line for line in found))

    def test_relative_link_outside_site_without_www_is_a_finding(self):
        self.add_to_one(body='<a href="../../shared/note.html#n1">x</a>')
        args = [str(self.site), '--ns', 'site', '--rule', 'links']
        out = subprocess.run([sys.executable, '-B', str(LINT), *args], check=False, capture_output=True, text=True, timeout=20).stdout
        self.assertIn('../../shared/note.html#n1 leaves the site', out)

    def test_textarea_and_title_content_is_raw_text(self):
        self.add_to_one(body='<textarea><div></textarea>')
        self.edit('section/one.html', '<title>One — Section</title>', '<title>One <b> Section</title>')
        self.assertEqual(self.findings('html-balance'), [])
        self.assertEqual(self.findings('title-h1'), [])

    def test_css_class_escaped_attribute_and_template_literal_definitions(self):
        self.add_to_one(
            head='<style>.md\\:flex{} [class~=attrsel]{}</style>',
            body='<script>const c = `${on ? \'tpl\' : \'off\'}`</script><p class="md:flex attrsel tpl off">x</p>',
        )
        self.assertEqual(self.findings('css-class'), [])

    def test_redirect_stub_to_itself_or_each_other_fires(self):
        for name, target in (('self.html', 'self.html'), ('g1.html', 'g2.html'), ('g2.html', 'g1.html')):
            (self.site / name).write_text(
                f'<!doctype html><meta http-equiv="refresh" content="0; url={target}"><a href="{target}">go</a>')
        found = self.findings('redirect-stub')
        self.assertEqual(len(found), 3, found)
        self.assertTrue(all('leads back to this page' in line for line in found))

    def test_htm_and_upper_case_html_pages_are_scanned(self):
        (self.site / 'A.HTML').write_text('<!doctype html><title>t</title><h1>t</h1><div>')
        (self.site / 'b.htm').write_text('<!doctype html><title>t</title><h1>t</h1><div>')
        found = self.findings('html-balance')
        self.assertEqual(len(found), 2, found)

    def test_closing_tags_do_not_cost_quadratic_time(self):
        (self.site / 'deep.html').write_text('<!doctype html><title>t</title><h1>t</h1>' + '<b>' * 20000 + '</i>' * 20000)
        result = self.run_lint('--rule', 'html-balance')
        self.assertEqual(len(self.findings('html-balance')), 1, result.stdout)


if __name__ == '__main__':
    unittest.main()
