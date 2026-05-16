import { test, expect, asUser } from './_helpers';

// groups.spec — list, create, settings save, delete. Each test uses a
// distinct folder name so reruns + parallel ordering stay isolated.

test.describe('groups', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('list renders the seeded inbox', async ({ page }) => {
    await page.goto('/dash/groups/');
    await expect(page.locator('h1')).toHaveText('Groups');
    await expect(page.locator('details summary')).toContainText('inbox');
  });

  test('create new group via form, appears in list', async ({ page }) => {
    const folder = `pwgrp-${Date.now()}`;
    await page.goto('/dash/groups/new');
    await page.locator('input[name="folder"]').fill(folder);
    await page.locator('select[name="product"]').selectOption('assistant');
    await Promise.all([
      page.waitForURL('**/dash/groups/'),
      page.locator('button[type="submit"]', { hasText: 'create' }).click(),
    ]);
    await expect(page.locator('body')).toContainText(folder);
  });

  test('settings page renders form for seeded group', async ({ page }) => {
    await page.goto('/dash/groups/inbox/settings');
    await expect(page.locator('h1')).toContainText('Settings');
    await expect(
      page.locator('input[name="observe_window_messages"]'),
    ).toBeVisible();
    await expect(
      page.locator('input[name="observe_window_chars"]'),
    ).toBeVisible();
  });

  test('settings save persists across reload', async ({ page, context }) => {
    // create a dedicated group so we don't trample inbox
    const folder = `pwset-${Date.now()}`;
    const create = await context.request.post('/dash/groups/new', {
      form: { folder, product: 'assistant' },
      maxRedirects: 0,
    });
    expect(create.status()).toBe(303);

    await page.goto(`/dash/groups/${folder}/settings`);
    const winMsgs = page.locator('input[name="observe_window_messages"]');
    await winMsgs.fill('17');
    const owChars = page.locator('input[name="observe_window_chars"]');
    await owChars.fill('1234');
    await Promise.all([
      page.waitForURL(`**/dash/groups/${folder}/settings`),
      page.locator('button[type="submit"]', { hasText: 'save' }).click(),
    ]);
    await expect(winMsgs).toHaveValue('17');
    await expect(owChars).toHaveValue('1234');

    // reload + re-assert
    await page.reload();
    await expect(
      page.locator('input[name="observe_window_messages"]'),
    ).toHaveValue('17');
    await expect(
      page.locator('input[name="observe_window_chars"]'),
    ).toHaveValue('1234');
  });

  test('delete via POST form removes the group row', async ({
    page,
    context,
  }) => {
    const folder = `pwdel-${Date.now()}`;
    const create = await context.request.post('/dash/groups/new', {
      form: { folder, product: 'assistant' },
      maxRedirects: 0,
    });
    expect(create.status()).toBe(303);

    // confirm() auto-accept
    page.on('dialog', (d) => d.accept());
    await page.goto(`/dash/groups/${folder}/settings`);
    await Promise.all([
      page.waitForURL('**/dash/groups/'),
      page.locator('button', { hasText: 'delete group' }).click(),
    ]);

    const list = await page.content();
    expect(list).not.toContain(`<code>${folder}</code>`);
  });
});
