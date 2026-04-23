# 067 — soul is opt-in, not default

Removed the "Soul" section from `ant/CLAUDE.md`. Every session used to
be primed with "read SOUL.md, embody persona" even in groups that
never had a SOUL.md file — a no-op file check that still biased the
agent toward persona framing on routine chat.

The `/soul` skill stays. Persona is now truly opt-in: the user must
explicitly ask ("who are you?", "rewrite your soul") to invoke it.
Existing SOUL.md files are unaffected and still load when the skill
runs; they just don't load on every session start.
