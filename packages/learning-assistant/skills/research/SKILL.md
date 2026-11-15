---
name: research
description: Answer questions from the group's published knowledge site, research what it does not cover, and refresh researched pages on a schedule, with every claim tagged [V], [I] or [U]. NOT for page markup, links or moving pages (use knowledge-site) or a fact lookup the user wants kept off the site (use find).
when_to_use: 'what do you know about X, explain X, how does X work, is it true that, question, answer from the site, research X, look into X, deep dive, new topic, is this page still current, update the research, re-research, refresh the page, recheck the claims, verify a claim, sources, citation, claim tags, [V] [I] [U], verified inferred unknown, monthly research, recurring research, schedule a refresh'
user-invocable: true
---

# Research

The published site in `~/public_html/` is the memory. A question goes to
the site first, then to new research, and new research ends as a page.
Page markup, links and moves belong to the `knowledge-site` skill; `NS`
is the page-metadata namespace in `~/public_html/CLAUDE.md`.

## Answer from the site

1. Find the pages: read `~/public_html/archive/index.html` for the
   titles, then `grep -rli --include='*.html' '<term>' ~/public_html`
   with the question's key terms and their synonyms.
2. Read the hits: the `.tldr-box`, the matching sections, the claim tags
   and the footer sources.
3. Answer first, then cite: the page URL
   (`https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/<path>`) and the
   sources the page gives for the claims you used. Carry each claim's
   tag over as the page has it.
4. Say how fresh it is: the page's `NS:date`, and any `[I]` or `[U]`
   claim the answer rests on.

When the site covers part of the question, answer that part with its
citations, name the part it does not cover, and offer to research it.
When it covers none, say so and research.

## Research a new topic

1. Run the core `/find` on the question: it gathers primary sources,
   verifies each fact against its source and writes `~/facts/`.
2. Decide the place: a fact that extends an existing page goes into
   that page's section; a topic of its own gets a new page in the
   section whose type fits.
3. Write the page from the verified facts by
   `~/.claude/skills/knowledge-site/guides/pages.md`, typed `research`:
   the `.tldr-box` answers the question, each claim carries its tag
   (§ Claim tags) and its source.
4. Check and publish by `~/.claude/skills/knowledge-site/SKILL.md`
   § Check before announcing, regenerate the archive (§ Archive there),
   then answer the question in the chat with the page URL.
5. A page whose claims decay (versions, prices, releases, people,
   rankings, law) gets a refresh schedule (§ Scheduled refresh); tell
   the user its interval.

## Claim tags

| tag | means | the page shows |
| --- | --- | --- |
| `[V]` verified | you opened the source in this research and it says this | a link to the source in the same paragraph or list item |
| `[I]` inferred | follows from `[V]` claims but no source states it | the claims it follows from |
| `[U]` unknown | not checked, sources disagree, or no source found | what would settle it |

- The tag follows the claim in the text: `Version 2 dropped the
  plugin API [V] <a href="...">[Release notes]</a>`.
- One legend line per page, right under `.hub-subtitle`:
  `<p class="source-note">Checked YYYY-MM-DD · claims tagged [V] verified,
  [I] inferred, [U] unknown</p>`.
- The `.hub-footer` lists every source the page cites.

## Scheduled refresh

1. `list_tasks` first; NEVER create a second task for a page that has
   one.
2. `schedule_task` with a 5-field `cron` (monthly: `0 6 1 * *`; weekly
   for fast-moving topics) and a prompt that names the page path and
   says: "Refresh this page by the research skill § Refresh a page."
3. One task covers one page, or one section hub and the pages it lists.

## Refresh a page

1. Read the page and its sources.
2. Each `[V]` claim: open its source. It still says this → keep the tag.
   It changed → rewrite the claim from the source. It is gone → find
   another primary source, or retag `[U]`.
3. Each `[I]` and `[U]` claim: try to settle it now; a claim you
   verify becomes `[V]` with its source.
4. Run `/find` for what is new since the page's `NS:date` and fold it
   into the existing sections.
5. Update the legend's checked date. When the content changed, also
   update `NS:date` and add one line to the `.hub-footer`:
   `Updated YYYY-MM-DD: <what changed>`.
6. Check, regenerate the archive and verify 200 as in § Research a new
   topic, step 4.
7. Send one chat message naming the page and what changed; when
   nothing changed, send nothing.

## ALWAYS

- Search the published pages before answering from memory or starting
  research.
- Cite the page URL for an answer from the site, and the source for
  every `[V]` claim.
- Prefer primary sources: the code, the paper, the official document,
  the dataset. Two primary sources outweigh any number of articles that
  repeat each other.

## NEVER

- Tag a claim `[V]` without opening its source in this research —
  ALWAYS tag it `[I]` or `[U]` until you have.
- Upgrade a tag in the chat (an `[I]` on the page read out as fact) —
  ALWAYS repeat the page's tag.
- State a claim with no tag on a research page — ALWAYS tag it, or cut
  it.
- Keep research results only in the chat or `~/facts/` — ALWAYS publish
  what answers a topic as a page or a section.
- Add a "Recent updates" section — ALWAYS fold new findings into the
  sections they belong to and note the change in the footer line.
- Guess to fill a gap — ALWAYS write `[U]` and what would settle it.
