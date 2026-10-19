# CUZK example

A group addon for one-property cadastral checks. [The skill](skills/katastr/SKILL.md) defines the workflow and access limits.
Chromium, agent-browser, the command and browser profile run inside QEMU. Installation sends no CUZK requests.

## Install

The guest needs Python 3, socat, agent-browser and Chromium; headed mode also needs Xvfb and xauth.
The optional Dockerfile.ant extends an immutable image supplied as `ARIZUKO_ANT_BASE`.
Product apply copies the extension; the operator coordinates its image and rootfs delivery separately.

Preserve the existing katastr skill, products.toml, quota history and browser state before adoption.
Add this block to the group's products.toml; the source resolves under HOST_APP_DIR:

```toml
[[product]]
source = "examples/cuzk"
```

```bash
arizuko products <instance> apply <folder>
```

## Transport

The host must already supply SOCKS at `127.0.0.1:1080`.
Install `transport/cuzk-socks@.socket` and `.service` into `/etc/systemd/system/`.
Choose an unused port; these commands use 1081:

```bash
systemctl daemon-reload
systemctl enable --now cuzk-socks@1081.socket
```

The socket binds only `172.17.0.1`; restrict its source network with host firewall policy.
Existing group egress must permit `host.docker.internal`. The transport carries arbitrary SOCKS targets and does not authenticate individual groups.
The guest command chains through its existing `HTTP_PROXY`. Set `KATASTR_RELAY_PORT` if the host port differs from 1081.
Set `KATASTR_HEADED=1` to start guest Xvfb with TCP disabled; the default is headless.

## State and verification

The ledger and profile persist under `~/tmp/katastr`; `KATASTR_STATE` overrides that directory.
Before any open, import sanitized quota state containing RFC3339 `opens` and `next_allowed`; omitted `opens` means zero attempts.
Import refuses an existing ledger. Do not include credentials, cookies or cached property facts.

```bash
python3 ~/.claude/skills/katastr/katastr.py import-state <quota-file>
python3 ~/.claude/skills/katastr/katastr.py status
python3 ~/.claude/skills/katastr/katastr.py open https://nahlizenidokn.cuzk.gov.cz/
python3 ~/.claude/skills/katastr/katastr.py snapshot
```

For manual coordinate input inside the guest, export `AGENT_BROWSER_SOCKET_DIR=/tmp/katastr`.
Use `agent-browser --session katastr mouse move <x> <y>`, then `mouse down` and `mouse up` with the same session flag.
Run `python3 -B -m unittest discover -s examples/cuzk/tests` from the source root; fixtures start no Chromium.
Separately verify guest processes, preserved quota usage and one permitted metadata observation inside the actual QEMU guest.
Stop on challenges; hidden owners and rights remain unverified.

Rollback restores the saved skill and products.toml; the CLI does not implement whole-product uninstall.
Disable unused transport with `systemctl disable --now cuzk-socks@1081.socket`.
