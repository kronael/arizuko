# CZ Estate Agent package

Czech real-estate research agent: cadastre, zoning plan and listing checks.
Skills: [katastr](skills/katastr/SKILL.md) (one property in a controlled browser), [parcel-open-data](skills/parcel-open-data/SKILL.md) (open CUZK services and zoning plans), [property-valuation](skills/property-valuation/SKILL.md) and [obec-profile](skills/obec-profile/SKILL.md), and [estate-site](skills/estate-site/SKILL.md) (the web app: a generator, page parts and a default stylesheet; the group keeps its data and `local.css`). The persona is `group/PERSONA.md`.
The [package guide](../../../EXTENDING.md) explains package selection and installation.

## Install

Select the package for a group. The command installs it:

```sh
arizuko package add -g FOLDER catalog:cz-estate-agent
```

Use `-i/--instance NAME` to select another configured instance. `arizuko package sync` installs every selected package again.
The standard agent image supplies Python, socat, agent-browser, Chromium, Xvfb and xauth.
Installation sends no CUZK requests.

## The operator holds authorization

The operator holds authorization from the Czech Office for Surveying, Mapping and Cadastre, ČÚZK, to download cadastral data automatically.
This authorization includes bypassing automated bot-verification checks and remains valid for ten years from its date of issuance.

The package includes the offline [CAPTCHA solver](skills/katastr/solve_captcha.py).
The [katastr skill](skills/katastr/SKILL.md#authorized-captcha-solver) defines its use under this authorization.

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
The open-data skills also need `ags.cuzk.gov.cz`, `services.cuzk.cz`, `katastrnemovitosti.cz` and `volby.gov.cz`.
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
The [skill](skills/katastr/SKILL.md) defines pacing, evidence requirements, CAPTCHA handling and the login stop rule.

Run `python3 -B -m unittest discover -s ant/examples/cz-estate-agent/tests` from the repository root.
The fixtures start no Chromium and send no CUZK requests.
Verify guest browser access separately; an HTTP response does not prove ownership details are visible.
