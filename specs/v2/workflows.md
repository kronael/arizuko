# Workflows (v2)

Agent-driven workflows — multi-step processes the agent orchestrates
across sessions.

## Open ideas

### Agent-side media processing via MCP

Instead of gateway-side MIME handlers for OCR, image description, etc.,
the agent registers its own MCP server that provides media tools:

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

Gateway still downloads and saves attachments (MIME pipeline). But
enrichment beyond transcription happens agent-side — the agent calls
`mcp__media__ocr` or `mcp__media__describe_image` on the saved file.

Advantages:

- Agent decides what processing to run per message (contextual)
- No gateway restart for new media capabilities
- Agent can install and configure its own models

Disadvantage:

- Processing happens during agent turn (uses tokens/time)
- Gateway can't pre-enrich before agent sees the message

### Sub-workflows / sub-groups

Agent delegates work to sub-groups or sub-agents. Routing between
them via JID patterns (worlds spec). Not yet designed.
