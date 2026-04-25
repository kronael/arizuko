import { describe, expect, it, beforeEach, afterAll } from 'bun:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {
  authenticate,
  loadCookies,
  loadCursors,
  saveCookies,
  saveCursors,
  type Scraper,
} from './twitter';
import { parseJid, readResponseId, stripTweetPrefix } from './verbs';

const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'twitd-test-'));

afterAll(() => {
  try {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  } catch {}
});

function freshDir(name: string): string {
  const dir = path.join(tmpRoot, name);
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

function makeMockScraper(): {
  s: Scraper;
  state: {
    setCookiesCalls: unknown[];
    isLoggedInResult: boolean;
    loginCalls: unknown[][];
    cookiesToReturn: unknown;
  };
} {
  const state = {
    setCookiesCalls: [] as unknown[],
    isLoggedInResult: true,
    loginCalls: [] as unknown[][],
    cookiesToReturn: [{ name: 'auth_token', value: 'fresh' }],
  };
  const s = {
    setCookies: async (c: unknown) => {
      state.setCookiesCalls.push(c);
    },
    getCookies: async () => state.cookiesToReturn,
    isLoggedIn: async () => state.isLoggedInResult,
    login: async (...args: unknown[]) => {
      state.loginCalls.push(args);
    },
    getProfile: async () => ({ username: 'me' }),
  } as unknown as Scraper;
  return { s, state };
}

describe('parseJid', () => {
  it('parses twitter:home', () => {
    expect(parseJid('twitter:home')).toEqual({ kind: 'home', id: '' });
  });
  it('parses twitter:tweet/<id>', () => {
    expect(parseJid('twitter:tweet/12345')).toEqual({
      kind: 'tweet',
      id: '12345',
    });
  });
  it('parses twitter:dm/<conv>', () => {
    expect(parseJid('twitter:dm/conv-1')).toEqual({ kind: 'dm', id: 'conv-1' });
  });
  it('parses twitter:user/<handle>', () => {
    expect(parseJid('twitter:user/alice')).toEqual({
      kind: 'user',
      id: 'alice',
    });
  });
  it('returns unknown for unrecognised kinds', () => {
    expect(parseJid('twitter:weird/xyz').kind).toBe('unknown');
  });
});

describe('stripTweetPrefix', () => {
  it('extracts id from twitter:tweet/<id>', () => {
    expect(stripTweetPrefix('twitter:tweet/777')).toBe('777');
  });
  it('passes bare numeric ids through', () => {
    expect(stripTweetPrefix('777')).toBe('777');
  });
});

describe('readResponseId', () => {
  it('extracts rest_id from create_tweet response', async () => {
    const fakeResp = {
      json: async () => ({
        data: {
          create_tweet: {
            tweet_results: { result: { rest_id: 'abc123' } },
          },
        },
      }),
    };
    expect(await readResponseId(fakeResp)).toBe('abc123');
  });
  it('returns empty string for non-Response inputs', async () => {
    expect(await readResponseId(null)).toBe('');
    expect(await readResponseId({})).toBe('');
  });
  it('returns empty string when json throws', async () => {
    const r = {
      json: async () => {
        throw new Error('boom');
      },
    };
    expect(await readResponseId(r)).toBe('');
  });
});

describe('cookie persistence', () => {
  let dir: string;
  beforeEach(() => {
    dir = freshDir(`cookies-${Date.now()}-${Math.random()}`);
  });

  it('returns null when no files exist', () => {
    expect(loadCookies(dir)).toBeNull();
  });

  it('round-trips cookies through save+load', () => {
    const cookies = [{ name: 'auth_token', value: 'xyz' }];
    saveCookies(dir, cookies);
    expect(loadCookies(dir)).toEqual(cookies);
  });

  it('writes a .bak on subsequent save', () => {
    saveCookies(dir, [{ a: 1 }]);
    saveCookies(dir, [{ a: 2 }]);
    expect(fs.existsSync(path.join(dir, 'cookies.json.bak'))).toBe(true);
    const live = JSON.parse(
      fs.readFileSync(path.join(dir, 'cookies.json'), 'utf8'),
    );
    expect(live).toEqual([{ a: 2 }]);
  });

  it('falls back to .bak if live cookies are empty', () => {
    saveCookies(dir, [{ from: 'first' }]);
    saveCookies(dir, [{ from: 'second' }]);
    // .bak now holds the prior live, which was [{from:'first'}].
    fs.writeFileSync(path.join(dir, 'cookies.json'), '');
    expect(loadCookies(dir)).toEqual([{ from: 'first' }]);
  });
});

describe('cursor persistence', () => {
  it('round-trips cursors', () => {
    const dir = freshDir('cursors');
    saveCursors(dir, { mentions: 'M1', dms: 'D1' });
    expect(loadCursors(dir)).toEqual({ mentions: 'M1', dms: 'D1' });
  });
  it('returns empty state when missing', () => {
    expect(loadCursors(freshDir('cursors-empty'))).toEqual({});
  });
});

describe('authenticate', () => {
  it('uses cookies when present and session is valid', async () => {
    const dir = freshDir('auth-cookies-ok');
    saveCookies(dir, [{ name: 'auth_token', value: 'live' }]);
    const { s, state } = makeMockScraper();
    state.isLoggedInResult = true;
    await authenticate(s, { authDir: dir });
    expect(state.setCookiesCalls.length).toBe(1);
    expect(state.loginCalls.length).toBe(0);
  });

  it('falls back to login when cookies present but session invalid', async () => {
    const dir = freshDir('auth-cookies-stale');
    saveCookies(dir, [{ name: 'auth_token', value: 'old' }]);
    const { s, state } = makeMockScraper();
    state.isLoggedInResult = false;
    await authenticate(s, {
      authDir: dir,
      username: 'u',
      password: 'p',
    });
    expect(state.loginCalls.length).toBe(1);
    expect(state.loginCalls[0]![0]).toBe('u');
    // Fresh cookies should have been persisted.
    expect(loadCookies(dir)).not.toBeNull();
  });

  it('logs in directly when no cookies file exists', async () => {
    const dir = freshDir('auth-no-cookies');
    const { s, state } = makeMockScraper();
    state.isLoggedInResult = true;
    await authenticate(s, {
      authDir: dir,
      username: 'u',
      password: 'p',
      email: 'e@x',
      twoFactorSecret: 'TOTP',
    });
    expect(state.loginCalls.length).toBe(1);
    expect(state.loginCalls[0]![3]).toBe('TOTP');
  });

  it('throws when no auth path is available', async () => {
    const dir = freshDir('auth-nothing');
    const { s } = makeMockScraper();
    await expect(authenticate(s, { authDir: dir })).rejects.toThrow(/no auth/);
  });
});
