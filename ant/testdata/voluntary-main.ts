// A subprocess-only harness fixture. The real entrypoint and MCP client run;
// filesystem paths are mapped into the Go test's private temporary directory.
import { mock } from 'bun:test';
import fs from 'fs';
import net from 'net';
import path from 'path';

const root = process.env.VOLUNTARY_FIXTURE_ROOT!;
const source = process.env.VOLUNTARY_FIXTURE_SOURCE!;
const mode = process.env.VOLUNTARY_FIXTURE_MODE!;
const originalFS = { ...fs };
const originalNet = { ...net };
function mapped(value: unknown) {
  if (typeof value !== 'string') return value;
  for (const prefix of ['/run/ipc', '/home/node', '/tmp/arizuko-steer', '/tmp/arizuko-policy', '/tmp/input.json']) {
    if (value === prefix || value.startsWith(prefix + '/')) return root + value;
  }
  return value;
}
const fixtureFS = { ...originalFS };
for (const name of ['existsSync', 'mkdirSync', 'readdirSync', 'readFileSync', 'writeFileSync', 'unlinkSync'] as const) {
  fixtureFS[name] = ((file: unknown, ...args: unknown[]) => Reflect.apply(originalFS[name], originalFS, [mapped(file), ...args])) as never;
}
fixtureFS.renameSync = (from, to) => originalFS.renameSync(mapped(from) as string, mapped(to) as string);
mock.module('fs', () => ({ ...fixtureFS, default: fixtureFS }));
const fixtureNet = {
  ...originalNet,
  createConnection: (socket: string) => originalNet.createConnection(mapped(socket) as string),
};
mock.module('net', () => ({ ...fixtureNet, default: fixtureNet }));

let runs = 0;
mock.module(path.join(source, 'backend.ts'), () => ({
  selectHarness: () => ({
    name: 'fixture',
    profile: { continuity: 'native', tools: 'mcp', guard: 'none', steering: 'between-turn', manual: 'rendered', status: 'streaming' },
    run: async function* (turn: { turnID: string }) {
      runs++;
      const { rpc } = await import(path.join(source, 'mcp.ts'));
      yield { kind: 'continuity', token: 'fixture-token', sessionId: 'fixture-session' };
      if ((mode === 'queued' && runs === 1) || (mode === 'submit-nudge' && runs === 2)) {
        await rpc('tools/call', { name: 'reply', arguments: { chatJid: 'tg:1', text: 'first answer' } });
      }
      if (mode === 'submit-nudge' && runs === 1) {
        yield { kind: 'result', ok: true, text: 'terminal thinking stays private' };
        return;
      }
      try {
        await rpc('check_turn', { turn_id: turn.turnID });
      } catch (error) {
        console.error(`[fixture] Stop check failed: ${error}`);
      }
      yield { kind: 'result', ok: true, text: 'terminal thinking stays private' };
    },
  }),
}));
