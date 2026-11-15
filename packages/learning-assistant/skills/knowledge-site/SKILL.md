---
name: knowledge-site
description: Publish, check and organize the group's knowledge site in `~/public_html/` — pages, hub pages, moves, the archive. NOT for answering or researching a question (use research), a standalone app or dashboard (use web) or a one-page research hub outside the site (use hub).
when_to_use: 'write a page, publish a page, add a page to the site, update a page, start a knowledge site, site_lint, lint the site, broken link, missing fragment, missing h1, undefined class, unbalanced tag, page metadata, page type, page date, move a page, rename a page, redirect stub, reorganize the site, section hub, hub page, front page, archive, catalogue, catalogue.py, hub.css, hub.js, theme toggle, back button, LLM-authored banner, link style'
user-invocable: true
---

# Knowledge site

The site is `~/public_html/`, served at
`https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/`. The core `web` skill owns
the slot, the `web:publish` grant and the 200 check; this skill owns the
page contract, the linter and the order of the site.

## The site's rules file

`~/public_html/CLAUDE.md` states, once for the site:

- `NS` — the page-metadata namespace, lowercase (`<meta name="NS:type">`);
- the page types in catalogue order, ending with `index`, the type of
  hub pages (`guides,research,notes,index`);
- the site title the archive page shows;
- any section rules the user has given.

ALWAYS read it before touching a page. Every command below takes `NS`,
the types and the title from it. NEVER pick a namespace or a type on the
fly — ALWAYS add a new type to that file first, with the user's consent.

## Start a site (once)

1. Copy `~/.claude/skills/knowledge-site/components/hub.css` and
   `~/.claude/skills/knowledge-site/components/hub.js` into
   `~/public_html/assets/`.
2. Write `~/public_html/CLAUDE.md` with the four items above and a line
   that names this skill.
3. Write the root hub `~/public_html/index.html` from
   `~/.claude/skills/knowledge-site/components/page.html`: no back button
   on the root page, `assets/` paths without `../`.
4. Write the archive (§ Archive) and link it from the root hub.

Completion criterion: `site_lint.py` exits 0 and the root URL returns 200.

## Write or change a page

Follow `guides/pages.md`: the skeleton, the metadata, the components, the
link style. A new page is linked from its section hub in the same change
(`guides/organize.md` § Hub pages). A research page also follows the
`research` skill for claim tags and sources.

## Check before announcing

```sh
python3 ~/.claude/skills/knowledge-site/scripts/site_lint.py ~/public_html --ns NS
```

Every finding is an error, one line each; exit 0 means clean. `--rule
NAME` reruns one rule. Root-relative links resolve under `--www`
(default `/var/lib/www`, the read-only view of every published tree).

| rule | finds | fix |
| --- | --- | --- |
| `html-balance` | the first unclosed or stray tag on a page | close the tag; escape a literal `<word>` in text as `&lt;word&gt;` |
| `title-h1` | an empty `<title>` or no `<h1>` | give both (`guides/pages.md` § Skeleton) |
| `links` | an empty href, a missing file or page, a missing `#fragment` | fix the path or depth, or add the `id` |
| `css-class` | a class no loaded stylesheet or script defines | use a `hub.css` component (`guides/pages.md` § Components) |
| `page-meta` | a missing or malformed `NS:type` or `NS:date` | stamp the page (`guides/pages.md` § Metadata) |
| `redirect-stub` | a stub whose target is missing or whose body links elsewhere | `guides/organize.md` § Move a page |

Then verify the changed URLs return 200 (`web` skill § Verify).

## Organize

Hub pages, section labels, moves, retiring a page and the archive:
`guides/organize.md`. After a page is added, moved, retyped or redated,
regenerate the archive.

## Archive

```sh
uv run --with jinja2 ~/.claude/skills/knowledge-site/scripts/catalogue.py \
  ~/public_html --ns NS --types T1,T2,T3 --title "SITE TITLE"
```

It writes `~/public_html/archive/index.html`: every stamped page, grouped
in `--types` order, newest first, redirect stubs skipped. Exit 1 names
each page whose type is not in `--types`; fix that page's type, or add
the type to the site's rules file first.

## ALWAYS

- Read `~/public_html/CLAUDE.md` and the `CLAUDE.md` of the subtree
  before writing a page.
- Build every page from `components/page.html` and the classes in the
  site's `assets/hub.css`.
- Stamp every page except a redirect stub with `NS:type` and `NS:date`.
- Link a new page from its section hub in the same change.
- Run `site_lint.py` to exit 0 and check 200 on every changed URL before
  announcing it.
- Send the page URL and a one-line summary in the chat; a URL alone is
  not a delivery.

## NEVER

- Delete a page — ALWAYS leave a redirect stub when it moves, or unlink
  it from its hub to retire it (`guides/organize.md`).
- Hand-roll a colour, a `:root` override or a per-page class — ALWAYS use
  a `hub.css` component, or add the component to `assets/hub.css` once
  for the whole site.
- Hard-code an "LLM-authored" disclaimer — ALWAYS let `hub.js` insert the
  banner from the page metadata (`guides/pages.md` § Metadata).
- Announce a page while `site_lint.py` reports a finding on it — ALWAYS
  fix it first. Findings on pages you did not touch go to the user in
  one message with their count; NEVER add a new one.
- Edit the skill's own `components/` — ALWAYS change the site's copy in
  `~/public_html/assets/`.
- Put notes, source clones or research drafts under `~/public_html/` —
  ALWAYS keep them in `~/workspace/` or `~/facts/` (`web` skill § Pages
  only).

## Files

- `guides/pages.md` — skeleton, metadata, components, links
- `guides/organize.md` — hub pages, section labels, moves, retiring, archive
- `components/page.html` — page skeleton
- `components/hub.css`, `components/hub.js` — site stylesheet and script
  (theme toggle, back button, external-link arrows, LLM-authored banner)
- `components/catalogue.html.j2` — the archive page template
- `scripts/site_lint.py`, `scripts/catalogue.py` — the linter and the
  archive generator; each prints its usage with `--help`
