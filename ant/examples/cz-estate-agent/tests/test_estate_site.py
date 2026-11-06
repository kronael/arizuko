import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "skills/estate-site/scripts/gen_site.py"

DB = """| ID | Place | URL | Price | Area | Min | First | Verified | Status |
|---|---|---|---|---|---|---|---|---|
| 1 | Alpha <b> | https://example.test/1 | 5 000 000 Kč | 12000 | 30 | 2026-10-01 | 2026-10-02 | aktivní ★ |
| 2 | Beta | https://example.test/2 | 9 000 000 Kč | 3000 | 70 | 2026-10-05 | 2026-10-06 | nové |
| 3 | Gone | https://example.test/3 | 1 Kč | 1 | 1 | 2026-09-01 | 2026-09-02 | prodáno |
"""


def run(*args):
    return subprocess.run([sys.executable, str(SCRIPT), *args], check=True, capture_output=True, text=True).stdout


class SiteTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.site = root / "public_html"
        (root / "db.md").write_text(DB)
        self.cfg = root / "site.json"
        self.cfg.write_text(json.dumps({"db": str(root / "db.md"), "site": str(self.site), "title": "Test site"}))

    def tearDown(self):
        self.tmp.cleanup()

    def build(self):
        return run("--config", str(self.cfg), "build", "2026-11-06")

    def test_build_links_local_css_and_filters(self):
        self.build()
        page = (self.site / "index.html").read_text()
        self.assertLess(page.index('href="estate.css"'), page.index('href="local.css"'))
        self.assertIn("Alpha &lt;b&gt;", page)
        self.assertIn("Beta", page)
        self.assertNotIn("Gone", page)
        self.assertTrue((self.site / "estate.css").exists())

    def test_local_css_survives_build(self):
        self.build()
        (self.site / "local.css").write_text(":root{--accent:red}")
        self.build()
        self.assertEqual((self.site / "local.css").read_text(), ":root{--accent:red}")

    def test_new_page_listed_as_valuation(self):
        self.build()
        run("--config", str(self.cfg), "new", "oceneni/alpha", "Alpha", "--listing", "1")
        sub = (self.site / "oceneni/alpha.html").read_text()
        self.assertIn('href="../estate.css"', sub)
        self.assertIn('href="../local.css"', sub)
        self.build()
        self.assertIn('href="oceneni/alpha.html"', (self.site / "index.html").read_text())
        with self.assertRaises(subprocess.CalledProcessError):
            run("--config", str(self.cfg), "new", "oceneni/alpha", "Alpha")


if __name__ == "__main__":
    unittest.main()
