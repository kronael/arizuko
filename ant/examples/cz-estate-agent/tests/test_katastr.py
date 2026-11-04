import contextlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "skills/katastr/katastr.py"
SPEC = importlib.util.spec_from_file_location("katastr", SCRIPT)
katastr = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(katastr)


class QuotaTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name)
        self.environment = patch.dict(os.environ, {"KATASTR_STATE": str(self.state)})
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def command(self, *args, now=100000, result=0):
        with (
            patch.object(sys, "argv", [str(SCRIPT), *args]),
            patch.object(katastr.time, "time", return_value=now),
            patch.object(katastr, "run_agent_browser", return_value=result) as browser,
            contextlib.redirect_stdout(io.StringIO()),
        ):
            code = katastr.main()
        return code, browser

    def ledger(self):
        return json.loads((self.state / "ledger.json").read_text())

    def test_each_rolling_boundary(self):
        for seconds, limit in [(60, 2), (3600, 30), (86400, 100)]:
            with self.subTest(seconds=seconds):
                opens = [100000 - seconds + 1] * limit
                data = {"opens": opens, "next_allowed": 0}
                self.assertEqual(katastr.next_open(data, 100000), 100001)
                self.assertEqual(katastr.next_open(data, 100001), 100001)

    def test_failed_open_survives_restart_and_exhausts_minute(self):
        url = "https://nahlizenidokn.cuzk.gov.cz/"
        self.assertEqual(self.command("open", url, result=7)[0], 7)
        self.command("open", url, now=100001)
        with self.assertRaisesRegex(ValueError, "100060"):
            self.command("open", url, now=100002)
        self.assertEqual(self.ledger()["opens"], [100000, 100001])
        self.command("open", url, now=100060)
        self.assertEqual(len(self.ledger()["opens"]), 3)

    def test_reservation_precedes_dispatch_and_persistence_fails_closed(self):
        def observe(*args):
            self.assertEqual(self.ledger()["opens"], [100000])
            raise subprocess.TimeoutExpired("agent-browser", 90)

        with (
            patch.object(
                sys, "argv", [str(SCRIPT), "open", "https://nahlizenidokn.cuzk.gov.cz/"]
            ),
            patch.object(katastr.time, "time", return_value=100000),
            patch.object(katastr, "run_agent_browser", side_effect=observe),
            self.assertRaises(subprocess.TimeoutExpired),
        ):
            katastr.main()
        with (
            patch.object(katastr, "save_state", side_effect=OSError("full")),
            self.assertRaises(OSError),
        ):
            self.command("open", "https://nahlizenidokn.cuzk.gov.cz/")
        self.assertEqual(self.ledger()["opens"], [100000])

    def test_import_populated_history_cooldown_and_omitted_opens(self):
        source = self.state / "import.json"
        source.write_text(
            json.dumps(
                {
                    "opens": ["1970-01-02T03:46:40.000000000Z"] * 2,
                    "next_allowed": "1970-01-02T03:48:20Z",
                    "cache": {"private": "ignored"},
                }
            )
        )
        self.command("import-state", str(source))
        self.assertEqual(
            self.ledger(),
            {
                "opens": [100000, 100000],
                "next_allowed": 100100,
            },
        )
        self.assertEqual(katastr.next_open(self.ledger(), 100000), 100100)
        with self.assertRaisesRegex(ValueError, "100100"):
            self.command("open", "https://nahlizenidokn.cuzk.gov.cz/")
        with self.assertRaisesRegex(ValueError, "100100"):
            self.command("snapshot")
        with self.assertRaisesRegex(ValueError, "Ledger exists"):
            self.command("import-state", str(source))
        source.write_text('{"next_allowed":"1970-01-02T03:48:20Z"}')
        self.assertEqual(katastr.read_state(source)["opens"], [])

    def test_observation_keeps_history_and_cooldown_never_shortens(self):
        self.command("open", "https://nahlizenidokn.cuzk.gov.cz/")
        self.command("snapshot")
        self.command("screenshot", str(self.state / "page.png"))
        self.command("cooldown", "1970-01-02T03:48:20Z")
        self.command("cooldown", "1970-01-02T03:47:20Z")
        self.assertEqual(
            self.ledger(),
            {
                "opens": [100000],
                "next_allowed": 100100,
            },
        )

    def test_corrupt_state_and_foreign_open_do_not_dispatch(self):
        with self.assertRaises(ValueError):
            self.command("open", "https://example.com/")
        (self.state / "ledger.json").write_text("invalid")
        with self.assertRaises(ValueError):
            self.command("snapshot")

    def test_concurrent_processes_share_reservations(self):
        runner = (
            "import runpy,sys,time; "
            "m=runpy.run_path(sys.argv[1]); "
            "m['main'].__globals__['run_agent_browser']=lambda *a: time.sleep(.1) or 0; "
            "sys.argv=[sys.argv[1],'open','https://nahlizenidokn.cuzk.gov.cz/']; "
            "m['main']()"
        )
        processes = [
            subprocess.Popen(
                [sys.executable, "-c", runner, str(SCRIPT)],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            for _ in range(6)
        ]
        results = [process.communicate(timeout=15) for process in processes]
        self.assertEqual(sum(process.returncode == 0 for process in processes), 2)
        self.assertEqual(len(self.ledger()["opens"]), 2)
        self.assertEqual(sum(b"Open limit" in err for _, err in results), 4)

    def test_native_cli_dispatch_with_fake_executable(self):
        binary = self.state / "agent-browser"
        binary.write_text(
            "#!/usr/bin/env python3\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n"
        )
        binary.chmod(0o700)
        original = katastr.subprocess.run
        for args in [
            ["open", "https://nahlizenidokn.cuzk.gov.cz/"],
            ["snapshot"],
            ["screenshot", "/tmp/page.png"],
            ["click", "@e1"],
            ["fill", "@e2", "a b"],
            ["select", "@e3", "value"],
        ]:
            with (
                patch.dict(
                    os.environ,
                    {
                        "PATH": f"{self.state}:{os.environ['PATH']}",
                        "HTTP_PROXY": "http://127.0.0.1:1234",
                        "KATASTR_HEADED": "0",
                    },
                ),
                patch.object(katastr, "start_relay"),
                tempfile.TemporaryFile(mode="w+") as output,
            ):

                def capture(command, **kwargs):
                    self.assertEqual(
                        os.environ["AGENT_BROWSER_SOCKET_DIR"], str(self.state)
                    )
                    return original(command, stdout=output, **kwargs)

                with patch.object(katastr.subprocess, "run", side_effect=capture):
                    code = katastr.run_agent_browser(args, self.state, self.state)
                output.seek(0)
                dispatched = json.loads(output.read())
            self.assertEqual(code, 0)
            self.assertEqual(dispatched[-len(args) :], args)
            self.assertEqual(dispatched[:2], ["--session", "katastr"])
            self.assertIn(str(self.state / "profile"), dispatched)
            self.assertIn("socks5://127.0.0.1:1081", dispatched)


if __name__ == "__main__":
    unittest.main()
