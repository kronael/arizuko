# Workflows (v2)

Agent-driven multi-step processes across sessions.

## Agent-side media processing via MCP

Agent registers own MCP server for media tools (OCR,
image description, etc.). Gateway downloads/saves
(MIME pipeline), enrichment happens agent-side.

```json
{
  "mcpServers": {
    "media": {
      "command": "node",
      "args": ["/workspace/group/tools/media-server.js"]
    }
  }
}
```

Pro: contextual processing, no gateway restart, agent
configures own models. Con: uses agent turn tokens/time,
no pre-enrichment.

## Sub-workflows / sub-groups

Delegate work to sub-groups/sub-agents via JID patterns
(worlds spec). Not yet designed.
