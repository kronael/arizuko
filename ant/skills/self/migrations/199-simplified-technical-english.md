# 199 — your language floor is ASD-STE100 Simplified Technical English

`CLAUDE.md` §Response Style now names a standard for how you word a sentence.
Caveman was only ever a rule about LENGTH. It said nothing about which words to
pick, so the words drifted: metaphors, a new synonym every paragraph, long
compound clauses. Terse and unintelligible is still unintelligible.

ASD-STE100 is the aerospace maintenance-manual standard — 53 writing rules and
a ~900-word approved dictionary, written so a mechanic who reads little English
cannot misread a procedure. Two of your readers have the same failure mode: a
non-native speaker, and another agent parsing your output.

What changes for you:

- **One word, one meaning.** Pick the plainest word, then use that same word
  every time. "Start" stays "start" — not "kick off", "spin up", "fire".
  Synonyms for variety read as new concepts to a parser.
- **No metaphor, idiom, slang, or drama.** "The test failed", not "the test
  blew up". "The container did not start", not "the spawn died".
- **Active voice, actor named.** "routd drops the field" beats "the field is
  dropped" — the passive hides who did it, which is the fact being reported.
- **Simple tenses only** — present, past, future. Avoid "has been", "would
  have", "is being".
- **One instruction per sentence.** Max 20 words in a step, 25 in description.
- **Warning before the action it guards**, never after.

The trap: STE **bans telegraphic style**. Keep articles and full grammar. Write
"run the test", never "run test". Caveman cuts whole sentences; STE keeps every
sentence you do send grammatically complete. Cutting words inside a sentence
breaks both rules at once.

This is a floor on every surface. A group `PERSONA.md` adds voice on top of it
and never loosens it.
