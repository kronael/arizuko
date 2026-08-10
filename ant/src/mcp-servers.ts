// MCP server assembly for the agent query. A pure function over config — no
// SDK runtime — so the assembly is unit-testable.

export type McpServerConfig = {
  command: string;
  args?: string[];
  env?: Record<string, string>;
  // alwaysLoad: true keeps every tool from this server eager (defer_loading:
  // false on the API). Omit it and the SDK defers the server's tools behind
  // the Tool Search Tool.
  alwaysLoad?: boolean;
};

const GATED_SOCKET = '/run/ipc/gated.sock';

// injectMcpEnv builds the agent's whole MCP server map. arizuko — socat to the
// per-turn gated socket — is the ONLY server, so every tool call crosses
// ipc.serveConn and meets the grant check, the audit row and CheckHold.
//
// A server the agent (or a product) declares in ~/.claude/settings.json is NOT
// read. That path handed tools straight to the harness: they never reached
// ipc.serveConn, no hold:mcp:<tool> rule could suspend them, and the agent
// writes that file itself, so it was a self-grant (BUGS J1, F76). Third-party
// MCP arrives as an arizuko connector (ipc.StoreFns.Connectors — built-in
// providers plus the operator's connectors.toml), served over this same socket.
//
// alwaysLoad keeps the core turn tools (send/reply/inspect_*/send_file) eager;
// it is per-server, so the management and connector tools ride along eagerly
// too — deferring those needs a routd-side server split.
export function injectMcpEnv(
  secrets: Record<string, string | undefined>,
): Record<string, McpServerConfig> {
  const definedSecrets: Record<string, string> = {};
  for (const [k, v] of Object.entries(secrets)) {
    if (v !== undefined) definedSecrets[k] = v;
  }
  return {
    arizuko: {
      command: 'socat',
      args: ['STDIO', `UNIX-CONNECT:${GATED_SOCKET}`],
      env: definedSecrets,
      alwaysLoad: true,
    },
  };
}
