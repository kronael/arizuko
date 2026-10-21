import { test, expect } from 'bun:test';
import net from 'net';
import fs from 'fs';
import path from 'path';
import os from 'os';
import { checkTurn, submitTurn, rpc } from './mcp.js';

/** The real client reads daemon decisions and leaves assistant prose off the submission wire. */
test('turn client returns the nudge over the actual socket implementation', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'arizuko-mcp-'));
  const sock = path.join(dir, 'gated.sock');
  const requests: Array<{ method: string; params: Record<string, unknown> }> = [];
  const server = net.createServer(client => {
    let buffer = '';
    client.on('data', chunk => {
      buffer += chunk.toString('utf8');
      if (!buffer.includes('\n')) return;
      const req = JSON.parse(buffer.slice(0, buffer.indexOf('\n')));
      requests.push(req);
      client.end(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: { ok: true, nudge: 'deliver', owed: true, delivered: false } }) + '\n');
    });
  });
  await new Promise<void>(resolve => server.listen(sock, resolve));
  try {
    expect(await submitTurn({ turn_id: 't1', session_id: 's1', status: 'success' }, sock)).toEqual({ nudge: 'deliver' });
    expect(await checkTurn('t1', sock)).toEqual({ nudge: 'deliver' });
    expect(requests.map(req => req.method)).toEqual(['submit_turn', 'check_turn']);
    expect(requests[0].params).toEqual({ turn_id: 't1', session_id: 's1', status: 'success' });
    expect(requests[0].params).not.toHaveProperty('result');
    expect(requests[1].params).toEqual({ turn_id: 't1' });
  } finally {
    await new Promise<void>(resolve => server.close(() => resolve()));
    if (fs.existsSync(sock)) fs.unlinkSync(sock);
    fs.rmdirSync(dir);
  }
});

/** An RPC error rejects rather than silently accepting a missing completion decision. */
test('turn client rejects an RPC error', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'arizuko-mcp-'));
  const sock = path.join(dir, 'gated.sock');
  const server = net.createServer(client => client.once('data', chunk => {
    const req = JSON.parse(chunk.toString());
    client.end(JSON.stringify({ jsonrpc: '2.0', id: req.id, error: { code: -32603, message: 'unavailable' } }) + '\n');
  }));
  await new Promise<void>(resolve => server.listen(sock, resolve));
  try {
    await expect(rpc('check_turn', { turn_id: 't1' }, sock)).rejects.toThrow('unavailable');
  } finally {
    await new Promise<void>(resolve => server.close(() => resolve()));
    if (fs.existsSync(sock)) fs.unlinkSync(sock);
    fs.rmdirSync(dir);
  }
});
