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

## Authorization and ownership

The shipped [authorization memo](skills/katastr/AUTHORIZATION.md) records the operator's authorization details.
The [ownership procedure](skills/katastr/vlastnici.md) covers parcel lookup, visible checks and the offline [solver](skills/katastr/solve.py).

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
Headed Chromium under Xvfb is the default; Xvfb disables TCP access.
Set `KATASTR_HEADED=0` for headless debugging.

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

Use `katastr.py click REF`, `mouse move X Y`, `mouse down left` and `mouse up left` for browser input.
Use `katastr.py eval JS` or `ab COMMAND ...` for same-session inspection.
Navigation requires `open`; never use `eval` or `ab` to bypass pacing.
The [skill](skills/katastr/SKILL.md) defines pacing, evidence requirements, challenge handling and the login stop rule.

Run the local tests from the repository root:

```sh
uv run --with numpy --with pillow --with scipy python -B -X dev -W error -m unittest discover -s packages/cz-estate-agent/tests
```
The fixtures start no Chromium and send no CUZK requests.
Verify guest browser access separately; an HTTP response does not prove ownership details are visible.
