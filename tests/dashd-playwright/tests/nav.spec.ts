import { test, expect, asUser, asOperator } from './_helpers';

// nav.spec — the way in.
//
// Every other spec navigates by typing a URL, so the suite proved the PAGES
// worked while proving nothing about whether an operator can reach them. The
// top nav is built from `dashNavFor`, which reads X-User-Groups DIRECTLY —
// unlike page access, which falls back to an ACL read. A suite that sent only
// X-User-Sub therefore rendered every operator link hidden, and "there is no
// link to click" was invisible to it.

// Mirrors dashd/main.go navLinks — keep in sync with it, not the reverse.
const SCOPED = [
  { href: '/dash/', label: 'arizuko' },
  { href: '/dash/status/', label: 'status' },
  { href: '/dash/tasks/', label: 'tasks' },
  { href: '/dash/activity/', label: 'activity' },
  { href: '/dash/groups/', label: 'groups' },
  { href: '/dash/routes/', label: 'routes' },
  { href: '/dash/memory/', label: 'memory' },
];
const OPERATOR = [
  { href: '/dash/services/', label: 'services' },
  { href: '/dash/packages/', label: 'packages' },
  { href: '/dash/invites/', label: 'invites' },
  { href: '/dash/approvals/', label: 'approvals' },
  { href: '/dash/audit/', label: 'audit' },
  { href: '/dash/usage/', label: 'usage' },
  { href: '/dash/proactive/', label: 'proactive' },
  { href: '/dash/engagement/', label: 'engagement' },
];

test.describe('top nav', () => {
  test('operator sees every nav link, including approvals', async ({
    page,
    context,
  }) => {
    await asOperator(context);
    await page.goto('/dash/');
    for (const l of [...SCOPED, ...OPERATOR]) {
      await expect(
        page.locator(`nav a[href="${l.href}"]`),
        `nav link ${l.label}`,
      ).toHaveCount(1);
    }
  });

  test('every operator nav link actually opens its page', async ({
    page,
    context,
  }) => {
    await asOperator(context);
    for (const l of OPERATOR) {
      const resp = await page.goto(l.href);
      expect(resp?.status(), `${l.href} status`).toBe(200);
      // Heading case is the page's own (Services vs services) — the nav label
      // is the link text, not a promise about the h1.
      await expect(page.locator('h1'), `${l.href} heading`).toHaveText(
        new RegExp(`^${l.label}$`, 'i'),
      );
    }
  });

  test('every scoped nav link opens its page', async ({ page, context }) => {
    await asOperator(context);
    for (const l of SCOPED) {
      const resp = await page.goto(l.href);
      expect(resp?.status(), `${l.href} status`).toBe(200);
    }
  });

  test('a non-operator gets the scoped links and none of the operator ones', async ({
    page,
    context,
  }) => {
    await asUser(context, 'testuser', ['inbox']);
    await page.goto('/dash/');
    for (const l of SCOPED) {
      await expect(
        page.locator(`nav a[href="${l.href}"]`),
        `nav link ${l.label}`,
      ).toHaveCount(1);
    }
    for (const l of OPERATOR) {
      await expect(
        page.locator(`nav a[href="${l.href}"]`),
        `hidden link ${l.label}`,
      ).toHaveCount(0);
    }
  });

  test('the portal banner links to the held approvals', async ({
    page,
    context,
  }) => {
    await asOperator(context);
    await page.goto('/dash/');
    // The seed leaves one held pending_action, so the nudge must be there.
    const banner = page.locator('a[href="/dash/approvals/"]', {
      hasText: 'held for your approval',
    });
    await expect(banner).toHaveCount(1);
    await banner.click();
    await expect(page.locator('h1')).toHaveText('approvals');
  });

  test('the portal tiles all resolve', async ({ page, context }) => {
    await asOperator(context);
    await page.goto('/dash/');
    const tiles = page.locator('a.tile');
    const n = await tiles.count();
    expect(n).toBeGreaterThan(0);
    const hrefs: string[] = [];
    for (let i = 0; i < n; i++)
      hrefs.push((await tiles.nth(i).getAttribute('href')) ?? '');
    for (const href of hrefs) {
      if (!href.startsWith('/dash/')) continue; // /me/chats is webd's, not dashd's
      const r = await context.request.get(href, { maxRedirects: 0 });
      expect([200, 303], `${href} status`).toContain(r.status());
    }
  });
});
