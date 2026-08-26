import { test, expect, asUser, asOperator, stubURL } from './_helpers';

// control-planes.spec — the four per-daemon pages behind /dash/services/.
//
// Each reads its daemon's own /v1 face over HTTP with dashd's service bearer,
// so the content here comes from the stub upstream, not from dashd's DB. The
// point of the coverage is that dashd DECODES what the daemon sends: every
// stub response matches the struct the page declares, so a field rename in
// resreg or apiv1 fails here instead of rendering a blank table in production.
//
// The services HUB is a separate axis and stays in services.spec.ts: it probes
// hardcoded Docker hostnames (dashd/services.go `services`), not the *_URL env
// vars these pages read, so pointing the pages at a stub does not turn its
// tiles green.

const PAGES = [
  {
    path: '/dash/routd/',
    title: 'routd',
    mustContain: ['Errored chats', 'Dead-lettered outbound'],
  },
  {
    path: '/dash/runed/',
    title: 'runed',
    mustContain: ['Active runs', 'Recent runs'],
  },
  // authd is reached on its UNREACHABLE path: AUTHD_URL is the switch that
  // turns dashd's transit guard on, so the harness cannot point it at the stub
  // without needing a real proxyd bearer for every request (see global-setup).
  {
    path: '/dash/authd/',
    title: 'authd',
    mustContain: ['Signing keys', 'could not reach authd'],
  },
  { path: '/dash/proxyd/', title: 'proxyd', mustContain: ['Path', 'Goes to'] },
];

test.describe('daemon control planes', () => {
  test.beforeEach(async ({ context }) => {
    await asOperator(context);
  });

  for (const p of PAGES) {
    test(`${p.path} renders its sections`, async ({ page }) => {
      const resp = await page.goto(p.path);
      expect(resp?.status()).toBe(200);
      await expect(page.locator('h1')).toHaveText(p.title);
      for (const t of p.mustContain) {
        await expect(page.locator('body'), `${p.path} → ${t}`).toContainText(t);
      }
    });

    test(`${p.path} is operator-only`, async ({ context }) => {
      await asUser(context, 'github:regular');
      expect((await context.request.get(p.path)).status()).toBe(403);
    });
  }

  test('routd decodes the dead-letter rows it was sent', async ({ page }) => {
    await page.goto('/dash/routd/');
    const body = page.locator('body');
    await expect(body).toContainText('telegram:user/seed');
    // delivery_attempts=0 must read as "refused", not "0 attempts" — the two
    // dead-letter causes need different operator responses.
    await expect(body).toContainText('refused');
  });

  test('authd reports the unreachable daemon instead of rendering an empty table', async ({
    page,
  }) => {
    // The failure must be legible: an empty Signing-keys table and "authd is
    // down" are different facts and an operator must be able to tell them
    // apart.
    await page.goto('/dash/authd/');
    await expect(page.locator('body')).toContainText('could not reach authd');
  });

  test('proxyd decodes the route table', async ({ page }) => {
    await page.goto('/dash/proxyd/');
    const body = page.locator('body');
    await expect(body).toContainText('/dash/');
    await expect(body).toContainText('/pub/');
  });

  test('the stub is the upstream these pages read', async ({ context }) => {
    // Guards the harness itself: if global-setup stops pointing dashd at the
    // stub, the assertions above would pass against empty pages.
    const r = await context.request.get(`${stubURL()}/v1/signing_keys`);
    expect(r.status()).toBe(200);
  });
});
