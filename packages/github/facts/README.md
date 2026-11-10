# facts/ — what this agent knows without reading the code

One markdown file per durable truth about the repository. The agent reads
these before it answers, so a fact here saves a grep on every turn.

`repo.md` is the minimum. Good contents:

- how to build, test and lint — the exact commands, not the intent
- which directories are generated and must never be hand-edited
- the release procedure, and what "done" means for a change
- known-flaky tests, and what a red one actually indicates
- who owns which area, if more than one person commits

A fact that is wrong is worse than a fact that is missing: the agent will
cite it. Treat a stale line here as a bug with a filename.
