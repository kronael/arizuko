import { test, expect } from 'bun:test';
import { mkdtempSync } from 'fs';
import { tmpdir } from 'os';
import path from 'path';

async function run(doc?: string, mode?: string, callbackFail = false) {
 const root = mkdtempSync(path.join(tmpdir(), 'harness-'));
 const env: Record<string, string> = { ...process.env as Record<string,string>, VOLUNTARY_FIXTURE_ROOT: root, VOLUNTARY_FIXTURE_SOURCE: import.meta.dir, ARIZUKO_BACKEND:'claude', ARIZUKO_MODEL:'default-model', ARIZUKO_EFFORT:'high' };
 if (mode) env.VERDICT_FIXTURE = mode;
 if (callbackFail) env.VERDICT_CALLBACK_FAIL = '1';
 if (doc !== undefined) env.ARIZUKO_HARNESS = doc; else delete env.ARIZUKO_HARNESS;
 const child = Bun.spawn(['bun', '--preload', path.join(import.meta.dir, '../testdata/harness-main.ts'), path.join(import.meta.dir, 'index.ts')], {env, stdin:'pipe', stdout:'pipe', stderr:'pipe'});
 child.stdin.write('{"prompt":"test","groupFolder":"team","chatJid":"tg:1"}'); child.stdin.end();
 const output = await new Response(child.stdout).text();
 return {output, code:await child.exited};
}

test('invalid harness submits terminal failure before any adapter', async () => {
 for (const value of ['', 'null', '[]', '{}', '{"backend":"unknown"}', '{"backend":1}', '{"backend":"claude","effort":"bad"}', '{"backend":"claude","extra":1}']) {
  const result = await run(value);
  expect(result.code).toBe(78); expect(result.output).toContain('"status":"error"'); expect(result.output).not.toContain('ADAPTER ');
 }
});

test('harness document replaces all defaults at the adapter', async () => {
 for (const backend of ['claude', 'codex', 'pi']) {
  const result = await run(JSON.stringify({backend}));
  expect(result.code).toBe(0); expect(result.output).toContain(`ADAPTER {"name":"${backend}"}`);
 }
 const result = await run();
 expect(result.output).toContain('ADAPTER {"name":"claude","model":"default-model","effort":"high"}');
});

test('missing prompt fails before starting a turn', async () => {
 const child = Bun.spawn(['bun', path.join(import.meta.dir, 'index.ts')], {stdin:'pipe', stdout:'pipe', stderr:'pipe'});
 child.stdin.write('{}'); child.stdin.end();
 const stderr = await new Response(child.stderr).text();
 expect(await child.exited).toBe(78);
 expect(stderr).toContain('prompt must be a string');
});

/** Local and frame failures choose the process verdict and submit only once. */
test('failure verdict controls exit and submission', async () => {
 for (const [mode, code] of [['unknown', 1], ['transport', 1], ['opaque', 1], ['transient', 1], ['permanent', 78]] as const) {
  const result = await run(undefined, mode);
  expect(result.code).toBe(code);
  expect(result.output.match(/SUBMIT /g)).toHaveLength(1);
 }
});

/** A failed callback cannot replace the original process verdict. */
test('callback failure preserves the original verdict', async () => {
 for (const [mode, code] of [['unknown', 1], ['transport', 1], ['opaque', 1], ['transient', 1], ['permanent', 78]] as const) {
  const result = await run(undefined, mode, true);
  expect(result.code).toBe(code);
  expect(result.output.match(/SUBMIT /g)).toHaveLength(1);
 }
});
