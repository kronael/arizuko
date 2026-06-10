import {
  ServiceTokenSource,
  buildVerifier,
  type RoutdVerifier,
} from './auth.js';
import { RouterClient } from './client.js';
import { log } from './log.js';
import { pollMentions } from './poll.js';
import { startServer } from './server.js';
import {
  authenticate,
  loadCursors,
  saveCookies,
  saveCursors,
  type CursorState,
  type Scraper,
  type TwitterConfig,
} from './twitter.js';

function env(k: string, def?: string): string {
  const v = process.env[k] ?? def;
  if (v === undefined) {
    log('error', 'required env var missing', { key: k });
    process.exit(1);
  }
  return v;
}

function envOpt(k: string): string | undefined {
  const v = process.env[k];
  return v && v.length > 0 ? v : undefined;
}

const dataDir = process.env['DATA_DIR'] ?? '';
const authDir =
  process.env['TWITTER_AUTH_DIR'] ??
  (dataDir ? `${dataDir}/store/twitter-auth` : '/srv/data/store/twitter-auth');

const cfg: TwitterConfig = {
  authDir,
  username: envOpt('TWITTER_USERNAME'),
  password: envOpt('TWITTER_PASSWORD'),
  email: envOpt('TWITTER_EMAIL'),
  twoFactorSecret: envOpt('TWITTER_2FA_SECRET'),
};

const routerURL = env('ROUTER_URL');
const listenAddr = env('LISTEN_ADDR', ':8080');
const listenURL = env('LISTEN_URL', 'http://twitd:8080');
const pollIntervalSec = parseInterval(envOpt('TWITTER_POLL_INTERVAL') ?? '90');

// Split (spec 5/1) routd↔adapter auth — ES256 service tokens, no CHANNEL_SECRET
// (HMAC retire step 6). Outbound: exchange AUTHD_SERVICE_KEY for a
// service:<daemon> JWT (principal = AUTHD_SERVICE_NAME, the base daemon name)
// and present it on every routd call. Inbound: verifyRoutd pins service:routd
// against authd's JWKS. AUTHD_URL unset → local dev: bearer '' + verifier null
// (gate open), mirroring Go chanlib.
const authdURL = process.env['AUTHD_URL'];
const verifier: RoutdVerifier = buildVerifier();
let bearer: () => Promise<string> = async () => '';
if (authdURL) {
  const svcName = process.env['AUTHD_SERVICE_NAME'] || 'twitd';
  const svcKey = env('AUTHD_SERVICE_KEY');
  const src = new ServiceTokenSource(authdURL, svcName, svcKey);
  bearer = () => src.token();
  log('info', 'service-token auth enabled', {
    daemon: svcName,
    authd: authdURL,
  });
}
const rc = new RouterClient(routerURL, bearer);
let scraper: Scraper | null = null;
let connected = false;
let lastInboundAt = Math.floor(Date.now() / 1000);

function parseInterval(s: string): number {
  // accept "90", "90s", "5m"
  const m = /^(\d+)([sm]?)$/.exec(s.trim());
  if (!m) return 90;
  const n = parseInt(m[1]!, 10);
  if (m[2] === 'm') return n * 60;
  return n;
}

export function getScraper(): Scraper | null {
  return scraper;
}

export function isConnected(): boolean {
  return connected;
}

async function makeScraper(): Promise<Scraper> {
  // Lazy import — keeps `bun test` from loading the heavy module unless main runs.
  const mod = (await import('agent-twitter-client')) as unknown as {
    Scraper: new () => Scraper;
  };
  return new mod.Scraper();
}

async function persistCookiesIfPossible(s: Scraper): Promise<void> {
  try {
    const c = await s.getCookies();
    if (c) saveCookies(authDir, c);
  } catch (e) {
    log('warn', 'cookie persist failed', { err: String(e) });
  }
}

// pollOnce drains mentions since last cursor and posts to the router.
async function pollOnce(state: CursorState): Promise<CursorState> {
  if (!scraper) return state;
  const r = await pollMentions(scraper, rc, state);
  if (r.connected !== undefined) connected = r.connected;
  if (r.delivered > 0) lastInboundAt = Math.floor(Date.now() / 1000);
  return r.state;
}

async function pollLoop(): Promise<void> {
  let state = loadCursors(authDir);
  while (true) {
    try {
      state = await pollOnce(state);
      saveCursors(authDir, state);
    } catch (e) {
      log('error', 'poll loop iteration failed', { err: String(e) });
    }
    await sleep(pollIntervalSec * 1000);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

async function registerWithRetry(): Promise<void> {
  let attempt = 0;
  while (true) {
    try {
      await rc.register('twitter', listenURL);
      log('info', 'registered with router', { url: routerURL, attempt });
      return;
    } catch (e) {
      attempt++;
      const delay = Math.min(2000 * attempt, 30000);
      log('warn', 'router registration failed, retrying', {
        attempt,
        delay,
        err: String(e),
      });
      await sleep(delay);
    }
  }
}

async function pairOnce(): Promise<void> {
  if (!cfg.username || !cfg.password) {
    process.stderr.write(
      'pair mode requires TWITTER_USERNAME and TWITTER_PASSWORD env vars\n',
    );
    process.exit(1);
  }
  const s = await makeScraper();
  await s.login(cfg.username, cfg.password, cfg.email, cfg.twoFactorSecret);
  await persistCookiesIfPossible(s);
  process.stdout.write(`pair ok, cookies saved to ${authDir}/cookies.json\n`);
}

async function main() {
  if (process.argv.includes('--pair')) {
    await pairOnce();
    process.exit(0);
  }

  scraper = await makeScraper();
  try {
    await authenticate(scraper, cfg);
    connected = true;
    log('info', 'connected to twitter');
  } catch (e) {
    log('error', 'authentication failed', { err: String(e) });
    // Stay up — operator drops cookies, we recover on next restart.
  }

  // Don't gate on register success: docker restart loops would race.
  registerWithRetry();

  const srv = startServer(
    listenAddr,
    verifier,
    getScraper,
    isConnected,
    () => lastInboundAt,
  );

  if (connected) {
    pollLoop().catch((e) =>
      log('error', 'poll loop crashed', { err: String(e) }),
    );
  }

  async function shutdown() {
    log('info', 'shutting down');
    await rc.deregister();
    srv.close();
    process.exit(0);
  }

  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

main().catch((e) => {
  log('error', 'fatal', { err: String(e) });
  process.exit(1);
});
