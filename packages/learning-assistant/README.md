# Learning assistant package

An agent that answers your questions from a website it writes itself, and researches a topic and adds a page when the site has no answer.
The [product page](../../template/web/pub/arizuko/products/learning-assistant/index.html) shows what it does, the [setup page](../../template/web/pub/arizuko/products/learning-assistant/setup.html) shows the install, and the [package guide](../../EXTENDING.md) explains package selection.
The persona is `group/PERSONA.md`. The agent greets as Krons, the `brand` in `arizuko.yml`.

## Install

Select the package for a group. The command installs it:

```sh
arizuko package add -g FOLDER github.com/kronael/arizuko/packages/learning-assistant
```

Use `-i/--instance NAME` to select another configured instance. `arizuko package sync` installs every selected package again.
Set `TELEGRAM_BOT_TOKEN` in the instance `.env` for a Telegram channel.

## The agent answers from the site first

- Answers a question from the published pages and cites the page URL and the sources that page lists.
- Researches a topic the site does not cover, with primary sources, and publishes a page.
- Tags each claim `[V]` verified against a source, `[I]` inferred or `[U]` unknown.
- Organises the site: hub pages, moved pages with redirect stubs, an archive page.
- Refreshes a researched page on a schedule: it rechecks the `[V]` claims, retags them and updates the page date.

The agent writes pages to `~/public_html/`, the same site the core `web` skill publishes.

```text
 question ──► research ──► answer from ~/public_html pages, cite page URL and sources
                 │            └─ no page covers it ─► core find ─► ~/facts/ ─► new page
                 └─ scheduled refresh: schedule_task names one page ─► recheck [V] claims,
                    retag [V] [I] [U], update NS:date
 page work ─► knowledge-site ─► page from components/page.html, hub.css, hub.js
                 ├─ site_lint.py ~/public_html --ns NS        must exit 0
                 ├─ organize: hub pages, a move leaves a redirect stub
                 └─ catalogue.py ~/public_html --ns NS ...  ─► archive/index.html
```

## Two skills split the work

- [`research`](skills/research/SKILL.md) answers from the site, researches an uncovered topic with the core `find` skill and publishes the result, tags claims, and refreshes a page through `schedule_task`.
- [`knowledge-site`](skills/knowledge-site/SKILL.md) owns the page contract in [`guides/pages.md`](skills/knowledge-site/guides/pages.md), the order of the site in [`guides/organize.md`](skills/knowledge-site/guides/organize.md), the linter and the archive script.

`NS` is the page-metadata namespace: the prefix of `<meta name="NS:type">` and `<meta name="NS:date">` on every page.
Each site states `NS` once in its rules file, `~/public_html/CLAUDE.md`, with the page types in archive order, ending with `index`, and the site title.
The agent writes that file when it starts a site (`knowledge-site` § Start a site).

## site_lint.py checks six rules

```sh
python3 ~/.claude/skills/knowledge-site/scripts/site_lint.py ~/public_html --ns NS [--www DIR [--vhost FOLDER]] [--rule NAME ...]
```

Every finding is an error, one line each, and any finding exits 1.
Root-relative links (`/pub/x/` or `/x/`) resolve under `--www`, default `/var/lib/www`, the read-only view of every published tree.
A relative link must stay inside `--www`, or inside the site when `--www` is not given.
On a mapped vhost, `--vhost FOLDER` (a folder directly under `--www`) resolves root-relative links as the server does: a path outside `/pub/` goes to that folder, and a `/pub/` path found nowhere else goes there too.

| rule | finds |
| --- | --- |
| `html-balance` | the first unclosed or stray tag on a page; optional end tags such as `</p>` and `</li>` close by themselves |
| `title-h1` | an empty `<title>` or no `<h1>`; redirect stubs are exempt |
| `links` | an empty URL, a missing file or page, a missing `#fragment`; external URLs and the dynamic paths `priv dash chat api x auth` are not checked |
| `css-class` | a class that no loaded stylesheet defines and no script names; `language-*` markers pass, and a page that loads the Tailwind CDN or a remote stylesheet is exempt |
| `page-meta` | a missing or malformed `NS:type` or `NS:date`; redirect stubs are exempt |
| `redirect-stub` | a meta-refresh page whose target does not resolve or whose body does not link the target |

## catalogue.py writes the archive page

```sh
uv run --with jinja2 ~/.claude/skills/knowledge-site/scripts/catalogue.py ~/public_html --ns NS --types T1,T2,... --title TEXT [--out FILE]
```

It lists every page that carries `NS:type`, grouped in `--types` order and newest first, and skips redirect stubs.
It writes `~/public_html/archive/index.html` unless `--out` names another file.
A page whose type is not in `--types` exits 1, and the message names the page.

## Components give every page one skeleton

The agent copies `hub.css` and `hub.js` into `~/public_html/assets/` once and edits that copy, never the skill's.

- `components/page.html`: the page skeleton with the metadata, back button, theme toggle, TL;DR box, section nav and footer.
- `components/hub.css`: the site stylesheet and its components.
- `components/hub.js`: theme toggle, back button, external-link arrows, and the LLM-authored banner on a page typed `research` or marked `NS:authored` `llm`.
- `components/catalogue.html.j2`: the archive page template.

## Site rules stay in the group

The package ships no page content.
The site's sections, palette, front-page choices and the owner's own workflows stay in the group's `CLAUDE.md` and in `~/public_html/CLAUDE.md`.
The package differs from `strategy`, which delivers briefings on a watchlist, and from the core `hub` skill, which builds one page.

## Tests and eval cases

```sh
cd packages/learning-assistant && PYTHONDONTWRITEBYTECODE=1 python3 -m pytest tests -q -p no:cacheprovider
```

The linter tests run all six rules on the clean fixture site in `tests/fixtures/site/`, built from the components, then break a copy of that site for each case and expect the rule to fire.
The catalogue tests cover grouping, order, stub skipping and the unknown-type exit.
The package tests check the persona frontmatter and that every path a skill names exists.
`eval/learning-assistant.toml` holds four `anteval` cases: a reply check and three cases that each cite the persona line they test.
