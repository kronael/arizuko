## Check
grep -q "projects/\*/\*.jsonl" ~/.claude/skills/compact-memories/SKILL.md 2>/dev/null && echo "done" || echo "needed"

## Steps
cp /workspace/self/ant/skills/compact-memories/SKILL.md ~/.claude/skills/compact-memories/SKILL.md
