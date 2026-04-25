import { describe, expect, it, beforeAll, afterAll } from 'bun:test';
import { startServer } from './server';
import type { Scraper } from './twitter';

interface Call {
  method: string;
  args: unknown[];
}

function makeStub(): { sock: Scraper; calls: Call[] } {
  const calls: Call[] = [];
  const stub = {
    sendTweet: (...args: unknown[]) => {
      calls.push({ method: 'sendTweet', args });
      return Promise.resolve({
        json: async () => ({
          data: {
            create_tweet: {
              tweet_results: { result: { rest_id: 'NEW_ID' } },
            },
          },
        }),
      });
    },
    sendQuoteTweet: (...args: unknown[]) => {
      calls.push({ method: 'sendQuoteTweet', args });
      return Promise.resolve({
        json: async () => ({
          data: {
            create_tweet: {
              tweet_results: { result: { rest_id: 'QUOTE_ID' } },
            },
          },
        }),
      });
    },
    retweet: (...args: unknown[]) => {
      calls.push({ method: 'retweet', args });
      return Promise.resolve(new Response('ok'));
    },
    likeTweet: (...args: unknown[]) => {
      calls.push({ method: 'likeTweet', args });
      return Promise.resolve(new Response('ok'));
    },
    deleteTweet: (...args: unknown[]) => {
      calls.push({ method: 'deleteTweet', args });
      return Promise.resolve(new Response('ok'));
    },
    sendDirectMessage: (...args: unknown[]) => {
      calls.push({ method: 'sendDirectMessage', args });
      return Promise.resolve({});
    },
    setCookies: () => Promise.resolve(),
    getCookies: () => Promise.resolve([]),
    isLoggedIn: () => Promise.resolve(true),
    login: () => Promise.resolve(),
    getProfile: () => Promise.resolve({ username: 'me' }),
  } as unknown as Scraper;
  return { sock: stub, calls };
}

const SECRET = 'test-secret';
const PORT = 19223;
const BASE = `http://127.0.0.1:${PORT}`;

let server: ReturnType<typeof startServer>;
let stub = makeStub();
let connected = true;
let lastInboundAt = Math.floor(Date.now() / 1000);

beforeAll(() => {
  server = startServer(
    `:${PORT}`,
    SECRET,
    () => stub.sock,
    () => connected,
    () => lastInboundAt,
  );
});

afterAll(() => {
  server.close();
});

function auth() {
  return { Authorization: `Bearer ${SECRET}` };
}

describe('GET /health', () => {
  it('returns ok when connected', async () => {
    connected = true;
    lastInboundAt = Math.floor(Date.now() / 1000);
    const r = await fetch(`${BASE}/health`);
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.status).toBe('ok');
    expect(b.name).toBe('twitter');
    expect(b.jid_prefixes).toEqual(['twitter:']);
  });

  it('returns 503 disconnected when scraper not connected', async () => {
    connected = false;
    const r = await fetch(`${BASE}/health`);
    expect(r.status).toBe(503);
    const b = await r.json();
    expect(b.status).toBe('disconnected');
    connected = true;
  });

  it('returns 503 stale when no inbound within threshold', async () => {
    connected = true;
    lastInboundAt = Math.floor(Date.now() / 1000) - 10 * 60;
    const r = await fetch(`${BASE}/health`);
    expect(r.status).toBe(503);
    const b = await r.json();
    expect(b.status).toBe('stale');
    expect(typeof b.stale_seconds).toBe('number');
    lastInboundAt = Math.floor(Date.now() / 1000);
  });
});

