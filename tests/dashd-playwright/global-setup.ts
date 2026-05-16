// global-setup: build dashd + seed binary if missing, mint a fresh
// DATA_DIR, seed it, spawn dashd, write context (PID, DATA_DIR, port)
// to a state file for global-teardown.

import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import * as net from 'node:net';

const repoRoot = resolve(__dirname, '..', '..');
const seedBin = join(repoRoot, 'tmp', 'dashd-seed');
const dashdBin = join(repoRoot, 'tmp', 'dashd-bin');
const stateFile = join(__dirname, '.test-state.json');

function run(cmd: string, args: string[], cwd: string): void {
  const r = spawnSync(cmd, args, { cwd, stdio: 'inherit' });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(' ')} → ${r.status}`);
}

async function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const s = net.createServer();
    s.listen(0, '127.0.0.1', () => {
      const port = (s.address() as net.AddressInfo).port;
      s.close(() => res(port));
    });
    s.on('error', rej);
  });
}

async function waitHealth(url: string, timeoutMs = 5000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url);
      if (r.ok) return;
    } catch {
      /* not ready */
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`dashd health timeout: ${url}`);
}

export default async function globalSetup(): Promise<void> {
  // Always rebuild; Go's incremental compiler makes this fast (<200 ms
  // when nothing changed) and avoids "stale binary" surprises when the
  // suite is run after editing seed/main.go or dashd handlers.
  console.log('[setup] building seed binary');
  run(
    'go',
    ['build', '-o', seedBin, './tests/dashd-playwright/seed/'],
    repoRoot,
  );
  console.log('[setup] building dashd binary');
  run('go', ['build', '-o', dashdBin, './dashd'], repoRoot);

  const dataDir = mkdtempSync(join(tmpdir(), 'arizuko-dashd-pw-'));
  console.log(`[setup] DATA_DIR=${dataDir}`);
  run(seedBin, ['-data', dataDir], repoRoot);

  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const env = {
    ...process.env,
    DATA_DIR: dataDir,
    HOST_DATA_DIR: dataDir,
    HOST_APP_DIR: repoRoot,
    ARIZUKO_DEV: 'true',
    DASH_PORT: `:${port}`,
  };
  const child = spawn(dashdBin, [], {
    env,
    cwd: dataDir,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.stdout!.on('data', (b) => process.stdout.write(`[dashd] ${b}`));
  child.stderr!.on('data', (b) => process.stdout.write(`[dashd] ${b}`));
  child.on('exit', (code) => {
    if (code !== 0 && code !== null)
      console.error(`[setup] dashd exited ${code}`);
  });

  await waitHealth(`${baseURL}/health`);
  console.log(`[setup] dashd up at ${baseURL}`);

  writeFileSync(
    stateFile,
    JSON.stringify({ pid: child.pid, dataDir, port, baseURL }),
  );
  process.env.DASHD_BASE_URL = baseURL;
  process.env.DASHD_DATA_DIR = dataDir;
}
