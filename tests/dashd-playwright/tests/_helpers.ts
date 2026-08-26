import { test as base, expect, BrowserContext } from '@playwright/test';

// Identity is injected via the X-User-* headers that proxyd normally signs
// upstream. In dashd's view (and per dashd/README.md), any header reaching it is
// trusted; integration tests exercise that contract.
//
// `groups` is the X-User-Groups half, and it is NOT cosmetic. Page access falls
// back to an ACL read when the header is absent (dashd/authz.go callerScope),
// but the top nav does not — `dashNavFor` reads the header only. A suite that
// sent the sub alone therefore rendered every operator link hidden and never
// tested one. Pass `['**']` to browse as the operator proxyd would sign.
export async function asUser(
  context: BrowserContext,
  sub: string,
  groups?: string[],
): Promise<void> {
  const headers: Record<string, string> = { 'X-User-Sub': sub };
  if (groups) headers['X-User-Groups'] = JSON.stringify(groups);
  await context.setExtraHTTPHeaders(headers);
}

// asOperator is the common case: signed in AND carrying `**`, the way every
// request that reaches dashd through proxyd looks.
export async function asOperator(
  context: BrowserContext,
  sub = 'testadmin',
): Promise<void> {
  await asUser(context, sub, ['**']);
}

// stubURL is the sibling-daemon stub global-setup started (see
// stub-upstream.ts). Workers inherit the env var the setup process exported.
export function stubURL(): string {
  const u = process.env.DASHD_STUB_URL;
  if (!u) throw new Error('DASHD_STUB_URL unset — global-setup did not run');
  return u;
}

export { base as test, expect };
