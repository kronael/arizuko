import { beforeAll, describe, expect, it, test } from 'bun:test';
import { SignJWT, createLocalJWKSet, exportJWK, generateKeyPair } from 'jose';
import type * as http from 'node:http';
import { adapterStatus, handleDash, isOperator, renderDash } from './dash.js';
import type { RoutdVerifier } from './auth.js';

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
      'whapd',
      ['whatsapp:'],
      true,
      Math.floor(Date.now() / 1000),
    );
    expect(html).toContain('whapd');
    expect(html).toContain('whatsapp:');
    expect(html).toContain('ok');
    expect(html).toContain('/dash/services/');
  });

  // Stale warns rather than errs: the process is up, so the next step is the
  // platform link, not a restart.
  it('renders stale in the warning colour, not the error one', () => {
    const html = renderDash('whapd', ['whatsapp:'], true, 1);
    expect(html).toContain('stale');
    expect(html).toContain('#d4af37');
    expect(html).not.toContain('#f85149');
  });

  it('escapes values that reach HTML', () => {
    const html = renderDash('<script>x</script>', ['whatsapp:'], true, 0);
    expect(html).not.toContain('<script>x</script>');
  });
});

// whapd's delta (6/32): the pair-flow state. The shared page says
// "disconnected"; only this says whether an operator must go and enter a code
// on the phone.
describe('delta sections', () => {
  it('renders a section with its rows', () => {
    const html = renderDash('whapd', ['whatsapp:'], false, 0, [
      { title: 'Pairing', rows: [['state', 'pending']] },
    ]);
    expect(html).toContain('Pairing');
    expect(html).toContain('pending');
  });

  // An empty section still says so: omitting the panel looks identical to the
  // feature not existing.
  it('says so when a section is empty', () => {
    const html = renderDash('whapd', ['whatsapp:'], true, 0, [
      { title: 'Pairing', rows: [] },
    ]);
    expect(html).toContain('Pairing');
    expect(html).toContain('Nothing to show');
  });

  it('renders no section when the adapter has none', () => {
    expect(renderDash('whapd', ['whatsapp:'], true, 0)).not.toContain(
      '<h2>Pairing',
    );
  });

  it('escapes section values', () => {
    const html = renderDash('whapd', ['whatsapp:'], true, 0, [
      { title: 'x', rows: [['k', '<script>v</script>']] },
    ]);
    expect(html).not.toContain('<script>v</script>');
  });
});

// The operator page needs BOTH halves: proxyd's own service bearer AND the `**`
// scope it stamps. Reading the scope alone admitted anyone who could reach the
// port — send `X-User-Groups: ["**"]` to whapd:8080/dash/whapd/ and the operator page
// came back. BUGS `L400`; `L270` closed the same hole on the Go adapters, where
// chanlib/dash.go calls auth.RequireOperator.
describe('handleDash: the transit proof is half the gate', () => {
  let signKey: CryptoKey;
  let verifier: ReturnType<typeof createLocalJWKSet>;

  const sign = (sub: string, typ = 'service') =>
    new SignJWT({ typ })
      .setProtectedHeader({ alg: 'ES256' })
      .setSubject(sub)
      .setIssuer('authd')
      .setIssuedAt()
      .setExpirationTime('5m')
      .sign(signKey);

  beforeAll(async () => {
    const { publicKey, privateKey } = await generateKeyPair('ES256', {
      extractable: true,
    });
    signKey = privateKey;
    const jwk = await exportJWK(publicKey);
    verifier = createLocalJWKSet({ keys: [{ ...jwk, alg: 'ES256' }] });
  });

  function call(headers: Record<string, string>, v: RoutdVerifier) {
    const req = {
      method: 'GET',
      url: '/dash/whapd/',
      headers,
    } as unknown as http.IncomingMessage;
    let code = 0;
    let body = '';
    const res = {
      writeHead: (c: number) => {
        code = c;
      },
      end: (b?: string) => {
        body = b ?? '';
      },
    } as unknown as http.ServerResponse;
    return handleDash(req, res, {
      name: 'whapd',
      prefixes: ['x:'],
      enforce: true,
      verifier: v,
      isConnected: () => true,
      lastInboundAt: () => Math.floor(Date.now() / 1000),
    }).then(() => ({ code, body }));
  }

  test('the scope alone is refused — this is the hole', async () => {
    const { code } = await call({ 'x-user-groups': '["**"]' }, verifier);
    expect(code).toBe(403);
  });

  test('a routd token is not a proxyd token', async () => {
    const t = await sign('service:routd');
    const { code } = await call(
      { 'x-user-groups': '["**"]', authorization: `Bearer ${t}` },
      verifier,
    );
    expect(code).toBe(403);
  });

  test('the proxyd bearer without the operator scope is refused', async () => {
    const t = await sign('service:proxyd');
    const { code } = await call(
      { 'x-user-groups': '["atlas"]', authorization: `Bearer ${t}` },
      verifier,
    );
    expect(code).toBe(403);
  });

  test('both halves together serve the page', async () => {
    const t = await sign('service:proxyd');
    const { code, body } = await call(
      { 'x-user-groups': '["**"]', authorization: `Bearer ${t}` },
      verifier,
    );
    expect(code).toBe(200);
    expect(body).toContain('whapd');
  });

  // Local dev is `enforce: false` — AUTHD_URL unset, so nothing stamps a header
  // and nothing can verify one. Go's auth.RequireOperator opens on ks==nil for
  // the same reason. With enforce ON and no verifier the scope is still required,
  // which is why this asserts the flag and not the verifier.
  test('local dev keeps the page open', async () => {
    const req = {
      method: 'GET',
      url: '/dash/whapd/',
      headers: {},
    } as unknown as http.IncomingMessage;
    let code = 0;
    const res = {
      writeHead: (c: number) => {
        code = c;
      },
      end: () => {},
    } as unknown as http.ServerResponse;
    await handleDash(req, res, {
      name: 'whapd',
      prefixes: ['x:'],
      enforce: false,
      verifier: null,
      isConnected: () => true,
      lastInboundAt: () => Math.floor(Date.now() / 1000),
    });
    expect(code).toBe(200);
  });
});
