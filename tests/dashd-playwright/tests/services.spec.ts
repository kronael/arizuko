import { test, expect, asUser } from './_helpers';

// services.spec — /dash/services/ cockpit hub.
//
// The services hub probes all 8 core daemons at /health. In tests none of the
// daemon hostnames resolve, so every tile shows status=unknown (DNS failure).
// The key invariant is the tile structure: built tiles link to their control
// plane REGARDLESS of probe status (D6 — an unreachable built daemon is
// exactly the one an operator wants to click through to diagnose); unbuilt
// tiles always render the name as plain text. BUILT/UNBUILT mirror
// dashd/services.go, the source of truth — keep in sync with it, not the
// other way round.

const BUILT = ['routd', 'runed'];
const UNBUILT = ['authd', 'proxyd', 'onbod', 'timed', 'webd', 'davd'];
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

  test('built tiles link to their control plane even when unreachable', async ({
    page,
  }) => {
    await page.goto('/dash/services/');
    for (const name of BUILT) {
      const tile = page.locator('.service-tile', { hasText: name });
      // This suite never resolves a daemon hostname — the tile is unknown,
      // yet the link must still render (D6: link is gated on Built alone).
      await expect(tile).toHaveAttribute('data-status', 'unknown');
      const link = tile.locator('a', { hasText: name });
      await expect(link).toBeVisible();
      const href = await link.getAttribute('href');
      expect(href).toBe(`/dash/${name}/`);
    }
  });

  test('unbuilt tiles render name as text, not a link, even though other tiles do link', async ({
    page,
  }) => {
    await page.goto('/dash/services/');
    // Sanity: at least one BUILT tile does link, so this isn't trivially true
    // because nothing on the page links at all.
    await expect(page.locator('.services-grid a')).not.toHaveCount(0);
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
