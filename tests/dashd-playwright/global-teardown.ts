import { readFileSync, rmSync, existsSync, unlinkSync } from 'node:fs';
import { join } from 'node:path';

const stateFile = join(__dirname, '.test-state.json');

export default async function globalTeardown(): Promise<void> {
  if (!existsSync(stateFile)) return;
  const { pid, dataDir } = JSON.parse(readFileSync(stateFile, 'utf8'));
  try {
    process.kill(pid, 'SIGTERM');
    // small grace; SIGKILL fallback after 500ms
    await new Promise((r) => setTimeout(r, 500));
    try {
      process.kill(pid, 'SIGKILL');
    } catch {
      /* already gone */
    }
  } catch {
    /* already gone */
  }
  if (dataDir && dataDir.includes('arizuko-dashd-pw-')) {
    rmSync(dataDir, { recursive: true, force: true });
  }
  unlinkSync(stateFile);
}
