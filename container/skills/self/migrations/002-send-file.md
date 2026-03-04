# 002 — send_file MCP tool

Gateway now supports outbound file sending. The `send_file` MCP tool is
available in all groups.

## What changed

- New `send_file` tool in the MCP server — sends a file from the workspace
  to the user as a document attachment via Telegram.
- Store files under `/workspace/group/{folder}/media/YYYYMMDD/` before
  calling `send_file`.

## No agent-side action required

The tool is provided automatically by the gateway's MCP sidecar. No skill
files need updating.
