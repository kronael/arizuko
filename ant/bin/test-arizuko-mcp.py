#!/usr/bin/env python3
"""Smoke test for arizuko-mcp CLI.

Spawns a mock MCP server on a temp unix socket, invokes the CLI against
it, and asserts the tool call landed with the expected JSON.
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import threading

HERE = os.path.dirname(os.path.abspath(__file__))
CLI = os.path.join(HERE, "arizuko-mcp")


class MockServer:
    def __init__(self, sock_path):
        self.sock_path = sock_path
        self.calls = []
        self.srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.srv.bind(sock_path)
        self.srv.listen(1)
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()

    def _serve(self):
        try:
            conn, _ = self.srv.accept()
        except Exception:
            return
        rf = conn.makefile("r", encoding="utf-8", newline="\n")
        wf = conn.makefile("w", encoding="utf-8", newline="\n")

        def send(obj):
            wf.write(json.dumps(obj) + "\n")
            wf.flush()

        try:
            while True:
                line = rf.readline()
                if not line:
                    break
                req = json.loads(line)
                method = req.get("method")
                rid = req.get("id")
                if method == "initialize":
                    send(
                        {
                            "jsonrpc": "2.0",
                            "id": rid,
                            "result": {
                                "protocolVersion": "2024-11-05",
                                "capabilities": {"tools": {}},
                                "serverInfo": {
                                    "name": "mock",
                                    "version": "0",
                                },
                            },
                        }
                    )
                elif method == "notifications/initialized":
                    pass
                elif method == "tools/list":
                    send(
                        {
                            "jsonrpc": "2.0",
                            "id": rid,
                            "result": {
                                "tools": [
                                    {
                                        "name": "send_message",
                                        "description": "send",
                                    }
                                ]
                            },
                        }
                    )
                elif method == "tools/call":
                    self.calls.append(req.get("params", {}))
                    send(
                        {
                            "jsonrpc": "2.0",
                            "id": rid,
                            "result": {
                                "content": [
                                    {"type": "text", "text": "ok"}
                                ]
                            },
                        }
                    )
                else:
                    send(
                        {
                            "jsonrpc": "2.0",
                            "id": rid,
                            "error": {
                                "code": -32601,
                                "message": "method not found",
                            },
                        }
                    )
        finally:
            try:
                conn.close()
            except Exception:
                pass

    def stop(self):
        try:
            self.srv.close()
        except Exception:
            pass


def run(tmpdir, sock, args):
    return subprocess.run(
        [sys.executable, CLI, "--socket", sock, *args],
        capture_output=True,
        text=True,
        timeout=10,
    )


def main():
    tmpdir = tempfile.mkdtemp(prefix="arizuko-mcp-test-")
    sock = os.path.join(tmpdir, "gated.sock")

    # Test 1: send_message
    ms = MockServer(sock)
    r = run(tmpdir, sock, ["message", "test@jid", "hello"])
    ms.stop()
    assert r.returncode == 0, f"exit {r.returncode}: {r.stderr}"
    assert "ok" in r.stdout, f"stdout: {r.stdout!r}"
    assert ms.calls, "no tool call received"
    c = ms.calls[0]
    assert c["name"] == "send_message", c
    assert c["arguments"]["chatJid"] == "test@jid"
    assert c["arguments"]["text"] == "hello"
    print("test 1 (message): ok")

    # Test 2: send_file with caption
    os.remove(sock)
    ms = MockServer(sock)
    r = run(
        tmpdir,
        sock,
        ["file", "test@jid", "/home/node/foo.pdf", "caption text"],
    )
    ms.stop()
    assert r.returncode == 0, f"exit {r.returncode}: {r.stderr}"
    c = ms.calls[0]
    assert c["name"] == "send_file", c
    assert c["arguments"]["filepath"] == "/home/node/foo.pdf"
    assert c["arguments"]["caption"] == "caption text"
    print("test 2 (file): ok")

    # Test 3: tools listing
    os.remove(sock)
    ms = MockServer(sock)
    r = run(tmpdir, sock, ["tools"])
    ms.stop()
    assert r.returncode == 0, f"exit {r.returncode}: {r.stderr}"
    assert "send_message" in r.stdout
    print("test 3 (tools): ok")

    # Test 4: missing socket
    os.remove(sock)
    r = run(tmpdir, sock, ["message", "x", "y"])
    assert r.returncode == 2, f"expected exit 2, got {r.returncode}"
    assert "not found" in r.stderr
    print("test 4 (missing sock): ok")

    print("\nall smoke tests passed")


if __name__ == "__main__":
    main()
