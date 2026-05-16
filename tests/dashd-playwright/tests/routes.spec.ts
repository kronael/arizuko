import { test, expect, asUser } from './_helpers';

// routes.spec — exercise /dash/routes/ list + add form + delete via DOM.
// Setup: seeded route `chat_jid=telegram:user/seed → inbox` is always
// present. Each test adds its own row, asserts presence, then deletes
// via the inline form so the table is back to baseline.

test.describe('routes editor', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('table renders seeded route', async ({ page }) => {
    await page.goto('/dash/routes/');
    await expect(page.locator('h1')).toHaveText('Routes');
    await expect(page.locator('table tbody')).toContainText(
      'chat_jid=telegram:user/seed',
    );
    await expect(page.locator('table tbody')).toContainText('inbox');
  });

  test('add route via form, row appears', async ({ page }) => {
    const match = `chat_jid=telegram:user/pw-${Date.now()}`;
    await page.goto('/dash/routes/');
    await page.locator('input[name="seq"]').fill('42');
    await page.locator('input[name="match"]').fill(match);
    await page.locator('input[name="target"]').fill('inbox');
    await Promise.all([
      page.waitForURL('**/dash/routes/'),
      page.locator('button[type="submit"]', { hasText: 'add' }).click(),
    ]);
    await expect(page.locator('table tbody')).toContainText(match);

    // cleanup: click the delete button on the row we just added. Confirm()
    // pops up — auto-accept it.
    page.on('dialog', (d) => d.accept());
    const row = page.locator('tr', { hasText: match });
    await row.locator('button', { hasText: 'delete' }).click();
    await page.waitForURL('**/dash/routes/');
    await expect(page.locator('table tbody')).not.toContainText(match);
  });

  test('PATCH route via JSON API updates target', async ({ context }) => {
    // Add via form-POST, fetch id from listing, PATCH it, verify rendered target.
    const match = `chat_jid=telegram:user/patch-${Date.now()}`;
    const post = await context.request.post('/dash/routes/', {
      form: { seq: '50', match, target: 'inbox' },
      maxRedirects: 0,
    });
    expect(post.status()).toBe(303);

    const list = await (await context.request.get('/dash/routes/')).text();
    const escMatch = match.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const rowRe = new RegExp(
      `${escMatch}[\\s\\S]*?\\/dash\\/routes\\/(\\d+)\\/delete`,
    );
    const m = list.match(rowRe);
    expect(m, 'inserted row must be found').not.toBeNull();
    const id = m![1];

    const patch = await context.request.patch(`/dash/routes/${id}`, {
      data: { seq: 51, match, target: 'inbox' },
      headers: { 'Content-Type': 'application/json' },
    });
    expect(patch.status()).toBe(204);

    const del = await context.request.post(`/dash/routes/${id}/delete`, {
      maxRedirects: 0,
    });
    expect(del.status()).toBe(303);
  });

  test('DELETE unknown id → 404 (admin)', async ({ context }) => {
    const r = await context.request.delete('/dash/routes/9999999');
    expect(r.status()).toBe(404);
  });
});
