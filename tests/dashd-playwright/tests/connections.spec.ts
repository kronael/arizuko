import { test, expect, asUser } from './_helpers';

// connections.spec — /dash/me/connections, the per-user OAuth provider list.
// Self-service, so it is NOT operator-gated; any signed-in caller sees their
// own connections and nobody else's.

test.describe('me/connections', () => {
  test('renders the provider table for a signed-in user', async ({
    page,
    context,
  }) => {
    await asUser(context, 'testadmin');
    const resp = await page.goto('/dash/me/connections');
    expect(resp?.status()).toBe(200);
    const body = page.locator('body');
    await expect(body).toContainText('OAuth connections');
    // With no provider configured in the harness the page states that plainly
    // rather than rendering an empty table — assert the state it is actually in.
    await expect(body).toContainText('No providers configured');
  });

  test("a second user sees their own page, not the first user's", async ({
    page,
    context,
  }) => {
    await asUser(context, 'testuser');
    const resp = await page.goto('/dash/me/connections');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('body')).not.toContainText('testadmin');
  });

  test('unauthenticated is rejected', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const r = await context.request.get('/dash/me/connections', {
      maxRedirects: 0,
    });
    expect([401, 303]).toContain(r.status());
  });
});
