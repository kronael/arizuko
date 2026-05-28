// Asserts the eager/deferred MCP-tool split (spec 5/E): arizuko core tools
// stay eager (alwaysLoad), third-party connector servers default to deferred.

import { test, expect } from 'bun:test';
import { injectMcpEnv } from './mcp-servers.js';

test('injectMcpEnv: arizuko core server is alwaysLoad (eager)', () => {
  const out = injectMcpEnv({}, {});
  expect(out.arizuko).toBeDefined();
  expect(out.arizuko.alwaysLoad).toBe(true);
  expect(out.arizuko.command).toBe('socat');
});

test('injectMcpEnv: third-party connector servers default to deferred', () => {
  const out = injectMcpEnv(
    { slack: { command: 'node', args: ['slack-mcp.js'] } },
    {},
  );
  expect(out.slack).toBeDefined();
  // No alwaysLoad → the SDK defers these tools behind Tool Search.
  expect(out.slack.alwaysLoad).toBeUndefined();
  // arizuko stays eager alongside the deferred connector.
  expect(out.arizuko.alwaysLoad).toBe(true);
});

test('injectMcpEnv: secrets fold into every server env', () => {
  const out = injectMcpEnv(
    { slack: { command: 'node', env: { EXISTING: '1' } } },
    { SLACK_TOKEN: 'xoxb-secret', SKIP: undefined },
  );
  expect(out.slack.env).toEqual({ EXISTING: '1', SLACK_TOKEN: 'xoxb-secret' });
  expect(out.arizuko.env).toEqual({ SLACK_TOKEN: 'xoxb-secret' });
});