describe('auth gate', () => {
  it('rejects missing token', async () => {
    const r = await fetch(`${BASE}/post`, {
      method: 'POST',
      body: '{}',
      headers: { 'Content-Type': 'application/json' },
    });
    expect(r.status).toBe(401);
  });

  it('rejects wrong token', async () => {
    const r = await fetch(`${BASE}/post`, {
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

describe('POST /post', () => {
  it('calls sendTweet and returns id', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/post`, {
      method: 'POST',
      body: JSON.stringify({ content: 'hello world' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.ok).toBe(true);
    expect(b.id).toBe('NEW_ID');
    const call = stub.calls.find((c) => c.method === 'sendTweet');
    expect(call).toBeTruthy();
    expect(call!.args[0]).toBe('hello world');
  });

  it('returns 502 when not connected', async () => {
    connected = false;
    const r = await fetch(`${BASE}/post`, {
      method: 'POST',
      body: JSON.stringify({ content: 'x' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(502);
    connected = true;
  });
});

describe('POST /reply', () => {
  it('calls sendTweet with replyTo id', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/reply`, {
      method: 'POST',
      body: JSON.stringify({
        reply_to: 'twitter:tweet/1234',
        content: 'reply',
      }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'sendTweet');
    expect(call).toBeTruthy();
    expect(call!.args[0]).toBe('reply');
    expect(call!.args[1]).toBe('1234');
  });

  it('accepts bare numeric id', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/reply`, {
      method: 'POST',
      body: JSON.stringify({ reply_to: '5678', content: 'r' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'sendTweet');
    expect(call!.args[1]).toBe('5678');
  });
});

describe('POST /repost', () => {
  it('calls retweet with parsed id', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/repost`, {
      method: 'POST',
      body: JSON.stringify({ target_id: 'twitter:tweet/9999' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'retweet');
    expect(call!.args[0]).toBe('9999');
  });
});

describe('POST /quote', () => {
  it('calls sendQuoteTweet with text + id', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/quote`, {
      method: 'POST',
      body: JSON.stringify({
        target_id: 'twitter:tweet/77',
        content: 'my take',
      }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.id).toBe('QUOTE_ID');
    const call = stub.calls.find((c) => c.method === 'sendQuoteTweet');
    expect(call!.args[0]).toBe('my take');
    expect(call!.args[1]).toBe('77');
  });

  it('returns 501 hint when content is empty', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/quote`, {
      method: 'POST',
      body: JSON.stringify({ target_id: 'twitter:tweet/77', content: '' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(501);
    const b = await r.json();
    expect(b.tool).toBe('quote');
    expect(String(b.hint)).toContain('repost');
  });
});

describe('POST /like', () => {
  it('calls likeTweet with parsed id', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/like`, {
      method: 'POST',
      body: JSON.stringify({ target_id: 'twitter:tweet/abc' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'likeTweet');
    expect(call!.args[0]).toBe('abc');
  });
});

describe('POST /delete', () => {
  it('calls deleteTweet', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/delete`, {
      method: 'POST',
      body: JSON.stringify({ target_id: 'twitter:tweet/del1' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'deleteTweet');
    expect(call!.args[0]).toBe('del1');
  });
});

describe('POST /send (DM)', () => {
  it('sends DM via sendDirectMessage', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: JSON.stringify({
        chat_jid: 'twitter:dm/conv-42',
        content: 'hi there',
      }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(200);
    const call = stub.calls.find((c) => c.method === 'sendDirectMessage');
    expect(call!.args[0]).toBe('conv-42');
    expect(call!.args[1]).toBe('hi there');
  });

  it('returns 502 for non-dm jid', async () => {
    stub = makeStub();
    const r = await fetch(`${BASE}/send`, {
      method: 'POST',
      body: JSON.stringify({ chat_jid: 'twitter:home', content: 'hi' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(502);
    const b = await r.json();
    expect(String(b.error)).toContain('twitter:dm');
  });
});

describe('hint-only verbs', () => {
  it('/forward returns 501 with hint', async () => {
    const r = await fetch(`${BASE}/forward`, {
      method: 'POST',
      body: '{}',
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(501);
    const b = await r.json();
    expect(b.tool).toBe('forward');
    expect(b.platform).toBe('twitter');
    expect(String(b.hint)).toContain('send');
  });

  it('/dislike returns 501 with hint', async () => {
    const r = await fetch(`${BASE}/dislike`, {
      method: 'POST',
      body: '{}',
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(501);
    const b = await r.json();
    expect(b.tool).toBe('dislike');
    expect(String(b.hint)).toContain('reply');
  });

  it('/edit returns 501 with hint', async () => {
    const r = await fetch(`${BASE}/edit`, {
      method: 'POST',
      body: '{}',
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(501);
    const b = await r.json();
    expect(b.tool).toBe('edit');
    expect(String(b.hint)).toContain('delete');
  });
});

describe('upstream errors', () => {
  it('returns 502 when sendTweet throws', async () => {
    const calls: Call[] = [];
    stub.sock = {
      sendTweet: (...args: unknown[]) => {
        calls.push({ method: 'sendTweet', args });
        return Promise.reject(new Error('rate limited'));
      },
    } as unknown as Scraper;
    connected = true;
    const r = await fetch(`${BASE}/post`, {
      method: 'POST',
      body: JSON.stringify({ content: 'x' }),
      headers: { 'Content-Type': 'application/json', ...auth() },
    });
    expect(r.status).toBe(502);
    const b = await r.json();
    expect(String(b.error)).toContain('rate limited');
    stub = makeStub();
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
