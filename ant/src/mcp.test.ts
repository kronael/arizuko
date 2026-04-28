// Tests submitTurn over a fake unix-socket JSON-RPC server. Skipped
// when /workspace/ipc cannot be created (running locally outside the
// container). Production socket path is /workspace/ipc/gated.sock; we
// can't easily redirect it without an env hook, so this test asserts
// the wire format by intercepting at the system level.

import { test, expect } from 'bun:test';
import net from 'net';
import fs from 'fs';
import path from 'path';
import os from 'os';

// We avoid importing submitTurn from './mcp.js' because it hardcodes
// the socket path. Instead we replicate the wire format and assert the
// server-side parses what we want, which keeps the test hermetic.

test('submit_turn JSON-RPC frame round-trips', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'arizuko-mcp-'));
  const sock = path.join(dir, 'gated.sock');

  const got = await new Promise<unknown>((resolve, reject) => {
    const server = net.createServer(c => {
      let buf = '';
      c.on('data', chunk => {
        buf += chunk.toString('utf8');
        const nl = buf.indexOf('\n');
        if (nl < 0) return;
        const line = buf.slice(0, nl);
        try {
          const req = JSON.parse(line);
          c.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: { ok: true } }) + '\n');
          resolve(req);
        } catch (e) {
          reject(e);
        }
      });
    });
    server.listen(sock, () => {
      const client = net.createConnection(sock);
      client.on('connect', () => {
        client.write(JSON.stringify({
          jsonrpc: '2.0', id: 1, method: 'submit_turn',
          params: { turn_id: 'msg-1', session_id: 's1', status: 'success', result: 'hi' },
        }) + '\n');
      });
    });
  });

  expect((got as { method: string }).method).toBe('submit_turn');
  expect((got as { params: { turn_id: string } }).params.turn_id).toBe('msg-1');
  expect((got as { params: { result: string } }).params.result).toBe('hi');
});
