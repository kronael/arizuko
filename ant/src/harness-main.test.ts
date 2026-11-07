import { test, expect } from 'bun:test';
import { mkdtempSync } from 'fs';
import { tmpdir } from 'os';
import path from 'path';

async function run(doc?: string) {
 const root = mkdtempSync(path.join(tmpdir(), 'harness-'));
 const env: Record<string, string> = { ...process.env as Record<string,string>, VOLUNTARY_FIXTURE_ROOT: root, VOLUNTARY_FIXTURE_SOURCE: import.meta.dir, ARIZUKO_BACKEND:'claude', ARIZUKO_MODEL:'default-model', ARIZUKO_EFFORT:'high' };
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
 for (const backend of ['claude', 'codex']) {
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
 expect(await child.exited).toBe(1);
 expect(stderr).toContain('prompt must be a string');
});
