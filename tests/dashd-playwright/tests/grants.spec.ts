import { test, expect, asUser } from './_helpers';

// grants.spec — view ACL table, add a grant, revoke it. testadmin's own
// operator grant (admin / **) is never touched; we add+revoke a fresh row.

test.describe('grants', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('list renders page with grants table', async ({ page }) => {
    await page.goto('/dash/groups/inbox/grants');
    await expect(page.locator('h1')).toContainText('Grants');
    // grants page shows the ACL table and the add-grant form.
    // testadmin's ** grant is instance-wide, not shown in folder-scoped page.
    await expect(page.locator('form')).toBeVisible();
  });

  test('add grant via form, row appears, then revoke removes it', async ({
    page,
    context,
  }) => {
    const principal = `pwgrant-${Date.now()}@example`;
    const add = await context.request.post('/dash/groups/inbox/grants', {
      form: { principal, action: 'send', effect: 'allow', scope: 'inbox' },
      maxRedirects: 0,
    });
    expect(add.status()).toBe(303);

    await page.goto('/dash/groups/inbox/grants');
    await expect(page.locator('body')).toContainText(principal);

    const revoke = await context.request.post(
      '/dash/groups/inbox/grants/revoke',
      {
        form: { principal, action: 'send', effect: 'allow' },
        maxRedirects: 0,
      },
    );
    expect(revoke.status()).toBe(303);

    await page.reload();
    await expect(page.locator('body')).not.toContainText(principal);
  });

  test('non-admin is forbidden', async ({ context }) => {
    await asUser(context, 'nobody');
    const res = await context.request.get('/dash/groups/inbox/grants', {
      maxRedirects: 0,
    });
    expect(res.status()).toBe(403);
  });

  test('unauthenticated is rejected', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const res = await context.request.get('/dash/groups/inbox/grants', {
      maxRedirects: 0,
    });
    expect(res.status()).toBe(401);
  });
});
