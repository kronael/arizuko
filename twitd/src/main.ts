import { RouterClient, type InboundMsg } from './client.js';
import { log } from './log.js';
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
const channelSecret = env('CHANNEL_SECRET', '');
const listenAddr = env('LISTEN_ADDR', ':8080');
const listenURL = env('LISTEN_URL', 'http://twitd:8080');
const pollIntervalSec = parseInterval(envOpt('TWITTER_POLL_INTERVAL') ?? '90');

const rc = new RouterClient(routerURL, channelSecret);
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

// pollOnce drains mentions / DMs since last cursor and posts to the router.
// All sources are best-effort — one source failing must not stall the others.
async function pollOnce(state: CursorState): Promise<CursorState> {
  if (!scraper) return state;
  const next: CursorState = { ...state };

  // Mentions → reply / message inbound.
  try {
    const m = (
      scraper as unknown as {
        getMentions?: (n: number) => AsyncIterable<unknown>;
      }
    ).getMentions;
    if (typeof m === 'function') {
      const iter = m.call(scraper, 20);
      for await (const t of iter) {
        const tw = t as Record<string, unknown>;
        const id = String(tw['id'] ?? '');
        if (!id) continue;
        if (state.mentions && id <= state.mentions) break;
        if (!next.mentions || id > next.mentions) next.mentions = id;
        const sender = String(tw['username'] ?? 'unknown');
        const inReplyTo = tw['inReplyToStatusId']
          ? String(tw['inReplyToStatusId'])
          : undefined;
        const msg: InboundMsg = {
          id,
          chat_jid: 'x:home',
          sender: `x:user/${sender}`,
          sender_name: String(tw['name'] ?? sender),
          content: String(tw['text'] ?? ''),
          timestamp: Number(tw['timestamp']) || Math.floor(Date.now() / 1000),
          verb: inReplyTo ? 'reply' : 'message',
          ...(inReplyTo ? { reply_to: inReplyTo } : {}),
        };
        try {
          await rc.sendMessage(msg);
          lastInboundAt = Math.floor(Date.now() / 1000);
        } catch (e) {
          log('error', 'deliver mention failed', { id, err: String(e) });
        }
      }
    }
  } catch (e) {
    log('warn', 'mentions poll failed', { err: String(e) });
  }

  return next;
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
    channelSecret,
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
