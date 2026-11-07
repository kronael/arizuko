import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

SCRIPT = Path(__file__).resolve().parents[1] / "skills/katastr/solve.py"
ROTATIONS = [1, 3, 2, 1, 0, 2, 3, 1, 0]
EXPECTED = [3, 1, 2, 3, 0, 2, 1, 3, 0]


class SolverTests(unittest.TestCase):
    def test_generated_tiles_and_cli_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            y, x = np.mgrid[:360, :360]
            pixels = (
                np.stack(
                    [
                        150 + 45 * np.sin(x / 48) + 25 * np.cos(y / 37),
                        140 + 40 * np.cos((x + y) / 55),
                        145 + 35 * np.sin(y / 43) + 20 * np.cos(x / 33),
                    ],
                    axis=-1,
                )
                .clip(0, 255)
                .astype(np.uint8)
            )
            image = Image.fromarray(pixels)
            draw = ImageDraw.Draw(image)
            for points, color in [
                ([(0, 40), (130, 170), (359, 250)], (25, 45, 90)),
                ([(50, 0), (190, 200), (280, 359)], (130, 35, 45)),
                ([(0, 300), (200, 80), (359, 120)], (30, 90, 40)),
            ]:
                draw.line(points, fill=color, width=5)
            for i, rotation in enumerate(ROTATIONS):
                row, col = divmod(i, 3)
                tile = image.crop(
                    (col * 120, row * 120, (col + 1) * 120, (row + 1) * 120)
                )
                bordered = Image.new("RGB", (140, 140), "black")
                bordered.paste(tile, (10, 10))
                bordered = bordered.rotate(-90 * rotation)
                bordered.save(root / f"tile{i}.png")
            result = root / "result.json"
            montage = root / "montage.png"
            command = [
                sys.executable,
                str(SCRIPT),
                str(root),
                "--trim",
                "10",
                "--save",
                str(montage),
                "--json",
                str(result),
                "--weights",
                "1,1,0.5,1",
                "--quiet",
            ]
            completed = subprocess.run(
                command, check=True, capture_output=True, text=True
            )
            self.assertEqual(completed.stdout, "")
            self.assertEqual(json.loads(result.read_text())["cw"], EXPECTED)
            self.assertTrue(montage.is_file())
            for i in range(9):
                with Image.open(root / f"tile{i}.png") as tile:
                    tile.save(root / f"tile{i}.jpg", quality=95)
                (root / f"tile{i}.png").unlink()
            completed = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    str(root),
                    "--trim",
                    "auto",
                    "--json",
                    str(result),
                ],
                check=True,
                capture_output=True,
                text=True,
            )
            self.assertIn("tile margins:", completed.stdout)
            self.assertEqual(json.loads(result.read_text())["cw"], EXPECTED)


if __name__ == "__main__":
    unittest.main()
