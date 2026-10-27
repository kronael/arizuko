# CUZK package

Inspect one Czech cadastral property with the [katastr skill](skills/katastr/SKILL.md).
The [package guide](../../../EXTENDING.md) explains package selection and installation.

## Install

Add the source to the group's `arizuko.yml`, then run `arizuko sync`:

```yml
packages:
  - source: ant/examples/cuzk
```

Use `arizuko sync -i NAME` to select another configured instance.
The standard agent image supplies Python, socat, agent-browser, Chromium, Xvfb and xauth.
Installation sends no CUZK requests.

## Transport

The host must supply SOCKS at `127.0.0.1:1080`.
Install the two [transport units](transport/) into `/etc/systemd/system/`, then enable the socket:

```sh
systemctl daemon-reload
systemctl enable --now cuzk-socks@1081.socket
```

```text
┌─────────────────┐     ┌─────────────────┐     ┌──────────────────┐
│ Guest Chromium  ├────►│ Egress proxy    ├────►│ Host relay :1081 │
└─────────────────┘     └─────────────────┘     └───────┬──────────┘
                                                        │
                                               ┌────────▼─────────┐
                                               │ Host SOCKS :1080 │
                                               └──────────────────┘
```

The socket binds only `172.17.0.1`; host firewall policy must restrict its source network.
The group's egress grant must permit `host.docker.internal`.
The relay carries arbitrary SOCKS targets and does not authenticate groups.
Set `KATASTR_RELAY_PORT` when the relay uses another port.
Set `KATASTR_HEADED=1` for headed Chromium; Xvfb disables TCP access.

## State and verification

Quota history and the browser profile persist under `~/tmp/katastr`; `KATASTR_STATE` selects another directory.
Keep existing quota history and browser state when updating the package.
Import sanitized quota state only when no ledger exists; exclude credentials, cookies and cached property facts.

```sh
python3 ~/.claude/skills/katastr/katastr.py import-state QUOTA_FILE
python3 ~/.claude/skills/katastr/katastr.py status
python3 ~/.claude/skills/katastr/katastr.py open https://nahlizenidokn.cuzk.gov.cz/
python3 ~/.claude/skills/katastr/katastr.py snapshot
```

For manual browser input, set `AGENT_BROWSER_SOCKET_DIR=/tmp/katastr` and use `agent-browser --session katastr`.
The [skill](skills/katastr/SKILL.md) defines pacing, evidence requirements and the stop rule for challenges.

Run `python3 -B -m unittest discover -s ant/examples/cuzk/tests` from the repository root.
The fixtures start no Chromium and send no CUZK requests.
Verify guest browser access separately; an HTTP response does not prove ownership details are visible.
