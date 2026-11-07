---
name: katastr
description: Inspect one Czech cadastral property in CUZK. NOT for bulk downloads.
when_to_use: 'katastr, kadastr, ČÚZK, CUZK, parcela, list vlastnictví, LV, vlastník, zástavní právo'
---

# Katastr

ALWAYS run the command inside the group's QEMU guest:
`python3 ~/.claude/skills/katastr/katastr.py <command>`.
ALWAYS use the package's [transport setup](../../README.md#transport) for browser access.
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
NEVER repeat a failed action blindly; ALWAYS inspect the current page first.
ALWAYS stop on 429 or 503 and record Retry-After with `cooldown <UTC-RFC3339>`; use at least 60 seconds when absent.
The CLI does not expose every response header or limit redirects and subresources.
NEVER change identity to pass a challenge; ALWAYS keep the existing browser identity.
ALWAYS stop for the operator at a login.

## Authorized CAPTCHA solver

ALWAYS use `solve_captcha.py` only under the [operator authorization](../../README.md#the-operator-holds-authorization).
The offline solver requires NumPy and Pillow. It neither acquires tiles nor clicks the browser.
ALWAYS save the current challenge's nine tiles as `tile0.jpg` through `tile8.jpg` in one directory.
ALWAYS number tiles in row-major order, left to right and top to bottom.
ALWAYS run the solver inside the group's QEMU guest:

```sh
python3 ~/.claude/skills/katastr/solve_captcha.py TILE_DIRECTORY
```

If NumPy or Pillow is unavailable, ALWAYS run:

```sh
uv run --with numpy --with pillow python ~/.claude/skills/katastr/solve_captcha.py TILE_DIRECTORY
```

The solver prints clockwise quarter-turn counts for tiles 0–8 and writes `solution.png` into `TILE_DIRECTORY`.
ALWAYS apply the printed click counts to the corresponding tiles in the existing `katastr` browser session.
ALWAYS verify the real page result after submitting the challenge.
NEVER treat the montage as proof of success; ALWAYS confirm that the requested cadastral details are visible.

## Property evidence

ALWAYS select Parcela or Podle LV and verify the territory and parcel number on the detail page.
ALWAYS separate visible parcel type, area, LV number and BPEJ from ownership and rights.
ALWAYS inspect list vlastnictví for owners and section C for liens, easements and enforcement entries.
NEVER treat a hidden section as empty or clear title; ALWAYS report it as unverified.
ALWAYS preserve displayed fields, source URL and UTC observation time in the group's research record.
