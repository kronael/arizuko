import { test as base, expect, BrowserContext } from '@playwright/test';

// Identity is injected via the X-User-Sub header that proxyd normally
// signs upstream. In dashd's view (and per dashd/README.md), any header
// reaching it is trusted; integration tests exercise that contract.
export async function asUser(context: BrowserContext, sub: string) {
  await context.setExtraHTTPHeaders({ 'X-User-Sub': sub });
}

export { base as test, expect };
