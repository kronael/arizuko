// MCP server assembly for the agent query. Pure functions over config —
// no SDK runtime — so the eager/deferred split is unit-testable. Spec 6/A.

import fs from 'fs';

export type McpServerConfig = {
  command: string;
  args?: string[];
  env?: Record<string, string>;
  // alwaysLoad: true keeps every tool from this server eager (defer_loading:
  // false on the API). Omit it and the SDK defers the server's tools behind
  // the Tool Search Tool. Spec 6/A.
  alwaysLoad?: boolean;
};

const GATED_SOCKET = '/run/ipc/gated.sock';

// loadAgentMcpServers reads third-party MCP servers the agent self-registered
// (or the operator seeded) in settings.json. The arizuko server is synthesised
// in injectMcpEnv, so drop any stale copy here.
export function loadAgentMcpServers(home: string): Record<string, McpServerConfig> {
  try {
    const s = JSON.parse(fs.readFileSync(`${home}/.claude/settings.json`, 'utf-8'));
    const servers = s.mcpServers;
    if (!servers || typeof servers !== 'object') return {};
    delete servers.arizuko;
    return servers;
  } catch {
    return {};
  }
}

// injectMcpEnv folds secrets into each server's env and decides the eager vs
// deferred split:
//   - third-party servers (per-folder connectors) default to deferred — their
//     tools load only when the model finds them via Tool Search, so large
//     platform surfaces never ride the eager request prefix.
//   - arizuko (socat to gated) is alwaysLoad: it serves the per-turn core
//     tools (send/reply/inspect_*/send_file) needed every turn. alwaysLoad is
//     per-server, so gated's management + connectors.toml tools ride along
//     eagerly too — deferring those needs a gated-side server split.
export function injectMcpEnv(
  servers: Record<string, McpServerConfig>,
  secrets: Record<string, string | undefined>,
): Record<string, McpServerConfig> {
  const definedSecrets: Record<string, string> = {};
  for (const [k, v] of Object.entries(secrets)) {
    if (v !== undefined) definedSecrets[k] = v;
  }
  const out: Record<string, McpServerConfig> = {};
  for (const [name, cfg] of Object.entries(servers)) {
    out[name] = { ...cfg, env: { ...cfg.env, ...definedSecrets } };
  }
  out.arizuko = {
    command: 'socat',
    args: ['STDIO', `UNIX-CONNECT:${GATED_SOCKET}`],
    env: definedSecrets,
    alwaysLoad: true,
  };
  return out;
}
