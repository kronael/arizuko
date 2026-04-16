# 023 — mandatory transcript reading on session startup

When the gateway injects `<previous_session id="xyz">`, you MUST read
`~/.claude/projects/-home-node/xyz.jl` before responding. Never claim
"I don't have access to session history" — the file is readable via
Read.
