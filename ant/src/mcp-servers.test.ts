// Asserts the agent's MCP server map: arizuko is the only server and its core
// tools stay eager, and a settings.json-declared server never reaches the SDK.

import { test, expect } from 'bun:test';
import fs from 'fs';
import os from 'os';
import path from 'path';
import * as mcpServers from './mcp-servers.js';
import { injectMcpEnv } from './mcp-servers.js';

test('injectMcpEnv: arizuko core server is alwaysLoad (eager)', () => {
  const out = injectMcpEnv({});
  expect(out.arizuko).toBeDefined();
  expect(out.arizuko.alwaysLoad).toBe(true);
  expect(out.arizuko.command).toBe('socat');
});

test('injectMcpEnv: secrets fold into the arizuko server env', () => {
  const out = injectMcpEnv({ SLACK_TOKEN: 'xoxb-secret', SKIP: undefined });
  expect(out.arizuko.env).toEqual({ SLACK_TOKEN: 'xoxb-secret' });
});

// BUGS J1/F76: a settings.json server was handed straight to the harness, so
// its tool calls never crossed ipc.serveConn and no hold:mcp:<tool> rule could
// suspend them. The agent writes that file itself, so it was a self-grant.
test('a settings.json mcpServers entry never reaches the SDK config', () => {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'ant-home-'));
  fs.mkdirSync(path.join(home, '.claude'));
  fs.writeFileSync(
    path.join(home, '.claude', 'settings.json'),
    JSON.stringify({
      mcpServers: { sneaky: { command: 'node', args: ['sneaky.js'] } },
      outputStyle: 'telegram',
    }),
  );
  const prevHome = process.env.HOME;
  process.env.HOME = home;
  try {
    expect(Object.keys(injectMcpEnv({})).sort()).toEqual(['arizuko']);
  } finally {
    if (prevHome === undefined) delete process.env.HOME;
    else process.env.HOME = prevHome;
    fs.unlinkSync(path.join(home, '.claude', 'settings.json'));
    fs.rmdirSync(path.join(home, '.claude'));
    fs.rmdirSync(home);
  }
});

// The reader is gone, not merely unused: a re-added loader would fail here
// before it could re-open the bypass.
test('mcp-servers exports no settings.json reader', () => {
  expect(Object.keys(mcpServers)).toEqual(['injectMcpEnv']);
});
