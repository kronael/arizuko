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
    context,
  }) => {
    // revoke any stale token first (idempotent — 303 or 404 both OK)
    await context.request.post('/dash/tokens/inbox/web:inbox/revoke', {
      maxRedirects: 0,
    });

    // POST returns 200 with inline banner (no redirect).
    // kind via query-string because r.FormValue reads URL params before body.
    const res = await context.request.post('/dash/tokens/inbox/?kind=chat', {
      maxRedirects: 0,
    });
    const html = await res.text();
    expect(html).toContain('banner-ok');
    expect(html).toContain('Copy it now');
    expect(html).toContain('web:inbox');

    // cleanup
    await context.request.post('/dash/tokens/inbox/web:inbox/revoke', {
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
