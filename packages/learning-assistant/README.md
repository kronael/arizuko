# Learning assistant package

A learning assistant that keeps a published knowledge site: it answers from the pages it has published, researches what they do not cover, and organises the site.
The persona is `group/PERSONA.md`; the agent greets as Krons.
The [package guide](../../EXTENDING.md) explains package selection and installation.

## Install

Select the package for a group. The command installs it:

```sh
arizuko package add -g FOLDER github.com/kronael/arizuko/packages/learning-assistant
```

Use `-i/--instance NAME` to select another configured instance. `arizuko package sync` installs every selected package again.
Set `TELEGRAM_BOT_TOKEN` in the instance `.env` for a Telegram channel.

## What it does

- Answers a question from the published pages and cites the page URL.
- Researches a topic the site does not cover, with primary sources, and publishes a page.
- Tags each claim `[V]` verified, `[I]` inferred or `[U]` unknown.
- Organises the site: hub pages, moved pages with redirect stubs, an archive index.

The agent writes pages to `~/public_html/`, the same site the core `web` skill publishes.
The package differs from `strategy`, which delivers briefings, and from the core `hub` skill, which builds one page.
