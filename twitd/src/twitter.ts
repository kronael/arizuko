import fs from 'node:fs';
import path from 'node:path';
import { log } from './log.js';

// Minimal subset of agent-twitter-client's Scraper surface that twitd uses.
// Declared as an interface so tests can stub without dragging in the library.
export interface Scraper {
  setCookies(cookies: unknown): Promise<void>;
  getCookies(): Promise<unknown>;
  isLoggedIn(): Promise<boolean>;
  login(
    username: string,
    password: string,
    email?: string,
    twoFactorSecret?: string,
  ): Promise<void>;
  getProfile(username: string): Promise<{ userId?: string; username?: string }>;
  sendTweet(
    text: string,
    replyToTweetId?: string,
    mediaData?: { data: Buffer; mediaType: string }[],
  ): Promise<Response>;
  sendQuoteTweet(
    text: string,
    quotedTweetId: string,
    mediaData?: { data: Buffer; mediaType: string }[],
  ): Promise<Response>;
  retweet(tweetId: string): Promise<Response>;
  likeTweet(tweetId: string): Promise<Response>;
  deleteTweet(tweetId: string): Promise<Response>;
  sendDirectMessage(conversationId: string, text: string): Promise<unknown>;
  getMentions?(count: number): AsyncIterable<unknown>;
  getDirectMessageConversations?(userId: string): Promise<unknown>;
}

export interface TwitterConfig {
  authDir: string;
  username?: string;
  password?: string;
  email?: string;
  twoFactorSecret?: string;
}

// loadCookies returns cookie array if cookies.json exists and parses, else null.
// Falls back to .bak when the live file is missing or empty (mirrors whapd).
export function loadCookies(authDir: string): unknown[] | null {
  const live = path.join(authDir, 'cookies.json');
  const backup = path.join(authDir, 'cookies.json.bak');
  for (const p of [live, backup]) {
    try {
      const stat = fs.statSync(p);
      if (stat.size === 0) continue;
      const raw = fs.readFileSync(p, 'utf8');
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) return parsed;
    } catch {
      // fall through
    }
  }
  return null;
}

// saveCookies writes atomically: temp file then rename, with a .bak rotation.
export function saveCookies(authDir: string, cookies: unknown): void {
  fs.mkdirSync(authDir, { recursive: true });
  const live = path.join(authDir, 'cookies.json');
  const backup = path.join(authDir, 'cookies.json.bak');
  const tmp = path.join(authDir, `cookies.json.${process.pid}.tmp`);
  fs.writeFileSync(tmp, JSON.stringify(cookies), { mode: 0o600 });
  try {
    if (fs.existsSync(live) && fs.statSync(live).size > 0) {
      fs.copyFileSync(live, backup);
    }
  } catch {}
  fs.renameSync(tmp, live);
}

export interface CursorState {
  mentions?: string;
  dms?: string;
  likes?: string;
  retweets?: string;
  followers?: number;
}

export function loadCursors(authDir: string): CursorState {
  try {
    const raw = fs.readFileSync(path.join(authDir, 'cursors.json'), 'utf8');
    return JSON.parse(raw) as CursorState;
  } catch {
    return {};
  }
}

export function saveCursors(authDir: string, c: CursorState): void {
  try {
    fs.mkdirSync(authDir, { recursive: true });
    fs.writeFileSync(path.join(authDir, 'cursors.json'), JSON.stringify(c), {
      mode: 0o600,
    });
  } catch (e) {
    log('warn', 'cursor save failed', { err: String(e) });
  }
}

// authenticate runs the 3-path login flow:
//   1. cookies file → setCookies + probe via isLoggedIn/getProfile
//   2. password creds → login() + persist cookies
//   3. neither → throw
// Returns the now-logged-in scraper.
export async function authenticate(
  scraper: Scraper,
  cfg: TwitterConfig,
): Promise<void> {
  fs.mkdirSync(cfg.authDir, { recursive: true });

  const cookies = loadCookies(cfg.authDir);
  if (cookies) {
    try {
      await scraper.setCookies(cookies);
      const ok = await scraper.isLoggedIn();
      if (ok) {
        log('info', 'authenticated via cookies');
        return;
      }
      log('warn', 'cookies present but session invalid');
    } catch (e) {
      log('warn', 'cookie load failed', { err: String(e) });
    }
  }

  if (cfg.username && cfg.password) {
    log('info', 'logging in with password', { user: cfg.username });
    await scraper.login(
      cfg.username,
      cfg.password,
      cfg.email,
      cfg.twoFactorSecret,
    );
    const fresh = await scraper.getCookies();
    saveCookies(cfg.authDir, fresh);
    log('info', 'login ok, cookies persisted');
    return;
  }

  throw new Error(
    'no auth available: provide cookies.json or TWITTER_USERNAME/PASSWORD',
  );
}
