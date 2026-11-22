import { afterEach, expect, it } from 'bun:test';
import { RouterClient } from './client.js';

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
});

/** Re-registration replaces the token and a rejected deregistration fails. */
it('deregisters with the latest registration token and reports rejection', async () => {
  let count = 0;
  let authorization = '';
  globalThis.fetch = (async (_url, init) => {
    count++;
    if (count <= 2)
      return Response.json({ ok: true, token: `registration-${count}` });
    authorization = new Headers(init?.headers).get('Authorization') ?? '';
    return new Response('denied', { status: 401 });
  }) as typeof fetch;
  const client = new RouterClient('http://router', async () => 'service-jwt');
  await client.register('whatsapp', 'http://whapd:8080');
  await client.register('whatsapp', 'http://whapd:8080');
  await expect(client.deregister()).rejects.toThrow('401');
  expect(authorization).toBe('Bearer registration-2');
});
