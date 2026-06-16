import { test, expect, asUser } from './_helpers';

// tasks.spec — /dash/tasks/ list + create + partial refresh.

test.describe('tasks', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('list renders empty state', async ({ page }) => {
    await page.goto('/dash/tasks/');
    await expect(page.locator('h1')).toHaveText('Tasks');
    await expect(page.locator('table')).toBeVisible();
    await expect(page.locator('thead')).toContainText('Cron');
  });

  test('create task via form → appears in list', async ({ page }) => {
    await page.goto('/dash/tasks/');
    await page.fill('[name="owner"]', 'inbox');
    await page.fill('[name="chat_jid"]', 'telegram:user/seed');
    await page.fill('[name="prompt"]', 'playwright task probe');
    await page.fill('[name="cron"]', '0 9 * * *');
    await page.click('button[type="submit"]');
    await expect(page.locator('table tbody')).toContainText(
      'playwright task probe',
    );
  });

  test('partial refresh endpoint returns rows', async ({ context }) => {
    const r = await context.request.get('/dash/tasks/x/list');
    expect(r.status()).toBe(200);
  });

  test('unauthenticated → 401', async ({ request }) => {
    const r = await request.get('/dash/tasks/');
    expect(r.status()).toBe(401);
  });
});
