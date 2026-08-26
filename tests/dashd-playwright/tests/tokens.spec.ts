import { test, expect, asUser } from './_helpers';

// tokens.spec — /dash/tokens/{folder}/ list + revoke.
//
// dashd no longer MINTS tokens: the form was deleted 2026-08-15 (BUGS V2) on
// the rule that a token is minted where it is owned — routd's
// /v1/route_tokens, or `arizuko token`. This page lists and revokes, and says
// where to mint. The suite kept testing the deleted form for months and read
// as green because three of its assertions failed for the same stale reason.

test.describe('route tokens', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('GET lists the folder tokens and points at where minting happens', async ({
    page,
  }) => {
    await page.goto('/dash/tokens/inbox/');
    await expect(page.locator('h1')).toHaveText('Tokens — inbox');
    const body = page.locator('body');
    await expect(body).toContainText('Issuing a token');
    await expect(body).toContainText('arizuko token');
    // The seeded chat token must be listed.
    await expect(body).toContainText('web:inbox');
  });

  test('the page offers no mint control', async ({ page }) => {
    await page.goto('/dash/tokens/inbox/');
    // Guards the V2 decision: a re-added mint form here would be a second
    // renderer for a credential routd owns.
    await expect(
      page.locator('form[action="/dash/tokens/inbox/"]'),
    ).toHaveCount(0);
    await expect(page.locator('body')).not.toContainText('Copy it now');
  });

  test('POST to the list is not a mint route', async ({ context }) => {
    const r = await context.request.post('/dash/tokens/inbox/?kind=chat', {
      maxRedirects: 0,
    });
    expect(r.status()).toBe(405);
  });

  test('revoke removes the jid from the list', async ({ context }) => {
    const before = await (
      await context.request.get('/dash/tokens/inbox/')
    ).text();
    expect(before).toContain('web:inbox');

    const revoke = await context.request.post(
      '/dash/tokens/inbox/web:inbox/revoke',
      {
        maxRedirects: 0,
      },
    );
    expect(revoke.status()).toBe(303);

    const after = await (
      await context.request.get('/dash/tokens/inbox/')
    ).text();
    expect(after).not.toContain('web:inbox');
  });

  test('a caller with no grant on the folder cannot revoke', async ({
    context,
  }) => {
    await asUser(context, 'stranger');
    const r = await context.request.post(
      '/dash/tokens/inbox/web:inbox/revoke',
      {
        maxRedirects: 0,
      },
    );
    expect(r.status()).toBe(403);
  });

  test('unauthenticated → 401', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const r = await context.request.get('/dash/tokens/inbox/');
    expect(r.status()).toBe(401);
  });
});
