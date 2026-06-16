import { test, expect, asUser } from './_helpers';

// davd is the WebDAV daemon: per-group workspace browser/editor built on
// the `dufs` Rust binary. Auth and per-group scoping live upstream in
// proxyd — davd itself has no notion of identity. In this test environment
// only dashd is running (no proxyd, no davd), so the WebDAV surface at
// /dav/* cannot be exercised here. Instead, these tests verify that dashd
// renders correct /dav/* links in its group detail page, confirming the
// operator can reach the workspace browser when proxyd+davd are present.

test.describe('davd surface links', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('memory page links to /dav/{folder}/ workspace', async ({ page }) => {
    await page.goto('/dash/memory/?group=inbox');
    await expect(page.locator('a[href="/dav/inbox/"]')).toBeVisible();
  });

  test('memory page links to /dav/{folder}/ per-file entries', async ({
    page,
  }) => {
    await page.goto('/dash/memory/?group=inbox');
    const html = await page.content();
    expect(html).toContain('/dav/inbox/MEMORY.md');
    expect(html).toContain('/dav/inbox/CLAUDE.md');
  });
});
