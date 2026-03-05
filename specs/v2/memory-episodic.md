# Memory: Episodic — open

Archived conversation transcripts the agent can recall.

## Current state

The pre-compact hook (agent runner) writes markdown archives to
`/workspace/group/conversations/YYYY-MM-DD-<summary>.md` when Claude Code
compacts a session. These are readable files in the group workspace.

The agent _can_ read them with standard file tools (`cat`, `ls`) but:

- Has no automatic awareness they exist
- Has no search capability — must know filename or list directory
- Archives only exist after compaction fires (not after every session)

No vector search, no embedding index, no retrieval mechanism exists.

## What episodic memory should provide

A way for the agent to answer: "what did we discuss about X last month?"
or "have I helped Alice with this before?" — without reading every archive.

## Proposed

### Minimal (file-based, no vector DB)

1. On session end / compaction, append a one-line summary entry to
   `/workspace/group/conversations/index.md`:
   ```
   2026-03-05 | alice-deployment-help | Alice asked about deploying to REDACTED
   ```
2. Agent reads `index.md` on demand to find relevant archives by date/keyword.
3. No infrastructure dependency.

### Full (vector search)

Embed each archived conversation and store vectors in a local index
(`/workspace/group/memory.db` using sqlite-vec or similar). Agent queries
via an MCP tool `search_memory(query)` → returns top-k relevant excerpts.

Requires: embedding model access (Voyage, OpenAI, or local), MCP tool
implementation, index rebuild on new archives.

## Open

- Decide: minimal file index vs vector search
- Implement session-end indexing (currently only compaction triggers archive)
- MCP tool `search_memory` if going vector route
- Expose `conversations/` path in agent SKILL.md so agent knows to look there
