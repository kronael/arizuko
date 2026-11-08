---
name: estate-site
description: Build and publish the group's property web site from its listing table and valuation pages. NOT for the research itself (use property-valuation) or for other web pages (use web).
when_to_use: 'web, stránka, index, přehled nabídek, publikovat, vzhled, css, gen_site, přegenerovat'
---

# Estate site

The site is a generated index plus hand-written pages. The package owns the generator, the page parts and the default stylesheet. The group owns the data, the pages and the look.

## Files

| Path | Owner | Edit |
|---|---|---|
| `~/.claude/skills/estate-site/` (`scripts/gen_site.py`, `components/`) | package | NEVER |
| `~/public_html/estate.css` | package copy, rewritten on every build | NEVER |
| `~/public_html/local.css` | group, linked after `estate.css`, created empty once | YES: all look changes |
| `~/public_html/index.html` | generated | NEVER |
| `~/public_html/**/*.html` | group pages | YES |
| `~/estate/db.md` | group, listing table | YES |
| `~/estate/site.json` | group, optional settings | YES |

## Data

`db.md` has one row per listing:
`| ID | Place | URL | Price | Area m² | Minutes | First seen | Verified | Status |`.
A status with `staženo`, `prodáno`, `neaktivní` or `vyřazeno` removes the row from the site. `★` in the status marks a priority. `nové` marks a new row.

`site.json` holds only what differs from the defaults: `title`, `lang`, `intro`, `db`, `site`, `links` (cards under "more"), `limits` (the colour thresholds: `minutes_green`, `minutes_yellow`, `area_green`, `price_red`), `labels` (every fixed text, default Czech), and `criteria` (the group's listing research brief). The default `db` and `site` paths are in the table above. The listing workflow reads `criteria`; the renderer does not score it.

## Build

```sh
python3 ~/.claude/skills/estate-site/scripts/gen_site.py build
curl -sI "https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/" | head -1
```

ALWAYS build after every `db.md` or page change, and at the end of every scan. ALWAYS check that the URL returns 200 before you announce it.
NEVER edit `index.html`: the next build overwrites it. Change content in `db.md`, `site.json` or a page.

Before delivery, check the index and every changed HTML page:

```sh
python3 ~/.claude/skills/estate-site/scripts/check_format.py --config ~/estate/site.json ~/public_html/index.html ~/public_html/oceneni/place.html
```

The check covers numeric tables, headings, valuation placement and colour/dot consistency. It does not verify sources or replace a browser check.

## Pages

```sh
python3 ~/.claude/skills/estate-site/scripts/gen_site.py new oceneni/place "Place (ID 9)" --listing 9
```

`new` writes a skeleton that links `estate.css` and `local.css` with the right relative path, and refuses to overwrite a file. Fill the body from `components/parts.html`: the `.big` verdict, numeric tables and `.warn`.
A page with `<meta name="listing" content="ID">` links from its active listing card. The newest page for an ID wins.
Use numbered points for prose. Give numeric tables a `.tbl` wrapper, one label column and at most two numeric columns with class `n`.
ALWAYS check a new page at 390 px width: `document.documentElement.scrollWidth` must not exceed the viewport.

## Look

Change colours, width and type in `local.css`. The default sheet defines them as variables:

```css
:root { --accent: #8a2be2; --width: 840px; }
```

NEVER copy `estate.css` into a page or edit it. A package update rewrites it, and your tuning in `local.css` stays.
