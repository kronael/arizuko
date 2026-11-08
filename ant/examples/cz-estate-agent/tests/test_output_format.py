import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "skills/estate-site/scripts/check_format.py"


class FormatTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.site = self.root / "site"
        self.site.mkdir()
        self.db = self.root / "db.md"
        self.db.write_text("| 1 | Example | https://example.test/1 | 100 | 200 | 30 | 2026-01-01 | 2026-01-01 | active |\n")
        self.cfg = self.root / "site.json"
        self.cfg.write_text(json.dumps({"site": str(self.site), "db": str(self.db), "labels": {"valuations": "My valuations"}}))
        self.val = self.site / "valuation.html"
        self.val.write_text('<meta name="listing" content="1"><h1>Valuation</h1>')
        self.index = self.site / "index.html"
        self.good = '<div class="item"><a href="https://example.test/1">Example</a><div class="meta zel">200 m² · 🟢 30 min · ID 1 · <a href="valuation.html">valuation</a></div></div>'
        self.index.write_text(self.good)

    def tearDown(self):
        self.tmp.cleanup()

    def run_check(self, *paths, config=True):
        return subprocess.run([sys.executable, "-B", str(SCRIPT), *(["--config", str(self.cfg)] if config else []), *(str(p) for p in paths)], text=True, capture_output=True)

    def test_valid_multiple_files(self):
        result = self.run_check(self.index, self.val)
        self.assertEqual((result.returncode, result.stdout), (0, "OK\n"))

    def test_n1_rejects_prose_and_fake_numeric(self):
        for table in ('<table><tr><td>A</td><td>B</td><td>C</td></tr></table>', '<table><tr><td>A</td><td class="n">words</td></tr></table>', '<table class="stack"><tr><td>A</td><td class="n">1</td></tr></table>', '<table><tr><td>A</td><td class="n">1</td><td class="n">2</td><td class="n">3</td></tr></table>', '<table></table>'):
            self.val.write_text(table)
            result = self.run_check(self.val)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('N1:', result.stdout)

    def test_n1_accepts_numeric_formats(self):
        self.val.write_text('<table><tr><td>Price</td><td class="n">1 000–2 000 Kč</td><td class="n">12,5 %</td></tr></table>')
        self.assertEqual(self.run_check(self.val).returncode, 0)

    def test_n1_accepts_row_header_label(self):
        self.val.write_text('<table><tr><th>Item</th><th>Value</th></tr><tr><th>Price</th><td class="n">1 000 Kč</td></tr></table>')
        self.assertEqual(self.run_check(self.val).returncode, 0)

    def test_n2_negative_heading(self):
        self.val.write_text('<h2>  Co nežádat: omezení </h2>')
        result = self.run_check(self.val)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('N2:', result.stdout)

    def test_n3_wrong_card_and_standalone_section(self):
        for html in (self.good.replace('ID 1', 'ID 2'), '<h2>My valuations</h2>' + self.good, '<div class="item">ID 1 <a href="valuation.html">valuation</a></div>'):
            self.index.write_text(html)
            result = self.run_check(self.index)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('N3:', result.stdout)

    def test_inactive_listing_does_not_require_link(self):
        self.db.write_text(self.db.read_text().replace('active', 'prodáno'))
        self.index.write_text('<h1>Listings</h1>')
        self.assertEqual(self.run_check(self.index).returncode, 0)

    def test_n4_colour_dot_mismatch(self):
        self.index.write_text(self.good.replace('🟢', '🔴'))
        result = self.run_check(self.index)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('N4:', result.stdout)

    def test_missing_minutes_permits_no_dot(self):
        self.db.write_text(self.db.read_text().replace('| 30 |', '|  |'))
        self.index.write_text(self.good.replace('🟢 30 min · ', ''))
        self.assertEqual(self.run_check(self.index).returncode, 0)

    def test_missing_empty_and_multiple_inputs_fail(self):
        self.assertNotEqual(self.run_check(self.root / 'missing.html').returncode, 0)
        self.val.write_text('')
        self.assertNotEqual(self.run_check(self.index, self.val).returncode, 0)
        self.assertNotEqual(self.run_check(self.index, config=False).returncode, 0)
        self.assertNotEqual(self.run_check().returncode, 0)


if __name__ == '__main__':
    unittest.main()
