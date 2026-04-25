import { describe, expect, it, beforeAll, afterAll } from 'bun:test';
import { startServer } from '../server';
import type { Scraper } from '../twitter';

// Integration test: prove the handler chain end-to-end with a stubbed
// Scraper for a typical "post a tweet" round-trip.

const SECRET = 'handler-twitd-secret';
const PORT = 19224;
const BASE = `http://127.0.0.1:${PORT}`;

interface Call {
  method: string;
  args: unknown[];
}

const calls: Call[] = [];

const scraper = {
  sendTweet: (...args: unknown[]) => {
    calls.push({ method: 'sendTweet', args });
    return Promise.resolve({
      json: async () => ({
        data: {
          create_tweet: {
            tweet_results: { result: { rest_id: 'TWEET_42' } },
          },
        },
      }),
    });
  },
} as unknown as Scraper;

let server: ReturnType<typeof startServer>;

beforeAll(() => {
  server = startServer(
    `:${PORT}`,
    SECRET,
    () => scraper,
    () => true,
    () => Math.floor(Date.now() / 1000),
  );
});

afterAll(() => {
  server.close();
});

describe('post handler', () => {
  it('delivers a tweet and returns the new id', async () => {
    const r = await fetch(`${BASE}/post`, {
      method: 'POST',
      body: JSON.stringify({ content: 'hello x' }),
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${SECRET}`,
      },
    });
    expect(r.status).toBe(200);
    const b = await r.json();
    expect(b.ok).toBe(true);
    expect(b.id).toBe('TWEET_42');
    expect(calls.length).toBe(1);
    expect(calls[0]!.args[0]).toBe('hello x');
  });
});
