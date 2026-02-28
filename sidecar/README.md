# Sidecars

Adjacent MCP servers that extend kanipi operations.

## Adding a sidecar

1. Place binary or script in this directory
2. Register in `template/workspace/mcporter.json`:

```json
{
  "servers": {
    "my-sidecar": {
      "command": "/app/sidecar/my-sidecar",
      "transport": "stdio"
    }
  }
}
```

3. Rebuild image: `make image`

The openclaw agent discovers tools via mcporter and calls them
natively during conversations.
