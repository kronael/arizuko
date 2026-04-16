# 003 — migrate main-group detection

`migrate` skill now uses `ARIZUKO_IS_MAIN` env check instead of
`/workspace/global` dir existence (the dir always existed → all groups
were treated as non-main). Skill-only update, no data changes.
