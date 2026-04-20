import { describe, expect, it, beforeAll, afterAll } from 'bun:test';
import { startServer } from '../server';

// Integration test: POSTs /send to the real HTTP handler with a stubbed
// WASocket. Proves the handler forwards (to, text) to sendMessage with the
// normalized JID and returns ok=true (not queued) when connected.

const SECRET = 'handler-test-secret';
const PORT = 19124;
const BASE = `http://127.0.0.1:${PORT}`;

interface Call {
  to: string;
  content: Record<string, unknown>;
}

let server: ReturnType<typeof startServer>;
const calls: Call[] = [];

const sock = {
  sendMessage: (to: string, content: Record<string, unknown>) => {
    calls.push({ to, content });
    return Promise.resolve({});
  },
} as any;

beforeAll(() => {
  server = startServer(
    `:${PORT}`,
    SECRET,
    () => sock,
    () => true,
    () => {
      throw new Error('should not queue when connected');
    },
    () => {},
  );
});

afterAll(() => {
  server.close();
});

describe('send handler', () => {
  it('delivers to WhatsApp client with normalized jid and markdown conversion', async () => {
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: JSON.stringify({
        chat_jid: 'whatsapp:42',
        content: 'hello **world**',
      }),
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${SECRET}`,
      },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.ok).toBe(true);
    expect(b.queued).toBeUndefined();

    expect(calls.length).toBe(1);
    expect(calls[0]!.to).toBe('42@s.whatsapp.net');
    expect(calls[0]!.content['text']).toBe('hello *world*');
  });
});
