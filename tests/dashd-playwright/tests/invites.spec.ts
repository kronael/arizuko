import { test, expect, asUser } from './_helpers';

// invites.spec — /dash/invites/ list + create + revoke.
// Seed: onbod.db exists, testadmin is operator (admin on **).

test.describe('invites', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
  });

  test('GET renders list (empty state OK)', async ({ page }) => {
    await page.goto('/dash/invites/');
    await expect(page.locator('h1')).toHaveText('Invites');
    const body = await page.locator('body').textContent();
    expect(body).toMatch(/No invites|Token/);
  });

  // Creating an invite renders the link ONCE and returns 200 — it does not
  // redirect back to the list, because the raw bearer is shown here and nowhere
  // else. Every read surface afterwards carries only the ref. The suite tested
  // a 303-then-find-the-token flow that the page stopped doing.
  test('create renders the one-time link and a ref, and returns 200', async ({
    context,
  }) => {
    const res = await context.request.post('/dash/invites/', {
      form: { target_glob: 'inbox', max_uses: '1' },
      maxRedirects: 0,
    });
    expect(res.status()).toBe(200);
    const html = await res.text();
    expect(html).toContain('Invite created');
    expect(html).toContain('not shown again');
    expect(html).toMatch(/\/invite\/[0-9a-f]{16,}/);

    // The list carries the ref, never the bearer.
    const raw = html.match(/\/invite\/([0-9a-f]{16,})/)![1];
    const list = await (await context.request.get('/dash/invites/')).text();
    expect(list).toContain('inbox');
    expect(list, 'the raw token must never reach a read surface').not.toContain(
      raw,
    );

    // cleanup
    const ref = list.match(/\/dash\/invites\/([^/"]+)\/revoke/)![1];
    await context.request.post(`/dash/invites/${ref}/revoke`, {
      maxRedirects: 0,
    });
  });

  test('revoke by ref → 303 → the row is gone', async ({ context }) => {
    const create = await context.request.post('/dash/invites/', {
      form: { target_glob: 'inbox', max_uses: '2' },
      maxRedirects: 0,
    });
    expect(create.status()).toBe(200);

    const html = await (await context.request.get('/dash/invites/')).text();
    const refMatch = html.match(/\/dash\/invites\/([^/"]+)\/revoke/);
    expect(refMatch, 'revoke form must expose an invite ref').not.toBeNull();
    const ref = refMatch![1];

    const revoke = await context.request.post(`/dash/invites/${ref}/revoke`, {
      maxRedirects: 0,
    });
    expect(revoke.status()).toBe(303);

    const after = await (await context.request.get('/dash/invites/')).text();
    expect(after).not.toContain(`/dash/invites/${ref}/revoke`);
  });

  test('non-operator → 403', async ({ context }) => {
    await asUser(context, 'stranger');
    const r = await context.request.get('/dash/invites/');
    expect(r.status()).toBe(403);
  });

  test('unauthenticated → 401', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    const r = await context.request.get('/dash/invites/');
    expect(r.status()).toBe(401);
  });
});
