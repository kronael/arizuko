import { expect, it } from 'bun:test';
import http from 'node:http';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

/** A human whose display name matches the assistant reaches routd. */
it('accepts a human with the assistant display name', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-main-'));
  let message = '';
  const router = http.createServer(async (req, res) => {
    if (req.url === '/v1/messages') {
      for await (const chunk of req) message += chunk;
    }
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ ok: true, token: 'registration-token' }));
  });
  await new Promise<void>((resolve) => router.listen(0, '127.0.0.1', resolve));
  const address = router.address();
  if (!address || typeof address === 'string')
    throw new Error('missing address');
  const child = Bun.spawn(['bun', 'run', 'src/main_fixture.test.ts'], {
    env: {
      ...process.env,
      WHAPD_FIXTURE: '1',
      ASSISTANT_NAME: 'Hermes',
      WHAPD_STATE_DIR: dir,
      WHATSAPP_AUTH_DIR: dir,
      ROUTER_URL: `http://127.0.0.1:${address.port}`,
      LISTEN_ADDR: '127.0.0.1:0',
    },
    stdout: 'inherit',
    stderr: 'inherit',
  });
  try {
    for (let i = 0; i < 50 && !message; i++) await Bun.sleep(100);
    expect(JSON.parse(message).id).toBe('human-1');
  } finally {
    child.kill();
    await child.exited;
    router.close();
  }
}, 10000);
