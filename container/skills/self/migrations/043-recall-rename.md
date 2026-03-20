Renamed `/recall` skill to `/recall-memories`. Added new `/recall-messages` skill
for searching older chat messages via `get_history` IPC.

```bash
# Rename skill directory
mv ~/.claude/skills/recall ~/.claude/skills/recall-memories

# Update frontmatter
sed -i 's/^name: recall$/name: recall-memories/' \
  ~/.claude/skills/recall-memories/SKILL.md

# Fetch recall-messages skill
cp /workspace/self/container/skills/recall-messages/SKILL.md \
   ~/.claude/skills/recall-messages/SKILL.md 2>/dev/null || true
```
