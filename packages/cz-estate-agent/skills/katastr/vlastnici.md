# Read ownership from the visible cadastral page

Read the adjacent [authorization memo](AUTHORIZATION.md) for the operator's authorization details.
Use the [katastr skill](SKILL.md) for pacing, cooldown, identity and evidence rules.
The procedure uses the controlled browser and the offline [solver](solve.py).

```text
┌─────────────────┐   ┌─────────────────┐   ┌──────────────────┐
│ Current page    ├──►│ Local tile crop ├──►│ Offline solve.py │
└─────────────────┘   └─────────────────┘   └────────┬─────────┘
         ▲                                          │
         │            ┌─────────────────┐           │
         └────────────┤ Wrapper input   │◄──────────┘
                      └────────┬────────┘
                               ▼
                      ┌─────────────────┐
                      │ Visible owners  │
                      └─────────────────┘
```

## Keep one browser session

Run every browser command inside the group's QEMU guest:

```sh
python3 ~/.claude/skills/katastr/katastr.py status
python3 ~/.claude/skills/katastr/katastr.py open https://nahlizenidokn.cuzk.gov.cz/
python3 ~/.claude/skills/katastr/katastr.py snapshot
```

Before each `open`, wait until `next_open_allowed` from `status`.
The wrapper starts the configured tunnel and headed Chromium automatically.
Keep the persistent browser profile and quota ledger.
Use wrapper `mouse`, `eval` and `ab` commands for the existing browser session.
Use `eval` only for inspection and `ab` for non-navigation browser commands.
Never use either command to evade `open` pacing or cooldown.
On 429 or 503, stop and record `cooldown UTC-RFC3339` with Retry-After or at least 60 seconds.
At a login, stop for the operator.
If the tunnel fails, ask the operator to check the host relay.
An HTTP probe to a SOCKS port does not verify the tunnel.

## Inspect the visible verification controls

When a bot-verification screen appears, take a fresh `snapshot` and `screenshot PATH`.
Read the visible instruction before selecting images.
Use semantic `click REF` for checkboxes, image choices and verification buttons from the snapshot.
Refresh references after each page change.
Submit with the current visible control, then inspect the resulting page.
A redirect or an error page does not prove that the requested cadastral fields are accessible.
If another navigation is necessary, use `status` and wait before `open`.

## Verify the requested parcel

Follow the visible "Vyhledání parcely" link from the home page.
Use `fill REF VALUE`, `select REF VALUE` and `click REF` against fresh snapshot references.
Enter the cadastral territory and parcel identifiers from the user's source.
Choose the applicable parcel category and enter the main number and subdivision where present.
Verify the territory and full parcel number on "Informace o pozemku".
Read visible area, land type, LV and BPEJ separately from ownership and rights.

## Capture the current map grid

When ownership requires a rotating map check, open its visible control with `click REF`.
Save a fresh screenshot with `screenshot PATH`.
Inspect the screenshot to determine the nine tile bounds and centres.
Read bounds from the current image; banners and layout changes can move the grid.

Crop the screenshot locally with Pillow or an available image tool.
Supply each tile's actual left, top, right and bottom bounds from the screenshot.
Keep the full tile image, including its border; `solve.py` handles border trimming.
Save tiles in a new directory as `tile0.png` through `tile8.png`, or use `.jpg`.
Number tiles left to right, then top to bottom.
Do not mix tiles from different screenshots or retain tiles after the grid changes.

## Solve offline and inspect the result

```sh
uv run --with numpy --with pillow --with scipy python ~/.claude/skills/katastr/solve.py TILE_DIRECTORY --trim auto --save TILE_DIRECTORY/solution.png --json TILE_DIRECTORY/result.json
```

The printed `cw` list gives clockwise quarter-turn counts from zero to three for each tile.
The solver scores colour, line, port and gradient continuity across adjacent edges.
The margin is the best score minus the second-best score; it is not a probability or a success guarantee.
Inspect `solution.png`, the current screenshot and per-tile margins before applying uncertain rotations.
For weak or ambiguous results, inspect the crop or ask the operator to complete the visible check.
Do not use an unbounded reload loop.
The [solver reference](SKILL.md#solve) lists trim, output, weights and quiet options.

## Rotate through the wrapper and read visible owners

For each tile, use its centre from the current screenshot.
Repeat the following sequence exactly `cw[i]` times for tile `i`:

```sh
python3 ~/.claude/skills/katastr/katastr.py mouse move X Y
python3 ~/.claude/skills/katastr/katastr.py mouse down left
python3 ~/.claude/skills/katastr/katastr.py mouse up left
```

Each click applies one clockwise quarter-turn to the selected map tile.
Use semantic clicks for checkboxes; use the mouse sequence for map rotation.
Keep track of applied turns within the current grid.
Submit through the visible "Hotovo" control using a fresh reference.
Take a new snapshot and screenshot.
If the page reports an incorrect solution, inspect the current grid before another attempt.
Any new navigation still requires `status` and a permitted `open`.

Verify that "Vlastnické právo" and the requested owner fields are visible.
Read the displayed owners and shares from the page.
Inspect the LV and section C separately for liens, easements and enforcement entries.
Record displayed fields, source URL and UTC observation time in the group's research record.
Report hidden fields as unverified.
If the page remains inaccessible, ask the operator to complete the check or supply a visible LV extract.
