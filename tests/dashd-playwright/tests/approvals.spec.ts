import { test, expect, asUser, asOperator, stubURL } from './_helpers';

// approvals.spec — /dash/approvals/, the HITL review queue (spec 5/19).
//
// This page gates every tool call a hold rule suspended, and until now it was
// the one dashd page with no browser coverage (BUGS L98) — which is how L97
// (chat /approve never re-issues the held call) reached production.
//
// The queue itself lives in routd, not in dashd's DB: the page reads it over
// /v1/pending_actions and forwards the verdict to routd, because the verdict
// must commit together with the resolution message that makes the agent
// re-issue. So the assertions here are about what dashd RENDERS and what it
// FORWARDS — the stub upstream stands in for routd.

const HELD = 'seedheld';
const RESOLVED = 'seedgone';

async function stubResolved(request: {
  get: (
    u: string,
  ) => Promise<{
    json: () => Promise<{
      resolved: Array<{ id: string; verdict: string; body: string }>;
    }>;
  }>;
}) {
  const r = await request.get(`${stubURL()}/_stub/resolved`);
  return (await r.json()).resolved;
}

test.describe('approvals queue', () => {
  test.beforeEach(async ({ context }) => {
    await asOperator(context);
    await context.request.delete(`${stubURL()}/_stub/resolved`);
  });

  test('renders the held call with its tool, group and arguments', async ({
    page,
  }) => {
    await page.goto('/dash/approvals/');
    await expect(page.locator('h1')).toHaveText('approvals');
    const body = page.locator('body');
    await expect(body).toContainText('Waiting for a verdict');
    await expect(body).toContainText('like');
    await expect(body).toContainText('inbox');
    // The arguments are the material under review — they must be on the page,
    // not behind a hover.
    await expect(body).toContainText('targetId');
  });

  test('a resolved call appears under the verdicts, not the queue', async ({
    page,
  }) => {
    await page.goto('/dash/approvals/');
    await expect(page.locator('body')).toContainText('delete');
    await expect(page.locator('body')).toContainText('testadmin');
  });

  test('each held call offers both approve and reject', async ({ page }) => {
    await page.goto('/dash/approvals/');
    const form = page.locator(`form[action="/dash/approvals/${HELD}/resolve"]`);
    await expect(form).toHaveCount(1);
    await expect(form.locator('button[value="approve"]')).toBeVisible();
    await expect(form.locator('button[value="reject"]')).toBeVisible();
  });

  test('approve forwards the verdict and the reviewer to routd', async ({
    page,
    context,
  }) => {
    await page.goto('/dash/approvals/');
    await page
      .locator(
        `form[action="/dash/approvals/${HELD}/resolve"] input[name="note"]`,
      )
      .fill('ship it');
    await page
      .locator(
        `form[action="/dash/approvals/${HELD}/resolve"] button[value="approve"]`,
      )
      .click();
    await expect(page.locator('body')).toContainText('approved');

    const seen = await stubResolved(context.request);
    expect(seen).toHaveLength(1);
    expect(seen[0].id).toBe(HELD);
    expect(seen[0].verdict).toBe('approve');
    // reviewed_by must name the human, never dashd's service principal.
    expect(seen[0].body).toContain('testadmin');
    expect(seen[0].body).toContain('ship it');
  });

  test('reject forwards reject, not approve', async ({ page, context }) => {
    await page.goto('/dash/approvals/');
    await page
      .locator(
        `form[action="/dash/approvals/${HELD}/resolve"] button[value="reject"]`,
      )
      .click();
    const seen = await stubResolved(context.request);
    expect(seen).toHaveLength(1);
    expect(seen[0].verdict).toBe('reject');
  });

  test('?id= deep link focuses the named call', async ({ page }) => {
    await page.goto(`/dash/approvals/?id=${HELD}`);
    await expect(
      page.locator(`form[action="/dash/approvals/${HELD}/resolve"]`),
    ).toHaveCount(1);
  });

  test('?id= for an already-decided call says so instead of going quiet', async ({
    page,
  }) => {
    await page.goto(`/dash/approvals/?id=${RESOLVED}`);
    await expect(page.locator('body')).toContainText('already has a verdict');
  });

  test('?id= for an unknown call reports it', async ({ page }) => {
    await page.goto('/dash/approvals/?id=nosuchid');
    await expect(page.locator('body')).toContainText('no held call with id');
  });

  test('non-operator is forbidden on the page and on the verdict', async ({
    context,
  }) => {
    await asUser(context, 'github:regular');
    expect((await context.request.get('/dash/approvals/')).status()).toBe(403);
    const post = await context.request.post(`/dash/approvals/${HELD}/resolve`, {
      form: { verdict: 'approve' },
      maxRedirects: 0,
    });
    expect(post.status()).toBe(403);
  });

  test('unauthenticated is rejected', async ({ context }) => {
    await context.setExtraHTTPHeaders({});
    expect((await context.request.get('/dash/approvals/')).status()).toBe(401);
  });
});
