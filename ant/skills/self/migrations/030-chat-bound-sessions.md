# 030 — chat-bound sessions

Gateway uses file-based IPC instead of stdin:

- `start.json` — session config, prompt, secrets; agent deletes after reading
- `input/` — follow-up messages during an active session; agent deletes after processing
- No more stdin
- `send_reply` — replies to the bound conversation; use `send_message`
  only for cross-chat
- `IDLE_TIMEOUT` removed — containers exit when `input/` is empty and a
  `_close` sentinel is written
