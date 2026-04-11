import { describe, expect, it, beforeAll, afterAll } from 'bun:test';
import { startServer } from './server';
import { flushQueue } from './queue';
import { extractReplyMeta } from './reply';
import type { WAMessage } from '@whiskeysockets/baileys';

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
let typingCalls: { jid: string; on: boolean }[] = [];

beforeAll(() => {
  server = startServer(
    `:${PORT}`,
    SECRET,
    () => stub.sock,
    () => connected,
    () => {},
    (jid, on) => {
      typingCalls.push({ jid, on });
    },
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
  it('forwards on=true to setTyping with normalized jid', async () => {
    typingCalls = [];
    const r = await fetch(`${BASE}/typing`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', on: true }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    expect(typingCalls).toEqual([{ jid: '12345@s.whatsapp.net', on: true }]);
  });

  it('forwards on=false to setTyping', async () => {
    typingCalls = [];
    const r = await fetch(`${BASE}/typing`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', on: false }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    expect(typingCalls).toEqual([{ jid: '12345@s.whatsapp.net', on: false }]);
  });

  it('passes already-suffixed jids through unchanged', async () => {
    typingCalls = [];
    const r = await fetch(`${BASE}/typing`, {
      method: 'POST',
      body: JSON.stringify({
        chat_jid: 'whatsapp:12345@g.us',
        on: true,
      }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    expect(typingCalls).toEqual([{ jid: '12345@g.us', on: true }]);
  });
});

describe('POST /send-file auth', () => {
  it('rejects missing token', async () => {
    const fd = new globalThis.FormData();
    fd.append('chat_jid', 'whatsapp:12345');
    fd.append('file', new Blob(['data']), 'photo.jpg');
    const r = await fetch(`${BASE}/send-file`, {
      method: 'POST',
      body: fd,
    });
    expect(r.status).toBe(401);
  });
});

describe('unknown route', () => {
  it('returns 404', async () => {
    const r = await fetch(`${BASE}/nope`, {
      method: 'GET',
      headers: { ...auth() },
    });
    expect(r.status).toBe(404);
  });
});

describe('POST /send-file document MIME', () => {
  it('sends pdf as document', async () => {
    stub = makeStub();
    connected = true;
    const fd = new globalThis.FormData();
    fd.append('chat_jid', 'whatsapp:12345');
    fd.append('filename', 'report.pdf');
    fd.append('file', new Blob(['pdfdata']), 'report.pdf');
    const r = await fetch(`${BASE}/send-file`, {
      method: 'POST',
      body: fd,
      headers: { ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'sendMessage');
    expect(call).toBeTruthy();
    const content = call!.args[1] as Record<string, unknown>;
    expect(content['document']).toBeTruthy();
    expect(content['mimetype']).toBe('application/pdf');
    expect(content['fileName']).toBe('report.pdf');
  });
});

function makeThrowingStub() {
  const calls: { method: string; args: unknown[] }[] = [];
  const sock = {
    sendMessage: (...args: unknown[]) => {
      calls.push({ method: 'sendMessage', args });
      return Promise.reject(new Error('upstream boom'));
    },
    sendPresenceUpdate: (...args: unknown[]) => {
      calls.push({ method: 'sendPresenceUpdate', args });
      return Promise.resolve();
    },
  } as any;
  return { sock, calls };
}

describe('POST /send queues on sendMessage error', () => {
  it('falls through to queued response when upstream throws', async () => {
    const throwing = makeThrowingStub();
    stub.sock = throwing.sock;
    connected = true;
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', content: 'hi' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.ok).toBe(true);
    expect(b.queued).toBe(true);
    expect(throwing.calls.some((c) => c.method === 'sendMessage')).toBe(true);
    stub = makeStub();
  });
});

describe('POST /typing auth', () => {
  it('rejects missing token', async () => {
    const r = await fetch(`${BASE}/typing`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'whatsapp:12345', on: true }),
      headers: { 'Content-Type': 'application/json' },
    });
    expect(r.status).toBe(401);
  });
});

describe('POST /send-file upstream error', () => {
  it('returns 502 when sendMessage throws', async () => {
    const throwing = makeThrowingStub();
    stub.sock = throwing.sock;
    connected = true;
    const fd = new globalThis.FormData();
    fd.append('chat_jid', 'whatsapp:12345');
    fd.append('filename', 'photo.jpg');
    fd.append('file', new Blob(['imgdata']), 'photo.jpg');
    const r = await fetch(`${BASE}/send-file`, {
      method: 'POST',
      body: fd,
      headers: { ...auth() },
    });
    expect(r.status).toBe(502);
    const b = await r.json();
    expect(b.ok).toBe(false);
    expect(b.error).toContain('upstream boom');
    stub = makeStub();
  });
});

function wa(message: Record<string, unknown>): WAMessage {
  return { key: { id: 'k1' }, message } as unknown as WAMessage;
}

describe('extractReplyMeta', () => {
  it('returns undefined when message body is missing', () => {
    expect(
      extractReplyMeta({ key: { id: 'k1' } } as WAMessage),
    ).toBeUndefined();
  });

  it('returns undefined for plain text without contextInfo', () => {
    expect(extractReplyMeta(wa({ conversation: 'hello' }))).toBeUndefined();
  });

  it('extracts reply fields for text reply to text', () => {
    const meta = extractReplyMeta(
      wa({
        extendedTextMessage: {
          text: 'reply body',
          contextInfo: {
            stanzaId: 'STANZA1',
            participant: '12345@s.whatsapp.net',
            quotedMessage: { conversation: 'original' },
          },
        },
      }),
    );
    expect(meta).toEqual({
      replyTo: 'STANZA1',
      replyToText: 'original',
      replyToSender: 'whatsapp:12345@s.whatsapp.net',
    });
  });

  it('uses image caption when replying to captioned image', () => {
    const meta = extractReplyMeta(
      wa({
        extendedTextMessage: {
          text: 'nice pic',
          contextInfo: {
            stanzaId: 'S2',
            participant: 'p@s',
            quotedMessage: { imageMessage: { caption: 'sunset' } },
          },
        },
      }),
    );
    expect(meta?.replyToText).toBe('sunset');
  });

  it('falls back to [Image] when quoted image has no caption', () => {
    const meta = extractReplyMeta(
      wa({
        extendedTextMessage: {
          text: 'cool',
          contextInfo: {
            stanzaId: 'S3',
            participant: 'p@s',
            quotedMessage: { imageMessage: {} },
          },
        },
      }),
    );
    expect(meta?.replyToText).toBe('[Image]');
  });

  it('omits replyToSender when participant is missing', () => {
    const meta = extractReplyMeta(
      wa({
        extendedTextMessage: {
          text: 'r',
          contextInfo: {
            stanzaId: 'S4',
            quotedMessage: { conversation: 'q' },
          },
        },
      }),
    );
    expect(meta).toEqual({ replyTo: 'S4', replyToText: 'q' });
    expect(meta?.replyToSender).toBeUndefined();
  });

  it('passes stanzaId through verbatim', () => {
    const meta = extractReplyMeta(
      wa({
        extendedTextMessage: {
          text: 'r',
          contextInfo: {
            stanzaId: '3EB0ABCDEF1234567890',
            quotedMessage: { conversation: 'q' },
          },
        },
      }),
    );
    expect(meta?.replyTo).toBe('3EB0ABCDEF1234567890');
  });

  it('reads contextInfo from imageMessage wrapper', () => {
    const meta = extractReplyMeta(
      wa({
        imageMessage: {
          caption: 'my reply',
          contextInfo: {
            stanzaId: 'S5',
            participant: 'p@s',
            quotedMessage: { conversation: 'parent' },
          },
        },
      }),
    );
    expect(meta?.replyTo).toBe('S5');
    expect(meta?.replyToText).toBe('parent');
  });
});

describe('flushQueue', () => {
  it('returns empty result for empty queue', async () => {
    const q: string[] = [];
    const r = await flushQueue(q, async () => {});
    expect(r.sent).toEqual([]);
    expect(r.failed).toEqual([]);
    expect(q.length).toBe(0);
  });

  it('sends all items when sendFn succeeds', async () => {
    const q = ['a', 'b', 'c'];
    const seen: string[] = [];
    const r = await flushQueue(q, async (item) => {
      seen.push(item);
    });
    expect(seen).toEqual(['a', 'b', 'c']);
    expect(r.sent).toEqual(['a', 'b', 'c']);
    expect(r.failed).toEqual([]);
    expect(q.length).toBe(0);
  });

  it('continues past a mid-batch failure', async () => {
    const q = [
      { jid: '1', text: 'one' },
      { jid: '2', text: 'two' },
      { jid: '3', text: 'three' },
    ];
    const seen: string[] = [];
    const r = await flushQueue(q, async (item) => {
      seen.push(item.jid);
      if (item.jid === '2') throw new Error('boom');
    });
    expect(seen).toEqual(['1', '2', '3']);
    expect(r.sent.map((i) => i.jid)).toEqual(['1', '3']);
    expect(r.failed.length).toBe(1);
    expect(r.failed[0]!.item.jid).toBe('2');
    expect(r.failed[0]!.err.message).toBe('boom');
    expect(q.length).toBe(0);
  });

  it('wraps non-Error throws into Error', async () => {
    const q = ['x'];
    const r = await flushQueue(q, async () => {
      throw 'string-error';
    });
    expect(r.sent).toEqual([]);
    expect(r.failed[0]!.err).toBeInstanceOf(Error);
    expect(r.failed[0]!.err.message).toBe('string-error');
  });
});
