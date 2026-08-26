import { test, expect, asUser, asOperator } from './_helpers';

// operator-reads.spec — the four operator-only read surfaces that had no
// coverage: audit, usage, packages, proactive.
//
// They share one shape (operator gate → render from routd.db) so they share a
// file; each still asserts its own content, not merely a 200.

test.describe('operator read surfaces', () => {
  test.beforeEach(async ({ context }) => {
    await asOperator(context);
  });

  test('/dash/audit/ renders the log with its columns', async ({ page }) => {
    const resp = await page.goto('/dash/audit/');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('h1')).toHaveText('audit');
    const body = page.locator('body');
    for (const col of ['Time', 'Category', 'Action', 'Actor', 'Outcome']) {
      await expect(body, `audit column ${col}`).toContainText(col);
    }
  });

  test('/dash/usage/ renders the per-group and volume sections', async ({
    page,
  }) => {
    const resp = await page.goto('/dash/usage/');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('h1')).toHaveText('usage');
    await expect(page.locator('body')).toContainText('Per-group');
    await expect(page.locator('body')).toContainText('7-day volume');
  });

  test('/dash/packages/ renders the installed-package table', async ({
    page,
  }) => {
    const resp = await page.goto('/dash/packages/');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('h1')).toHaveText('packages');
    const body = page.locator('body');
    // The folder column is load-bearing: instance-wide and blended-into-a-group
    // are different facts and must not read as the same one.
    await expect(body).toContainText('folder');
    await expect(body).toContainText('revision');
  });

  test('/dash/proactive/ renders the settings and first-speaker sections', async ({
    page,
  }) => {
    const resp = await page.goto('/dash/proactive/');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('h1')).toHaveText('proactive');
    const body = page.locator('body');
    await expect(body).toContainText('Group settings');
    await expect(body).toContainText('Chats that spoke first');
  });

  for (const path of [
    '/dash/audit/',
    '/dash/usage/',
    '/dash/packages/',
    '/dash/proactive/',
  ]) {
    test(`${path} is operator-only`, async ({ context }) => {
      await asUser(context, 'github:regular');
      expect((await context.request.get(path)).status()).toBe(403);
    });

    test(`${path} rejects the unauthenticated`, async ({ context }) => {
      await context.setExtraHTTPHeaders({});
      expect((await context.request.get(path)).status()).toBe(401);
    });
  }
});
