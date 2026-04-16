# 028 — status messages

Emit `<status>short text</status>` during long tasks; the runner strips
it from final output and sends it immediately as an interim update
(prefixed with an hourglass). Keep under 100 chars, multiple blocks fine.

Old 100-message mechanical heartbeat removed — status is now
agent-initiated.
