---
name: katastr
description: Inspect one Czech cadastral property in CUZK. NOT for bulk downloads.
when_to_use: 'katastr, kadastr, ČÚZK, CUZK, parcela, list vlastnictví, LV, vlastník, zástavní právo'
---

# Katastr

ALWAYS run the command inside the group's QEMU guest:
`python3 ~/.claude/skills/katastr/katastr.py <command>`.
The wrapper starts the tunnel automatically through `HTTP_PROXY` and `KATASTR_RELAY_PORT`.
Headed Chromium under guest Xvfb is the default; `KATASTR_HEADED=0` enables headless debugging.
NEVER search for or use the Katastr API; ALWAYS use the controlled browser through the configured tunnel.
ALWAYS identify the cadastral territory and parcel number from the supplied source.
NEVER infer a parcel from its area; ALWAYS verify both identifiers on the detail page.
ALWAYS reuse an available detail page before another `open` of `https://nahlizenidokn.cuzk.gov.cz/`.

## Pacing and controls

ALWAYS call `status` before an `open` and wait until `next_open_allowed`.
Attempted opens count before dispatch: 2/minute, 30/hour and 100/day in rolling windows.
Failed opens and timeouts count. These operational limits cover explicit opens, not every HTTP request or malicious use.
ALWAYS use `snapshot` or `screenshot <path>` to observe without another open.
ALWAYS take fresh references before `click <ref>`, `fill <ref> <text>` or `select <ref> <value>`.
ALWAYS use `mouse <arguments...>`, `eval <js>` and `ab <command...>` through this wrapper for the same browser session.
`eval` accepts one quoted JavaScript expression. `ab` forwards browser commands without the `ab` prefix.
Empty `ab` and its `open`, `goto`, `navigate`, `back`, `forward`, `reload` and `pushstate` commands are refused.
NEVER navigate or evade pacing through `eval` or `ab`; ALWAYS use `status` and `open` for navigation.
NEVER repeat a failed action blindly; ALWAYS inspect the current page first.
ALWAYS stop on 429 or 503 and record Retry-After with `cooldown <UTC-RFC3339>`; use at least 60 seconds when absent.
The CLI does not expose every response header or limit redirects and subresources.
NEVER change identity to pass a challenge; ALWAYS keep the existing browser identity.
ALWAYS stop for the operator at a login.

## Solve

ALWAYS read the adjacent [authorization memo](AUTHORIZATION.md) before using `solve.py`.
ALWAYS follow the [ownership procedure](vlastnici.md) for capture, input and visible ownership evidence.
The canonical offline solver requires NumPy, Pillow and SciPy. It neither acquires tiles nor clicks the browser.
ALWAYS save nine tiles as `tile0.jpg` through `tile8.jpg`, or corresponding `.png` files, in one directory.
ALWAYS number tiles in row-major order, left to right and top to bottom.
ALWAYS run the solver inside the group's QEMU guest:

```sh
uv run --with numpy --with pillow --with scipy python ~/.claude/skills/katastr/solve.py TILE_DIRECTORY --save TILE_DIRECTORY/solution.png --json TILE_DIRECTORY/result.json
```

`--trim auto|N` selects automatic border trimming or an integer pixel width; the default is `auto`.
`--save PATH` writes an optional montage. `--json PATH` writes optional result data.
`--weights a,b,c,d` sets the colour, line, port and gradient weights; defaults are `1,1,0.5,1`.
`--quiet` suppresses printed output while retaining requested files.
The default output prints clockwise quarter-turn counts, margin, trim, gap and per-tile margins.
The margin separates the best and second-best scores; it is not a probability.
ALWAYS inspect uncertain results before input or ask the operator for help.
ALWAYS verify the real page result after submitting the check.
NEVER treat the montage as proof of ownership access; ALWAYS confirm that the requested cadastral fields are visible.

Alternative scripts accept a directory containing `tile0.jpg` through `tile8.jpg`.
They trim 12 pixels and require NumPy and Pillow; `colorline_iou.py` also requires SciPy.

| Script | Method | Output in tile directory |
| --- | --- | --- |
| [seam_ssd.py](solver/seam_ssd.py) | Squared RGB differences across edges | `solution2.png` |
| [pomeranz.py](solver/pomeranz.py) | Gradient prediction across edges | `solution3.png` |
| [colorline_iou.py](solver/colorline_iou.py) | Colour-weighted line overlap | `solution4.png` |

## Methods

ALWAYS read a method file only at the step that needs it:

- [vlastnici.md](vlastnici.md): ownership lookup and visible verification checks.
- `navazujici-parcely-wfs.md`: the exact parcel, its direct neighbours and the land type from ČÚZK WFS and RUIAN.
- `sousedni-parcely.md`: land type, zoning plan, official price and neighbours from the free katastrnemovitosti.cz map.
- `okolni-pozemky-scan.md`: nearby land for sale or lease from Bazoš and SPÚ.
- `per-parcel-db.md`: the method for one record per parcel, cadastre against zoning plan.

## Property evidence

ALWAYS select Parcela or Podle LV and verify the territory and parcel number on the detail page.
ALWAYS separate visible parcel type, area, LV number and BPEJ from ownership and rights.
ALWAYS inspect list vlastnictví for owners and section C for liens, easements and enforcement entries.
NEVER treat a hidden section as empty or clear title; ALWAYS report it as unverified.
ALWAYS preserve displayed fields, source URL and UTC observation time in the group's research record.
