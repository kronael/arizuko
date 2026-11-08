import importlib.util
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1] / "skills"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TerrainTests(unittest.TestCase):
    def test_missing_elevation_is_unknown(self):
        terrain = load("terrain", ROOT / "nemo/scripts/terrain_view.py")
        with patch.object(terrain, "elev_batch", return_value=[None] * 49), patch.object(terrain, "overpass", return_value={"err": "unavailable"}):
            result = terrain.analyze(50, 14)
        self.assertEqual(result["error"], "elevation unknown")
        self.assertEqual(result["ov"]["err"], "unavailable")


class FinanceTests(unittest.TestCase):
    def test_rate_is_required(self):
        try:
            import numpy_financial  # noqa: F401
        except ImportError:
            self.skipTest("numpy-financial is not installed")
        result = subprocess.run([sys.executable, str(ROOT / "finance/fin.py"), "splatka", "100000", "10"], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("sazba je povinná", result.stderr)

    def test_explicit_zero_rate(self):
        try:
            import numpy_financial  # noqa: F401
        except ImportError:
            self.skipTest("numpy-financial is not installed")
        finance = load("finance", ROOT / "finance/fin.py")
        self.assertAlmostEqual(finance.anu(120000, 0, 120), 1000)


class PastureTests(unittest.TestCase):
    def setUp(self):
        try:
            import requests  # noqa: F401
            import shapely  # noqa: F401
        except ImportError:
            self.skipTest("requests and shapely are not installed")
        self.pasture = load("pasture", ROOT / "parcel-open-data/scripts/pasture_expand.py")

    def test_output_path_is_required(self):
        with patch.object(sys, "argv", ["pasture", "--id", "1", "--gps", "50,14", "--ku", "1", "--subject", "1"]):
            with self.assertRaises(SystemExit) as error:
                self.pasture.main()
        self.assertNotEqual(error.exception.code, 0)

    def test_missing_subject_parcel_stops_before_output(self):
        from shapely.geometry import box

        parcel = {"ku": 1, "typ": 2, "label": "1", "geom": box(14, 50, 14.001, 50.001)}
        args = ["pasture", "--id", "1", "--gps", "50,14", "--ku", "1", "--subject", "1,2", "--out", "/unused/result"]
        with patch.object(sys, "argv", args), patch.object(self.pasture, "ruian_parcels", return_value=[parcel]), patch.object(self.pasture.os, "makedirs"):
            with self.assertRaisesRegex(SystemExit, "subject parcels not found"):
                self.pasture.main()

    def test_all_subject_parcels_form_the_boundary(self):
        from shapely.geometry import box
        from shapely.ops import unary_union

        parcels = [
            {"ku": 1, "typ": 2, "label": str(i), "id": i, "geom": box(14 + i * 0.001, 50, 14.001 + i * 0.001, 50.001)}
            for i in (1, 2)
        ]
        args = ["pasture", "--id", "1", "--gps", "50,14", "--ku", "1", "--subject", "1,2", "--out", "/unused/result"]
        with patch.object(sys, "argv", args), patch.object(self.pasture, "ruian_parcels", return_value=parcels), patch.object(self.pasture.os, "makedirs"), patch.object(self.pasture, "ku_context", side_effect=RuntimeError("stop")), patch.object(self.pasture, "unary_union", wraps=unary_union) as union:
            with self.assertRaisesRegex(RuntimeError, "stop"):
                self.pasture.main()
        self.assertEqual(len(union.call_args.args[0]), 2)

    def test_bad_lpis_response_is_unknown(self):
        response = unittest.mock.Mock(content=b"not xml")
        with patch.object(self.pasture, "get", return_value=response):
            self.assertIsNone(self.pasture.lpis_user(50, 14))


if __name__ == "__main__":
    unittest.main()
