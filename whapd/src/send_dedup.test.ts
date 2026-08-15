// The L32 case over the real /send path: routd resends a still-pending row,
// and whapd must not post a second WhatsApp message.

import { afterAll, beforeAll, describe, expect, it } from 'bun:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { DedupLedger } from './dedup';
import { startServer } from './server';

const PORT = 19127;
const BASE = `http://127.0.0.1:${PORT}`;

let sends: unknown[][] = [];
let sendErr: Error | null = null;
let connected = true;
let server: ReturnType<typeof startServer>;
let ledgerDir = '';

const sock = {
  sendMessage: (...args: unknown[]) => {
    if (sendErr) return Promise.reject(sendErr);
    sends.push(args);
    return Promise.resolve({ key: { id: 'wa-' + sends.length } });
  },
  sendPresenceUpdate: () => Promise.resolve(),
} as any;

beforeAll(() => {
  ledgerDir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-send-'));
  server = startServer(
    `:${PORT}`,
    // verifier null = local-dev open gate, mirroring Go chanlib.Auth at ks==nil.
    null,
    () => sock,
    () => connected,
    () => {},
    () => Math.floor(Date.now() / 1000),
    undefined,
    new DedupLedger(ledgerDir),
  );
});

afterAll(() => {
  server.close();
  fs.rmSync(ledgerDir, { recursive: true, force: true });
});

function send(body: Record<string, unknown>) {
  return fetch(`${BASE}/send`, {
    method: 'POST',
    body: JSON.stringify(body),
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('/send dedup', () => {
  it('posts once for a repeated idempotency_key', async () => {
    sends = [];
    const body = {
      chat_jid: 'whatsapp:1@s.whatsapp.net',
      content: 'hello',
      idempotency_key: 'out-1',
    };

    const first = await send(body);
    expect(first.status).toBe(200);
    expect(await first.json()).toMatchObject({ ok: true, id: 'wa-1' });

    // routd's sweep re-sends the same row.
    const second = await send(body);
    expect(second.status).toBe(200);
    expect(await second.json()).toMatchObject({ ok: true, id: 'wa-1' });

    expect(sends.length).toBe(1);
  });

  it('posts every time when no key is supplied', async () => {
    sends = [];
    const body = { chat_jid: 'whatsapp:2@s.whatsapp.net', content: 'hi' };
    expect((await send(body)).status).toBe(200);
    expect((await send(body)).status).toBe(200);
    expect(sends.length).toBe(2);
  });

  // A lost response leaves the claim inflight. The retry answers 409, not 502:
  // routd dead-letters an unresolved delivery for an operator instead of
  // burning its retry budget on the same refusal (spec 5/34).
  it('answers 409 for a redelivery that never settled', async () => {
    sends = [];
    const body = {
      chat_jid: 'whatsapp:3@s.whatsapp.net',
      content: 'x',
      idempotency_key: 'out-3',
    };
    sendErr = new Error('ack never arrived');
    expect((await send(body)).status).toBe(502);
    sendErr = null;

    const retry = await send(body);
    expect(retry.status).toBe(409);
    expect(sends.length).toBe(0);
  });

  // A link that is down proves nothing was sent, so it must be refused BEFORE
  // a claim exists — otherwise every routd restart would turn a plain
  // transient into a permanent unresolved dead-letter.
  it('records no claim when the socket is down', async () => {
    sends = [];
    const body = {
      chat_jid: 'whatsapp:4@s.whatsapp.net',
      content: 'y',
      idempotency_key: 'out-4',
    };
    connected = false;
    expect((await send(body)).status).toBe(502);
    connected = true;

    const retry = await send(body);
    expect(retry.status).toBe(200);
    expect(sends.length).toBe(1);
  });
});
