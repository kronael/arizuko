import { test, expect, asUser } from './_helpers';

// env.spec — /dash/me/env, the caller's own model-key profile
// (ANTHROPIC_API_KEY and friends). Sibling of me/secrets: same identity
// binding, same sealed-at-rest rule, different consumer — these keys are handed
// to the agent runtime, not to a capability tool.
//
// The whole surface depends on SECRETS_KEY being configured; without it dashd
// refuses to write rather than storing a key in plaintext, which is the right
// behaviour and the reason the harness now sets one.

// The key name is not free-form: dashd accepts only store.EnvProfileKeys, the
// canonical model-credential names, and rejects anything else with 400. A test
// that invents a name is testing the allow-list, not the CRUD.
const KEY = 'OPENAI_API_KEY';
const OTHER = 'CODEX_API_KEY';

test.describe('me/env', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('GET renders the key list', async ({ page }) => {
    const resp = await page.goto('/dash/me/env');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('body')).toContainText('API key');
  });

  test('create → the key appears, its value never does', async ({
    context,
  }) => {
    const k = KEY;
    const create = await context.request.post('/dash/me/env', {
      data: { key: k, value: 'sk-ant-secret-value' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(create.status()).toBe(204);

    const html = await (await context.request.get('/dash/me/env')).text();
    expect(html).toContain(k);
    // A read surface must never echo the material back.
    expect(html, 'the value must never be rendered').not.toContain(
      'sk-ant-secret-value',
    );

    await context.request.delete(`/dash/me/env/${k}`);
  });

  test('update replaces the value in place', async ({ context }) => {
    const k = KEY;
    await context.request.post('/dash/me/env', {
      data: { key: k, value: 'first' },
      headers: { 'Content-Type': 'application/json' },
    });
    const patch = await context.request.patch(`/dash/me/env/${k}`, {
      data: { value: 'second' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(patch.status()).toBe(204);

    const html = await (await context.request.get('/dash/me/env')).text();
    expect(html).toContain(k);
    await context.request.delete(`/dash/me/env/${k}`);
  });

  test('delete removes the key', async ({ context }) => {
    const k = KEY;
    await context.request.post('/dash/me/env', {
      data: { key: k, value: 'v' },
      headers: { 'Content-Type': 'application/json' },
    });
    const del = await context.request.delete(`/dash/me/env/${k}`);
    expect(del.status()).toBe(204);

    const html = await (await context.request.get('/dash/me/env')).text();
    expect(html).not.toContain(k);
  });

  test("one user cannot read another user's keys", async ({ context }) => {
    const k = OTHER;
    await context.request.post('/dash/me/env', {
      data: { key: k, value: 'v' },
      headers: { 'Content-Type': 'application/json' },
    });

    await asUser(context, 'testuser');
    const html = await (await context.request.get('/dash/me/env')).text();
    expect(html, 'env keys are identity-bound').not.toContain(k);

    await asUser(context, 'testadmin');
    await context.request.delete(`/dash/me/env/${k}`);
  });

  test('a name outside the model-credential list is refused', async ({
    context,
  }) => {
    const r = await context.request.post('/dash/me/env', {
      data: { key: 'NOT_A_MODEL_KEY', value: 'v' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(r.status()).toBe(400);
  });

  test('unauthenticated → 401', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const r = await context.request.get('/dash/me/env');
    expect(r.status()).toBe(401);
  });
});
