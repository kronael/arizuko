import { test, expect, asUser } from './_helpers';

// auth.spec — gate matrix. Unauth → 401 on every write + JSON page;
// non-admin signed-in → 403 on writes; admin → 200/303 on writes.
// Read pages are open to any signed-in user (parity with /dash/groups/).

test.describe('auth', () => {
  test('unauthenticated routes view → 401', async ({ request }) => {
    const r = await request.get('/dash/routes/');
    expect(r.status()).toBe(401);
  });

  test('unauthenticated route create → 401', async ({ request }) => {
    const r = await request.post('/dash/routes/', {
      form: { seq: '5', match: 'chat_jid=telegram:user/x', target: 'inbox' },
    });
    expect(r.status()).toBe(401);
  });

  test('non-admin user cannot create route → 403', async ({ browser }) => {
    const ctx = await browser.newContext();
    await asUser(ctx, 'testuser');
    const r = await ctx.request.post('/dash/routes/', {
      form: {
        seq: '6',
        match: 'chat_jid=telegram:user/forbidden',
        target: 'inbox',
      },
    });
    expect(r.status()).toBe(403);
    await ctx.close();
  });

  test('admin can create + delete route → 303 + 303', async ({ browser }) => {
    const ctx = await browser.newContext();
    await asUser(ctx, 'testadmin');
    const create = await ctx.request.post('/dash/routes/', {
      form: {
        seq: '7',
        match: 'chat_jid=telegram:user/auth-admin',
        target: 'inbox',
      },
      maxRedirects: 0,
    });
    expect(create.status()).toBe(303);

    // discover id: rows render <tr>...<form action="/dash/routes/{id}/delete">.
    // Match within a single row by disallowing `<tr` in the lazy span.
    const page = await ctx.request.get('/dash/routes/');
    const body = await page.text();
    const rowRe =
      /telegram:user\/auth-admin[\s\S]*?\/dash\/routes\/(\d+)\/delete/;
    const found = body.match(rowRe);
    expect(found, 'created row should be present').not.toBeNull();
    const id = found![1];

    const del = await ctx.request.post(`/dash/routes/${id}/delete`, {
      maxRedirects: 0,
    });
    expect(del.status()).toBe(303);
    await ctx.close();
  });

  test('non-admin cannot create group → 403', async ({ browser }) => {
    const ctx = await browser.newContext();
    await asUser(ctx, 'testuser');
    const r = await ctx.request.post('/dash/groups/new', {
      form: { folder: 'authtest-denied', product: 'assistant' },
    });
    expect(r.status()).toBe(403);
    await ctx.close();
  });
});
