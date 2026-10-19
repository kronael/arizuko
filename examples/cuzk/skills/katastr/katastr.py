#!/usr/bin/env python3
import argparse
import datetime
import fcntl
import json
import os
import secrets
import socket
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit

LIMITS = [(60, 2), (3600, 30), (86400, 100)]


def timestamp(value):
    if isinstance(value, (int, float)):
        return float(value)
    return datetime.datetime.fromisoformat(value).timestamp()


def read_state(path):
    data = json.loads(path.read_text())
    return {
        "opens": sorted(timestamp(value) for value in data.get("opens", [])),
        "next_allowed": timestamp(data.get("next_allowed", 0)),
    }


def save_state(path, data):
    pending = path.with_suffix(".pending")
    with pending.open("w") as stream:
        json.dump(data, stream)
        stream.flush()
        os.fsync(stream.fileno())
    pending.replace(path)
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def next_open(data, now):
    allowed = max(now, data["next_allowed"])
    for seconds, limit in LIMITS:
        recent = [opened for opened in data["opens"] if opened > now - seconds]
        if len(recent) >= limit:
            allowed = max(allowed, recent[-limit] + seconds)
    return allowed


def start_relay(runtime, port, upstream):
    proxy = urlsplit(upstream)
    if proxy.scheme != "http" or not proxy.hostname or not proxy.port:
        raise ValueError("HTTP_PROXY must contain an http URL with an explicit port")
    if proxy.username or proxy.password:
        raise ValueError("Authenticated HTTP_PROXY is not supported")
    listener = f"TCP4-LISTEN:{port},bind=127.0.0.1,reuseaddr,fork"
    target = (
        f"PROXY:{proxy.hostname}:host.docker.internal:"
        f"{int(os.environ.get('KATASTR_RELAY_PORT', '1081'))},"
        f"proxyport={proxy.port}"
    )
    pidfile = runtime / "relay.pid"
    if pidfile.exists():
        pid = int(pidfile.read_text())
        command = Path(f"/proc/{pid}/cmdline")
        if command.exists():
            arguments = command.read_bytes().split(b"\0")
            if target.encode() in arguments and listener.encode() in arguments:
                return
    process = subprocess.Popen(
        ["socat", listener, target],
        start_new_session=True,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    for _ in range(50):
        if process.poll() is not None:
            raise RuntimeError(
                "socat exited; check the guest relay port and HTTP_PROXY"
            )
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                pidfile.write_text(str(process.pid))
                return
        except OSError:
            time.sleep(0.02)
    process.terminate()
    process.wait()
    raise RuntimeError("Guest SOCKS relay did not start")


def start_display(runtime):
    display = ":99"
    authority = runtime / "Xauthority"
    if not Path("/tmp/.X11-unix/X99").exists():
        subprocess.run(
            ["xauth", "-f", str(authority), "add", display, ".", secrets.token_hex(16)],
            check=True,
        )
        process = subprocess.Popen(
            [
                "Xvfb",
                display,
                "-screen",
                "0",
                "1280x900x24",
                "-nolisten",
                "tcp",
                "-auth",
                str(authority),
            ],
            start_new_session=True,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        for _ in range(50):
            if process.poll() is not None:
                raise RuntimeError("Guest Xvfb exited")
            if Path("/tmp/.X11-unix/X99").exists():
                break
            time.sleep(0.02)
        else:
            process.terminate()
            process.wait()
            raise RuntimeError("Guest Xvfb did not start")
    os.environ["DISPLAY"] = display
    os.environ["XAUTHORITY"] = str(authority)


def run_agent_browser(args, state, runtime):
    os.environ["AGENT_BROWSER_SOCKET_DIR"] = str(runtime)
    port = 1081
    start_relay(runtime, port, os.environ["HTTP_PROXY"])
    command = [
        "agent-browser",
        "--session",
        "katastr",
        "--profile",
        str(state / "profile"),
        "--proxy",
        f"socks5://127.0.0.1:{port}",
    ]
    if os.environ.get("KATASTR_HEADED") == "1":
        start_display(runtime)
        command.append("--headed")
    command.extend(args)
    return subprocess.run(command, timeout=90, check=False).returncode


def main():
    parser = argparse.ArgumentParser(
        description="Guest CUZK browser. State: KATASTR_STATE (~/tmp/katastr). "
        "Transport: HTTP_PROXY, KATASTR_RELAY_PORT (private host port). "
        "Headed: KATASTR_HEADED=1 starts guest Xvfb.",
    )
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("status")
    commands.add_parser("snapshot")
    screenshot = commands.add_parser("screenshot")
    screenshot.add_argument("path")
    opening = commands.add_parser("open")
    opening.add_argument("url")
    click = commands.add_parser("click")
    click.add_argument("ref")
    for name in "fill", "select":
        control = commands.add_parser(name)
        control.add_argument("ref")
        control.add_argument("value")
    importing = commands.add_parser("import-state")
    importing.add_argument("path", type=Path)
    cooldown = commands.add_parser("cooldown")
    cooldown.add_argument("until", help="UTC RFC3339")
    args = parser.parse_args()
    os.umask(0o077)
    state = Path(os.environ.get("KATASTR_STATE", "~/tmp/katastr")).expanduser()
    state.mkdir(parents=True, exist_ok=True)
    with (state / "lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        ledger = state / "ledger.json"
        data = (
            read_state(ledger) if ledger.exists() else {"opens": [], "next_allowed": 0}
        )
        now = time.time()
        if args.command == "import-state":
            if ledger.exists():
                raise ValueError("Ledger exists; import requires a fresh destination")
            data = read_state(args.path)
            save_state(ledger, data)
        if args.command == "cooldown":
            data["next_allowed"] = max(data["next_allowed"], timestamp(args.until))
            save_state(ledger, data)
        allowed = next_open(data, now)
        if args.command in {"status", "import-state", "cooldown"}:
            print(
                json.dumps({**data, "next_open_allowed": allowed, "state": str(state)})
            )
            return 0
        if data["next_allowed"] > now:
            raise ValueError(f"Cooldown; next_allowed={data['next_allowed']}")
        if args.command == "open":
            url = urlsplit(args.url)
            if url.scheme != "https" or url.hostname != "nahlizenidokn.cuzk.gov.cz":
                raise ValueError("Open requires https://nahlizenidokn.cuzk.gov.cz/")
            if allowed > now:
                raise ValueError(f"Open limit; next_open_allowed={allowed}")
            data["opens"].append(now)
            save_state(ledger, data)
        runtime = Path("/tmp/katastr")
        runtime.mkdir(mode=0o700, exist_ok=True)
        browser_args = [args.command]
        for name in "url", "ref", "value", "path":
            if hasattr(args, name):
                browser_args.append(str(getattr(args, name)))
        return run_agent_browser(browser_args, state, runtime)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        sys.exit(2)
