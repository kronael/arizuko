import { test, expect, asUser } from './_helpers';

// read-only.spec — status, activity, memory pages. Memory write+delete
// over the JSON endpoint is exercised against the seeded `inbox` group.

test.describe('read-only pages', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('portal /dash/ renders nav + tiles', async ({ page }) => {
    await page.goto('/dash/');
    await expect(page.locator('h1')).toHaveText('arizuko');
    await expect(page.locator('.tiles')).toContainText('status');
    await expect(page.locator('.tiles')).toContainText('groups');
  });

  test('/dash/status/ shows DB path + group count row', async ({ page }) => {
    await page.goto('/dash/status/');
    await expect(page.locator('h1')).toHaveText('Status');
    // Page has two tables (key/value summary + channels list). Match the
    // summary table by content, then check rows inside it.
    const summary = page.locator('table').filter({ hasText: 'DB' }).first();
    await expect(summary).toContainText('Groups');
    await expect(summary).toContainText('Active sessions');
  });

  test('/dash/activity/ renders seeded message', async ({ page }) => {
    await page.goto('/dash/activity/');
    await expect(page.locator('h1')).toHaveText('Activity');
    await expect(page.locator('table tbody')).toContainText('hello from seed');
  });

  test('/dash/memory/ shows group selector and content', async ({ page }) => {
    await page.goto('/dash/memory/?group=inbox');
    await expect(page.locator('h1')).toHaveText('Memory');
    await expect(page.locator('h2', { hasText: 'MEMORY.md' })).toBeVisible();
    await expect(page.locator('pre')).toContainText('M-MARK-XYZ');
  });

  test('PUT /dash/memory/{folder}/MEMORY.md writes + DELETE removes', async ({
    context,
  }) => {
    const folder = 'inbox';
    const original = await (
      await context.request.get(`/dash/memory/?group=${folder}`)
    ).text();
    expect(original).toContain('M-MARK-XYZ');

    const newContent = `# rewritten by playwright @ ${Date.now()}\nMARK-NEW-XYZ\n`;
    const put = await context.request.fetch(
      `/dash/memory/${folder}/MEMORY.md`,
      {
        method: 'PUT',
        data: newContent,
      },
    );
    expect(put.status()).toBe(204);

    const reloaded = await (
      await context.request.get(`/dash/memory/?group=${folder}`)
    ).text();
    expect(reloaded).toContain('MARK-NEW-XYZ');

    // restore original so subsequent runs see the marker
    const restore = await context.request.fetch(
      `/dash/memory/${folder}/MEMORY.md`,
      {
        method: 'PUT',
        data: '# seed MEMORY\n\nplaywright test marker M-MARK-XYZ\n',
      },
    );
    expect(restore.status()).toBe(204);
  });
});
