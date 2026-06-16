import { test, expect, asUser } from './_helpers';

// services.spec — /dash/services/ cockpit hub.
//
// The services hub probes all 8 core daemons at /health. In tests none of the
// daemon hostnames resolve, so every tile shows status=unknown (DNS failure).
// The key invariant is the tile structure: built tiles (onbod, timed) have a
// link to their control plane; unbuilt tiles render the name as plain text.

const BUILT = ['onbod', 'timed'];
const UNBUILT = ['routd', 'runed', 'authd', 'proxyd', 'webd', 'davd'];
const ALL = [...BUILT, ...UNBUILT];

test.describe('services hub', () => {
  test.beforeEach(async ({ context }) => {
    await asUser(context, 'testadmin');
    await context.setExtraHTTPHeaders({
      'X-User-Sub': 'testadmin',
      'X-User-Groups': '**',
    });
  });

  test('renders services grid with all 8 daemons', async ({ page }) => {
    await page.goto('/dash/services/');
    await expect(page.locator('h1')).toHaveText('Services');
    await expect(page.locator('.services-grid')).toBeVisible();
    for (const name of ALL) {
      await expect(page.locator('.services-grid')).toContainText(name);
    }
  });

  test('built tiles link to their control plane', async ({ page }) => {
    await page.goto('/dash/services/');
    for (const name of BUILT) {
      const link = page.locator('.services-grid a', { hasText: name });
      await expect(link).toBeVisible();
      const href = await link.getAttribute('href');
      expect(href).toBe(`/dash/${name}/`);
    }
  });

  test('unbuilt tiles render name as text, not a link', async ({ page }) => {
    await page.goto('/dash/services/');
    for (const name of UNBUILT) {
      // The name should appear in the grid
      await expect(page.locator('.services-grid')).toContainText(name);
      // But NOT as a link to /dash/<name>/
      const deadLink = page.locator(`.services-grid a[href="/dash/${name}/"]`);
      await expect(deadLink).toHaveCount(0);
    }
  });

  test('all tiles show a status dot (ok/err/unknown)', async ({ page }) => {
    await page.goto('/dash/services/');
    // In tests, daemon hostnames don't resolve → DNS failure → unknown.
    const tiles = page.locator('.service-tile');
    await expect(tiles).toHaveCount(8);
    // Each tile carries a data-status attribute.
    for (let i = 0; i < 8; i++) {
      const status = await tiles.nth(i).getAttribute('data-status');
      expect(['ok', 'err', 'unknown']).toContain(status);
    }
  });

  test('non-operator is forbidden', async ({ context }) => {
    await context.setExtraHTTPHeaders({ 'X-User-Sub': 'github:regular' });
    const resp = await context.request.get('/dash/services/');
    expect(resp.status()).toBe(403);
  });
});
