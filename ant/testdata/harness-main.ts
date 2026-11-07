// A subprocess-only harness fixture. The real entrypoint and MCP client run;
// filesystem paths are mapped into the Go test's private temporary directory.
import { mock } from 'bun:test';
import fs from 'fs';
import net from 'net';
import path from 'path';

const root = process.env.VOLUNTARY_FIXTURE_ROOT!;
const source = process.env.VOLUNTARY_FIXTURE_SOURCE!;
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

const profile = { continuity: 'native', tools: 'mcp', guard: 'none', steering: 'between-turn', manual: 'rendered', status: 'streaming' };
function adapter(name: string) { return { name, profile, run: async function* (turn: {model?: string; effort?: string}) {
  console.log('ADAPTER ' + JSON.stringify({name, model: turn.model, effort: turn.effort}));
  yield { kind: 'result', ok: true };
} }; }
mock.module(path.join(source, 'claude.ts'), () => ({claudeHarness: adapter('claude')}));
mock.module(path.join(source, 'codex.ts'), () => ({codexHarness: adapter('codex')}));
mock.module(path.join(source, 'mcp.ts'), () => ({
 submitTurn: async (payload: unknown) => { console.log('SUBMIT ' + JSON.stringify(payload)); return {nudge: ''}; },
 submitStatus: async () => ({ remind: false }),
}));
