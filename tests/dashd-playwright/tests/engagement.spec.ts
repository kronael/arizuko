import { test, expect, asUser, asOperator } from './_helpers';

// engagement.spec — /dash/engagement/, the "agent is currently listening here"
// window and the force-disengage that ends it.

test.describe('engagement', () => {
  test.beforeEach(async ({ context }) => {
    await asOperator(context);
  });

  test('lists the engaged conversation routd reported', async ({ page }) => {
    const resp = await page.goto('/dash/engagement/');
    expect(resp?.status()).toBe(200);
    await expect(page.locator('h1')).toHaveText('engagement');
    const body = page.locator('body');
    await expect(body).toContainText('Engaged conversations');
    await expect(body).toContainText('telegram:user/seed');
    await expect(body).toContainText('inbox');
  });

  test('force-disengage is behind a confirm that names the chat', async ({
    page,
  }) => {
    await page.goto('/dash/engagement/');
    const form = page.locator('form[action="/dash/engagement/disengage"]');
    await expect(form).toHaveCount(1);
    const onsubmit = await form.getAttribute('onsubmit');
    expect(onsubmit ?? '').toContain('telegram:user/seed');
  });

  test('disengage posts and redirects', async ({ context }) => {
    const r = await context.request.post('/dash/engagement/disengage', {
      form: { jid: 'telegram:user/seed', topic: '' },
      maxRedirects: 0,
    });
    expect(r.status()).toBe(303);
  });

  test('operator-only', async ({ context }) => {
    await asUser(context, 'github:regular');
    expect((await context.request.get('/dash/engagement/')).status()).toBe(403);
  });
});
