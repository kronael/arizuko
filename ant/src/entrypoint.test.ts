import { expect, test } from 'bun:test';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

/** A signal to the entrypoint PID reaches Node and exits successfully. */
test('entrypoint delivers SIGUSR1 to the Node handler', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ant-entrypoint-'));
  fs.mkdirSync(`${dir}/app/node_modules`, { recursive: true });
  fs.mkdirSync(`${dir}/tmp/dist`, { recursive: true });
  fs.mkdirSync(`${dir}/bin`);
  fs.writeFileSync(`${dir}/bin/bunx`, '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  fs.writeFileSync(`${dir}/tmp/dist/index.js`, `
const fs = require('node:fs');
if (fs.readFileSync(0, 'utf8') !== '{}') process.exit(2);
const timer = setInterval(() => {}, 1000);
process.on('SIGUSR1', () => {
  console.log('handled SIGUSR1');
  clearInterval(timer);
});
console.log('ready');
`);
  const script = fs.readFileSync(
    new URL('../entrypoint.sh', import.meta.url), 'utf8',
  ).replace(/\/app|\/tmp/g, name => `${dir}${name}`);
  fs.writeFileSync(`${dir}/entrypoint.sh`, script);
  const child = spawn('bash', [`${dir}/entrypoint.sh`], {
    detached: true,
    env: { ...process.env, PATH: `${dir}/bin:${process.env.PATH}` },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const signal = AbortSignal.timeout(5000);
  const exited = once(child, 'exit', { signal });
  const closed = once(child, 'close', { signal });
  let output = '';
  let stderr = '';
  child.stdout.on('data', data => { output += data; });
  child.stderr.on('data', data => { stderr += data; });
  try {
    const ready = once(child.stdout, 'data', { signal });
    child.stdin.end('{}');
    await ready;
    expect(output).toBe('ready\n');
    child.kill('SIGUSR1');
    const [code, terminationSignal] = await exited;
    expect(terminationSignal).toBeNull();
    expect(code).toBe(0);
    await closed;
    expect(output).toBe('ready\nhandled SIGUSR1\n');
    expect(stderr).toBe('');
  } finally {
    if (child.pid) {
      try {
        process.kill(-child.pid, 'SIGKILL');
      } catch (error) {
        if (!(error instanceof Error && 'code' in error &&
          error.code === 'ESRCH')) throw error;
      }
    }
    await closed;
    fs.chmodSync(`${dir}/tmp/dist`, 0o755);
    fs.rmSync(dir, { recursive: true, force: true });
  }
}, 10000);
