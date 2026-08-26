import { test, expect, asUser } from './_helpers';

// profile.spec — exercise GET /dash/profile/ HTML rendering.
// testadmin has no provider prefix → all 4 link buttons appear.

test.describe('profile page', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('renders canonical sub', async ({ page }) => {
    await page.goto('/dash/profile/');
    await expect(page.locator('code')).toContainText('testadmin');
  });

  test('the provider section is present', async ({ page }) => {
    await page.goto('/dash/profile/');
    // The heading is "Add a provider" since the page was reworded; the test
    // still asserts the SECTION exists, not the old wording.
    await expect(page.locator('body')).toContainText('Add a provider');
  });

  test('the API-keys section is present', async ({ page }) => {
    await page.goto('/dash/profile/');
    await expect(page.locator('body')).toContainText('API keys');
    await expect(page.locator('body')).toContainText('Manage API keys');
  });

  test('provider link buttons appear for all unlinked providers', async ({
    page,
  }) => {
    await page.goto('/dash/profile/');
    for (const label of ['Google', 'GitHub', 'Discord', 'Telegram']) {
      await expect(
        page.locator(`a.oauth-btn`, { hasText: `Link ${label}` }),
      ).toBeVisible();
    }
  });

  test('unauthenticated → 200 with error banner, not 401', async ({
    browser,
  }) => {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    await page.goto('/dash/profile/');
    expect(page.url()).toContain('/dash/profile/');
    await expect(page.locator('body')).toContainText('no identity');
    await ctx.close();
  });
});
