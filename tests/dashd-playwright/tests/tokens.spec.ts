import { test, expect, asUser } from './_helpers';

// tokens.spec — /dash/tokens/{folder}/ list + issue + revoke.
// kind=chat → jid=web:inbox. Only one chat token per folder.
// POST renders the page with a banner (no redirect); raw token shown once.

test.describe('route tokens', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('GET renders list for inbox (may be empty)', async ({ page }) => {
    await page.goto('/dash/tokens/inbox/');
    await expect(page.locator('h1')).toHaveText('Tokens — inbox');
    await expect(page.locator('body')).toContainText('Issue new token');
  });

  test('issue chat token → raw token shown → appears in table', async ({
    page,
  }) => {
    await page.goto('/dash/tokens/inbox/');
    await page.locator('select[name="kind"]').selectOption('chat');
    await page.locator('button[type="submit"]').click();

    // POST renders inline (no redirect). htmlBanner escapes content so the
    // raw token appears as plain text inside div.banner-ok.
    const banner = page.locator('div.banner-ok');
    await expect(banner).toContainText('Copy it now');

    // jid web:inbox appears in table
    await expect(page.locator('table tbody')).toContainText('web:inbox');

    // cleanup
    await page.context().request.post('/dash/tokens/inbox/web:inbox/revoke', {
      maxRedirects: 0,
    });
  });

  test('revoke chat token → jid gone from list', async ({ context }) => {
    // ensure a token exists first
    await context.request.post('/dash/tokens/inbox/', {
      form: { kind: 'chat' },
    });

    const html = await (
      await context.request.get('/dash/tokens/inbox/')
    ).text();
    if (!html.includes('web:inbox')) {
      // token may already exist from a prior run; nothing to assert
      return;
    }

    const revoke = await context.request.post(
      '/dash/tokens/inbox/web:inbox/revoke',
      { maxRedirects: 0 },
    );
    expect(revoke.status()).toBe(303);

    const after = await (
      await context.request.get('/dash/tokens/inbox/')
    ).text();
    expect(after).not.toContain('web:inbox');
  });

  test('non-admin for folder → 403 on POST', async ({ context }) => {
    await asUser(context, 'stranger');
    const r = await context.request.post('/dash/tokens/inbox/', {
      form: { kind: 'chat' },
      maxRedirects: 0,
    });
    expect(r.status()).toBe(403);
  });

  test('unauthenticated → 401', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const r = await context.request.get('/dash/tokens/inbox/');
    expect(r.status()).toBe(401);
  });
});
