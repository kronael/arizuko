Install skills from the REDACTED tools repository (wisdom, tweet, trader) that
were previously only available globally and are now part of the canonical
container skill set.

```bash
# Copy REDACTED tools skills from canonical source
for skill in wisdom tweet trader; do
  src="/workspace/self/container/skills/$skill"
  dst="$HOME/.claude/skills/$skill"
  if [ -d "$src" ]; then
    mkdir -p "$dst"
    cp "$src/SKILL.md" "$dst/SKILL.md"
    echo "installed: $skill"
  else
    echo "skip (not found): $skill"
  fi
done
```
