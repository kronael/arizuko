import { describe, expect, it } from 'vitest';
import type * as http from 'node:http';
import { adapterStatus, isOperator, renderDash } from './dash.js';

// The contract's three states (6/31). `stale` is a live process whose platform
// link has gone quiet — the "healthchecks green but the agent doesn't reply"
// case — and collapsing it into ok or disconnected loses what separates them.
describe('adapterStatus', () => {
  const now = () => Math.floor(Date.now() / 1000);

  it('is ok when connected with fresh inbound', () => {
    expect(adapterStatus(true, now())).toBe('ok');
  });

  it('is stale when connected but quiet past the threshold', () => {
    expect(adapterStatus(true, now() - 6 * 60)).toBe('stale');
  });

  it('is disconnected when the platform link is down, however fresh', () => {
    expect(adapterStatus(false, now())).toBe('disconnected');
  });

  // A never-seen inbound is not stale: the adapter may have just started.
  it('is ok when connected and nothing has arrived yet', () => {
    expect(adapterStatus(true, 0)).toBe('ok');
  });
});

// A malformed scopes header must read as NO scopes. Reading it as all scopes is
// the shape that turns a parse bug into an operator takeover.
describe('isOperator', () => {
  const req = (hdr?: string) =>
    ({
      headers: hdr === undefined ? {} : { 'x-user-groups': hdr },
    }) as unknown as http.IncomingMessage;

  it('accepts the operator scope', () => {
    expect(isOperator(req('["**"]'))).toBe(true);
  });

  it('refuses a folder-scoped caller', () => {
    expect(isOperator(req('["atlas"]'))).toBe(false);
  });

  it('fails closed on absent or malformed headers', () => {
    expect(isOperator(req())).toBe(false);
    expect(isOperator(req(''))).toBe(false);
    expect(isOperator(req('["**"'))).toBe(false);
    expect(isOperator(req('{"scope":"**"}'))).toBe(false);
  });
});

describe('renderDash', () => {
  it('names the adapter, its prefixes and its status', () => {
    const html = renderDash(
      'twitd',
      ['twitter:'],
      true,
      Math.floor(Date.now() / 1000),
    );
    expect(html).toContain('twitd');
    expect(html).toContain('twitter:');
    expect(html).toContain('ok');
    expect(html).toContain('/dash/services/');
  });

  // Stale warns rather than errs: the process is up, so the next step is the
  // platform link, not a restart.
  it('renders stale in the warning colour, not the error one', () => {
    const html = renderDash('twitd', ['twitter:'], true, 1);
    expect(html).toContain('stale');
    expect(html).toContain('#d4af37');
    expect(html).not.toContain('#f85149');
  });

  it('escapes values that reach HTML', () => {
    const html = renderDash('<script>x</script>', ['twitter:'], true, 0);
    expect(html).not.toContain('<script>x</script>');
  });
});
