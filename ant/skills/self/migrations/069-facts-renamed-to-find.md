# 069 — /facts renamed to /find

`/facts` is now `/find`. Skill directory renamed; invocation is
`/find <topic>`. Storage directory `facts/` is unchanged — verified
knowledge still lands at `facts/<slug>.md`. Old invocations will
silently miss until the agent resolve-skill path rediscovers.
