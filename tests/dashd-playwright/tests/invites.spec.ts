import { test, expect, asUser } from './_helpers';

// invites.spec — /dash/invites/ list + create + revoke.
// Seed: onbod.db exists, testadmin is operator (admin on **).

test.describe('invites', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('GET renders list (empty state OK)', async ({ page }) => {
    await page.goto('/dash/invites/');
    await expect(page.locator('h1')).toHaveText('Invites');
    const body = await page.locator('body').textContent();
    expect(body).toMatch(/No invites|Token/);
  });

  test('create invite → 303 → token appears in list', async ({
    context,
    page,
  }) => {
    const res = await context.request.post('/dash/invites/', {
      form: { target_glob: 'inbox', max_uses: '1' },
      maxRedirects: 0,
    });
    expect(res.status()).toBe(303);

    await page.goto('/dash/invites/');
    await expect(page.locator('table tbody')).toContainText('inbox');

    // extract token for cleanup
    const code = page.locator('table tbody code').first();
    const token = await code.textContent();
    expect(token).toBeTruthy();

    // cleanup: revoke
    page.on('dialog', (d) => d.accept());
    const row = page.locator('tr', { hasText: token! });
    await row.locator('button', { hasText: 'revoke' }).click();
    await page.waitForURL('**/dash/invites/');
    await expect(page.locator('body')).not.toContainText(token!);
  });

  test('revoke invite → 303 → token gone', async ({ context }) => {
    const create = await context.request.post('/dash/invites/', {
      form: { target_glob: 'inbox', max_uses: '2' },
      maxRedirects: 0,
    });
    expect(create.status()).toBe(303);

    const html = await (await context.request.get('/dash/invites/')).text();
    const m = html.match(/<code>([0-9a-f]{32,})<\/code>/);
    expect(m, 'token must appear in listing').not.toBeNull();
    const token = m![1];

    const revoke = await context.request.post(`/dash/invites/${token}/revoke`, {
      maxRedirects: 0,
    });
    expect(revoke.status()).toBe(303);

    const after = await (await context.request.get('/dash/invites/')).text();
    expect(after).not.toContain(token);
  });

  test('non-operator → 403', async ({ context }) => {
    await asUser(context, 'stranger');
    const r = await context.request.get('/dash/invites/');
    expect(r.status()).toBe(403);
  });

  test('unauthenticated → 401', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const r = await context.request.get('/dash/invites/');
    expect(r.status()).toBe(401);
  });
});
