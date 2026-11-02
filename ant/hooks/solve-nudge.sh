#!/bin/sh
# UserPromptSubmit hook: nudge the agent to run /solve on the first prompt of a
# session and on the first prompt after an idle gap. It reads the hook JSON on
# stdin and keeps no state: "first" is a transcript with no user or assistant
# entry, "idle" is the newest such entry being older than IDLE_SECONDS. The
# current prompt is not in the transcript as a user entry yet when the hook runs.
# It never exits 2, which would block the prompt.
set -u

IDLE_SECONDS=1800

input=$(cat)
prompt=$(printf '%s' "$input" | jq -r '.prompt // ""' 2>/dev/null)
transcript=$(printf '%s' "$input" | jq -r '.transcript_path // ""' 2>/dev/null)

case $prompt in
*/solve*) exit 0 ;;
esac

last=""
if [ -n "$transcript" ] && [ -r "$transcript" ]; then
	last=$(jq -rR 'fromjson? | select(.type == "user" or .type == "assistant") | .timestamp | sub("\\.[0-9]+"; "") | fromdateiso8601' "$transcript" 2>/dev/null | tail -n 1)
fi

if [ -z "$last" ]; then
	text='New task (first prompt this session): run /solve before acting — it classifies the message, recalls context and matches skills. Skip only if this prompt directly continues work already in context.'
elif [ $(($(date +%s) - last)) -ge "$IDLE_SECONDS" ]; then
	text='New task? This prompt follows an idle gap: run /solve before acting unless it continues the work already in context.'
else
	exit 0
fi

jq -n --arg text "$text" '{hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext: $text}}'
exit 0
