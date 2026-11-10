import importlib.util
import os
import signal
import socket
import socketserver
import tempfile
import threading
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "skills/katastr/katastr.py"
SPEC = importlib.util.spec_from_file_location("katastr", SCRIPT)
katastr = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(katastr)


class ConnectFixture(socketserver.StreamRequestHandler):
    def handle(self):
        first = self.rfile.readline()
        while self.rfile.readline() not in {b"\r\n", b"\n", b""}:
            pass
        self.server.connects.append(first)
        if first != b"CONNECT host.docker.internal:1081 HTTP/1.0\r\n":
            self.wfile.write(b"HTTP/1.0 403 Forbidden\r\n\r\n")
            return
        self.wfile.write(b"HTTP/1.0 200 Connection established\r\n\r\n")
        self.wfile.flush()
        greeting = self.rfile.read(3)
        if not greeting:
            return
        self.server.greetings.append(greeting)
        self.wfile.write(b"\x05\x00")
        self.wfile.flush()
        request = self.rfile.read(5)
        if request:
            target = self.rfile.read(request[-1] + 2)
            self.server.targets.append(request + target)
            self.wfile.write(b"\x05\x00\x00\x01\x7f\x00\x00\x01\x01\xbb")


class Proxy(socketserver.ThreadingTCPServer):
    daemon_threads = True


class TransportTests(unittest.TestCase):
    def test_real_socat_chains_connect_socks_hostname_and_reuses_process(self):
        with tempfile.TemporaryDirectory() as directory:
            runtime = Path(directory)
            with Proxy(("127.0.0.1", 0), ConnectFixture) as proxy:
                proxy.connects = []
                proxy.greetings = []
                proxy.targets = []
                thread = threading.Thread(target=proxy.serve_forever, daemon=True)
                thread.start()
                with socket.socket() as reservation:
                    reservation.bind(("127.0.0.1", 0))
                    port = reservation.getsockname()[1]
                upstream = f"http://127.0.0.1:{proxy.server_address[1]}"
                processes = []
                original = katastr.subprocess.Popen

                def capture(*args, **kwargs):
                    process = original(*args, **kwargs)
                    processes.append(process)
                    return process

                try:
                    with (
                        patch.dict(os.environ, {"KATASTR_RELAY_PORT": "1081"}),
                        patch.object(katastr.subprocess, "Popen", side_effect=capture),
                    ):
                        katastr.start_relay(runtime, port, upstream)
                        first_pid = (runtime / "relay.pid").read_text()
                        katastr.start_relay(runtime, port, upstream)
                        self.assertEqual((runtime / "relay.pid").read_text(), first_pid)
                        with socket.create_connection(("127.0.0.1", port)) as client:
                            client.settimeout(3)
                            client.sendall(b"\x05\x01\x00")
                            self.assertEqual(client.recv(2), b"\x05\x00")
                            hostname = b"nahlizenidokn.cuzk.gov.cz"
                            request = b"\x05\x01\x00\x03" + bytes([len(hostname)])
                            request += hostname + b"\x01\xbb"
                            client.sendall(request)
                            self.assertEqual(client.recv(10)[:2], b"\x05\x00")
                        self.assertEqual(proxy.greetings, [b"\x05\x01\x00"])
                        self.assertEqual(proxy.targets, [request])
                        self.assertTrue(proxy.connects)
                        self.assertTrue(
                            all(
                                line
                                == b"CONNECT host.docker.internal:1081 HTTP/1.0\r\n"
                                for line in proxy.connects
                            )
                        )
                finally:
                    if (runtime / "relay.pid").exists():
                        pid = int((runtime / "relay.pid").read_text())
                        os.killpg(pid, signal.SIGTERM)
                        processes[0].wait(timeout=3)
                    proxy.shutdown()
                    thread.join(timeout=3)

    def test_proxy_url_validation_precedes_process_creation(self):
        with (
            tempfile.TemporaryDirectory() as directory,
            patch.object(katastr.subprocess, "Popen") as process,
        ):
            for upstream in (
                "",
                "https://proxy:3128",
                "http://proxy",
                "http://u:p@proxy:1",
            ):
                with self.assertRaises(ValueError):
                    katastr.start_relay(Path(directory), 1081, upstream)
            process.assert_not_called()


if __name__ == "__main__":
    unittest.main()
