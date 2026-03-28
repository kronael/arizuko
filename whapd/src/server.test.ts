import { describe, expect, it, beforeAll, afterAll } from 'bun:test';
import { startServer } from './server';

// Minimal WASocket stub
function makeStub() {
  const calls: { method: string; args: unknown[] }[] = [];
  const sock = {
    sendMessage: (...args: unknown[]) => {
      calls.push({ method: 'sendMessage', args });
      return Promise.resolve({});
    },
    sendPresenceUpdate: (...args: unknown[]) => {
      calls.push({ method: 'sendPresenceUpdate', args });
      return Promise.resolve();
    },
  } as any;
  return { sock, calls };
}

const SECRET = 'test-secret';
const PORT = 19123;
const BASE = `http://127.0.0.1:${PORT}`;

let server: ReturnType<typeof startServer>;
let stub = makeStub();
let connected = true;

beforeAll(() => {
  server = startServer(
    `:${PORT}`,
    SECRET,
    () => stub.sock,
    () => connected,
    () => {},
  );
});

afterAll(() => {
  server.close();
});

function auth() {
  return { Authorization: `Bearer ${SECRET}` };
}

describe('GET /health', () => {
  it('returns ok without auth', async () => {
    const r = await fetch(`${BASE}/health`);
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.status).toBe('ok');
    expect(b.name).toBe('whatsapp');
  });
});

describe('auth gate', () => {
  it('rejects missing token', async () => {
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: '{}',
      headers: { 'Content-Type': 'application/json' },
    });
    expect(r.status).toBe(401);
  });

  it('rejects wrong token', async () => {
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: '{}',
      headers: {
        'Content-Type': 'application/json',
        Authorization: 'Bearer wrong',
      },
    });
    expect(r.status).toBe(401);
  });
});

describe('POST /send', () => {
  it('queues when not connected', async () => {
    connected = false;
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', content: 'hello' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.queued).toBe(true);
    connected = true;
  });

  it('sends when connected', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', content: 'hello' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.ok).toBe(true);
    expect(b.queued).toBeUndefined();
    expect(stub.calls.some((c) => c.method === 'sendMessage')).toBe(true);
  });
});

describe('POST /send-file', () => {
  it('returns 502 when not connected', async () => {
    connected = false;
    const fd = new globalThis.FormData();
    fd.append('chat_jid', 'whatsapp:12345');
    fd.append('filename', 'photo.jpg');
    fd.append('file', new Blob(['data']), 'photo.jpg');
    const r = await fetch(`${BASE}/send-file`, {
      method: 'POST',
      body: fd,
      headers: { ...auth() },
    });
    expect(r.status).toBe(502);
    connected = true;
  });

  it('sends image file when connected', async () => {
    stub = makeStub();
    const fd = new globalThis.FormData();
    fd.append('chat_jid', 'whatsapp:12345');
    fd.append('filename', 'photo.jpg');
    fd.append('file', new Blob(['imgdata']), 'photo.jpg');
    const r = await fetch(`${BASE}/send-file`, {
      method: 'POST',
      body: fd,
      headers: { ...auth() },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.ok).toBe(true);
    const call = stub.calls.find((c) => c.method === 'sendMessage');
    expect(call).toBeTruthy();
  });

  it('returns 400 when chat_jid missing', async () => {
    const fd = new globalThis.FormData();
    fd.append('filename', 'photo.jpg');
    fd.append('file', new Blob(['data']), 'photo.jpg');
    const r = await fetch(`${BASE}/send-file`, {
      method: 'POST',
      body: fd,
      headers: { ...auth() },
    });
    expect(r.status).toBe(400);
  });
});

describe('POST /typing', () => {
  it('sends presence update when connected', async () => {
    stub = makeStub();
    connected = true;
    const r = await fetch(`${BASE}/typing`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', on: true }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    expect(stub.calls.some((c) => c.method === 'sendPresenceUpdate')).toBe(
      true,
    );
  });
});
