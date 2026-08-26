import { test, expect, asUser, asOperator } from './_helpers';

// tools.spec — /dash/groups/{folder}/tools, the read-only MCP tool browser.
// It is the operator's answer to "what can this group's agent actually do",
// and the surface 5/44's facade tools land on.

test.describe('group tool browser', () => {
  test('lists the tools injected for a group', async ({ page, context }) => {
    await asOperator(context);
    const resp = await page.goto('/dash/groups/inbox/tools');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('h1')).toContainText('inbox');
    // reply is the one tool every group has — no reply, no agent.
    await expect(page.locator('body')).toContainText('reply');
  });

  test('breadcrumbs lead back to the group list', async ({ page, context }) => {
    await asOperator(context);
    await page.goto('/dash/groups/inbox/tools');
    await expect(page.locator('a[href="/dash/groups/"]')).not.toHaveCount(0);
  });

  test('a caller with no grant on the folder is refused', async ({
    context,
  }) => {
    await asUser(context, 'github:regular');
    const r = await context.request.get('/dash/groups/inbox/tools');
    expect([403, 404]).toContain(r.status());
  });
});
