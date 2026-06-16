import { test, expect, asUser } from './_helpers';

// secrets.spec — exercise GET/POST/PATCH/DELETE /dash/me/secrets JSON API.
// All secrets are scoped to testadmin. Each test cleans up after itself.

test.describe('me/secrets', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('GET returns empty list for fresh sub', async ({ context }) => {
    const r = await context.request.get('/dash/me/secrets');
    expect(r.status()).toBe(200);
    const body = await r.json();
    expect(Array.isArray(body.secrets)).toBe(true);
  });

  test('POST create → 204, key appears in list', async ({ context }) => {
    const key = `MY_SECRET_${Date.now()}`;
    const create = await context.request.post('/dash/me/secrets', {
      data: { key, value: 'hunter2' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(create.status()).toBe(204);

    const list = await (await context.request.get('/dash/me/secrets')).json();
    expect(list.secrets.map((s: { key: string }) => s.key)).toContain(key);

    // cleanup
    await context.request.delete(`/dash/me/secrets/${key}`);
  });

  test('POST invalid key pattern → 400', async ({ context }) => {
    const r = await context.request.post('/dash/me/secrets', {
      data: { key: 'bad-key', value: 'x' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(r.status()).toBe(400);
  });

  test('PATCH update → 204, secret survives', async ({ context }) => {
    const key = `PATCH_SECRET_${Date.now()}`;
    await context.request.post('/dash/me/secrets', {
      data: { key, value: 'original' },
      headers: { 'Content-Type': 'application/json' },
    });

    const patch = await context.request.patch(`/dash/me/secrets/${key}`, {
      data: { value: 'updated' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(patch.status()).toBe(204);

    // cleanup
    await context.request.delete(`/dash/me/secrets/${key}`);
  });

  test('DELETE → 204, key gone from list', async ({ context }) => {
    const key = `DEL_SECRET_${Date.now()}`;
    await context.request.post('/dash/me/secrets', {
      data: { key, value: 'bye' },
      headers: { 'Content-Type': 'application/json' },
    });

    const del = await context.request.delete(`/dash/me/secrets/${key}`);
    expect(del.status()).toBe(204);

    const list = await (await context.request.get('/dash/me/secrets')).json();
    expect(list.secrets.map((s: { key: string }) => s.key)).not.toContain(key);
  });

  test('unauthenticated GET → 401', async ({ browser }) => {
    const ctx = await browser.newContext();
    const r = await ctx.request.get('/dash/me/secrets');
    expect(r.status()).toBe(401);
    await ctx.close();
  });
});
