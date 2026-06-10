// ES256 service-token auth for the routd↔adapter trust boundary (HMAC retire
// step 6). Mirrors the Go chanlib path (auth.ServiceToken + chanlib.Auth):
//
//   outbound — exchange AUTHD_SERVICE_KEY at authd for a short-TTL
//     service:<daemon> JWT, cache it, refresh ~1 min before expiry, present it
//     as `Authorization: Bearer <jwt>` on every routd call (register +
//     /v1/messages).
//   inbound  — verify routd's incoming ES256 token offline against authd's
//     JWKS and pin sub==service:routd (routd is the only principal that drives
//     an adapter). AUTHD_URL unset → local dev → gate open (no shared secret
//     remains, mirroring Go's ks==nil branch).

import http from 'node:http';
import { createRemoteJWKSet, jwtVerify, type JWTVerifyGetKey } from 'jose';

// CallerRoutd is the principal routd exchanges its AUTHD_SERVICE_KEY for
// (compose sets AUTHD_SERVICE_NAME=routd → service:routd). The adapter gate
// admits only this caller.
const CallerRoutd = 'service:routd';

// refreshLeadMs re-exchanges this long before expiry, matching the Go source.
const refreshLeadMs = 60_000;

// ServiceTokenSource holds a daemon's current service token and refreshes it
// lazily. token() returns a live token, exchanging on first use and
// re-exchanging once the cached one is within refreshLeadMs of expiry.
export class ServiceTokenSource {
  private cached = '';
  private expiresMs = 0;
  private inflight: Promise<string> | null = null;
  private readonly authdURL: string;

  constructor(
    authdURL: string,
    private daemon: string,
    private key: string,
  ) {
    this.authdURL = authdURL.replace(/\/+$/, '');
  }

  async token(): Promise<string> {
    if (this.cached && Date.now() < this.expiresMs - refreshLeadMs) {
      return this.cached;
    }
    // Collapse concurrent refreshes into one exchange.
    if (!this.inflight) {
      this.inflight = this.exchange().finally(() => {
        this.inflight = null;
      });
    }
    return this.inflight;
  }

  // exchange performs POST /v1/service-token: the secret rides the Authorization
  // header (kept out of body-logging), the body carries only the daemon name.
  private async exchange(): Promise<string> {
    const r = await fetch(`${this.authdURL}/v1/service-token`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${this.key}`,
      },
      body: JSON.stringify({ daemon: this.daemon }),
    });
    if (!r.ok) throw new Error(`service-token exchange: status ${r.status}`);
    const out = (await r.json()) as { token?: string };
    if (!out.token) throw new Error('service-token exchange: empty token');
    // Schedule refresh off the JWT's own exp claim so it tracks the real expiry
    // (authd's response carries no expires_at field; read it from the token).
    this.cached = out.token;
    this.expiresMs = expFromJWT(out.token) ?? Date.now() + 60 * 60 * 1000;
    return out.token;
  }
}

// expFromJWT reads the exp claim (seconds) from a compact JWS WITHOUT verifying
// the signature — used only to schedule refresh of a token authd just minted for
// us (the issuer is trusted; we hold no verify key here). Never an auth decision.
function expFromJWT(token: string): number | null {
  const parts = token.split('.');
  if (parts.length !== 3) return null;
  try {
    const payload = JSON.parse(
      Buffer.from(parts[1]!, 'base64url').toString('utf8'),
    ) as { exp?: number };
    return payload.exp ? payload.exp * 1000 : null;
  } catch {
    return null;
  }
}

// RoutdVerifier resolves the ES256 verify key for an incoming routd token. In
// production it is a RemoteJWKSet over authd's /v1/keys; null when AUTHD_URL is
// unset (local dev → gate open). Tests inject a createLocalJWKSet (same shape).
export type RoutdVerifier = JWTVerifyGetKey | null;

// buildVerifier constructs the JWKS-backed verifier from the process env.
// AUTHD_URL unset → null (local dev, gate open). createRemoteJWKSet is lazy:
// it fetches + caches on first verify, no network here.
export function buildVerifier(): RoutdVerifier {
  const authdURL = process.env['AUTHD_URL'];
  if (!authdURL) return null;
  return createRemoteJWKSet(new URL(`${authdURL.replace(/\/+$/, '')}/v1/keys`));
}

// verifyRoutd gates a routd→adapter call: with a verifier wired it admits only a
// valid service:routd ES256 token; with no verifier (local dev) it always
// admits. Returns true when the request may proceed.
export async function verifyRoutd(
  req: http.IncomingMessage,
  jwks: RoutdVerifier,
): Promise<boolean> {
  if (!jwks) return true; // local dev: no JWKS → gate open
  const hdr = req.headers.authorization || '';
  if (!hdr.startsWith('Bearer ')) return false;
  const token = hdr.slice('Bearer '.length).trim();
  try {
    const { payload } = await jwtVerify(token, jwks, {
      algorithms: ['ES256'],
      issuer: 'authd',
    });
    return payload['typ'] === 'service' && payload.sub === CallerRoutd;
  } catch {
    return false;
  }
}
