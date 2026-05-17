import fs from 'node:fs';
import type { WASocket } from '@whiskeysockets/baileys';
import { log } from './log.js';

// State of the pair-flow. `idle` = normal socket or no creds yet.
// `requesting` = waiting on Baileys to mint a code. `pending` = code
// in operator hands, waiting for phone-side entry to fire `open`.
export type PairState = 'idle' | 'requesting' | 'pending' | 'unauthenticated';

export interface PairStatus {
  state: PairState;
  since?: number; // unix sec the state was entered
  expires_at?: number; // unix sec the pending code expires
}

export interface StartResult {
  code: string;
  expires_at: number;
}

// Thrown by requestPair when the call is rate-limited or racing another
// pair. Carries an HTTP-status hint the server layer maps directly.
export class PairError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

// PAIR_TTL_MS: how long the operator has to type the code into the
// phone before we wipe state. WhatsApp's UI typically shows the code
// for ~60s; matching keeps the agent's "expired" message honest.
export const PAIR_TTL_MS = 60_000;

// RATE_WINDOW_MS / RATE_MAX: WhatsApp bans the account on excess
// link-device attempts; 5/hr is a self-imposed guard well under that.
const RATE_WINDOW_MS = 60 * 60 * 1000;
const RATE_MAX = 5;

// Builder a freshly-authed socket plus a helper that returns a promise
// resolving to the pairing code once Baileys mints one. The build path
// is injected to keep WhapdBot testable without Baileys.
export type SocketBuilder = (phone: string) => Promise<{
  sock: WASocket;
  codePromise: Promise<string>;
  openPromise: Promise<void>; // resolves on connection.update.open after save
}>;

export class WhapdBot {
  sock: WASocket | null = null;
  suspended = false; // connect() reconnect loop checks this
  private pairing: PairStatus = { state: 'idle' };
  private starts: number[] = []; // unix-ms timestamps of recent starts
  private deadline: ReturnType<typeof setTimeout> | null = null;
  private mu: Promise<void> = Promise.resolve();
  private build: SocketBuilder;
  private authDirExists: () => boolean;

  constructor(build: SocketBuilder, authDirExists: () => boolean) {
    this.build = build;
    this.authDirExists = authDirExists;
  }

  // Mark the bot unauthenticated once the connect() loop has detected a
  // 401-style invalidation (or never had creds). The status endpoint
  // reads this so dashd can render "session lost".
  markUnauthenticated(): void {
    this.pairing = { state: 'unauthenticated', since: nowSec() };
  }

  markIdle(): void {
    if (this.deadline) {
      clearTimeout(this.deadline);
      this.deadline = null;
    }
    this.pairing = { state: 'idle' };
  }

  getStatus(): PairStatus {
    if (this.pairing.state === 'idle' && !this.authDirExists()) {
      return { state: 'unauthenticated' };
    }
    // Redact the code: only requestPair() returns it.
    const { state, since, expires_at } = this.pairing;
    const out: PairStatus = { state };
    if (since !== undefined) out.since = since;
    if (expires_at !== undefined) out.expires_at = expires_at;
    return out;
  }

  // requestPair runs the pair flow under a mutex: at most one
  // requesting/pending window at a time. Returns code + expiry once
  // Baileys mints the code; the open-side runs in the background and
  // swaps the authed socket back into this.sock when it completes.
  async requestPair(phone: string): Promise<StartResult> {
    return this.withMutex(async () => {
      if (
        this.pairing.state === 'requesting' ||
        this.pairing.state === 'pending'
      ) {
        throw new PairError(429, 'pair in progress');
      }
      this.pruneStarts();
      if (this.starts.length >= RATE_MAX) {
        throw new PairError(429, 'too many attempts');
      }
      this.starts.push(Date.now());

      this.pairing = { state: 'requesting', since: nowSec() };
      this.suspended = true;
      // End the live socket; the reconnect loop will see `suspended`
      // and back off.
      try {
        this.sock?.end(undefined);
      } catch {}
      this.sock = null;

      let built;
      try {
        built = await this.build(phone);
      } catch (e) {
        this.suspended = false;
        this.markIdle();
        throw new PairError(500, String((e as Error).message ?? e));
      }

      let code: string;
      try {
        code = await built.codePromise;
      } catch (e) {
        this.suspended = false;
        try {
          built.sock.end(undefined);
        } catch {}
        this.markIdle();
        throw new PairError(500, String((e as Error).message ?? e));
      }

      const expiresAt = Math.floor((Date.now() + PAIR_TTL_MS) / 1000);
      this.pairing = {
        state: 'pending',
        since: nowSec(),
        expires_at: expiresAt,
      };
      this.deadline = setTimeout(
        () => this.expirePending(built.sock),
        PAIR_TTL_MS,
      );

      // Background: when the user types the code, the open promise
      // resolves; swap socket. If the creds-save fails or the build
      // throws inside, drop back to idle so /status reflects truth.
      built.openPromise.then(
        () => {
          if (this.deadline) {
            clearTimeout(this.deadline);
            this.deadline = null;
          }
          this.sock = built.sock;
          this.suspended = false;
          this.pairing = { state: 'idle' };
          log('info', 'pair completed; socket swapped');
        },
        (err) => {
          log('error', 'pair open failed', { err: String(err) });
          if (this.deadline) {
            clearTimeout(this.deadline);
            this.deadline = null;
          }
          try {
            built.sock.end(undefined);
          } catch {}
          this.suspended = false;
          this.pairing = { state: 'unauthenticated', since: nowSec() };
        },
      );

      return { code, expires_at: expiresAt };
    });
  }

  private expirePending(builtSock: WASocket): void {
    if (this.pairing.state !== 'pending') return;
    log('warn', 'pair expired without phone entry');
    try {
      builtSock.end(undefined);
    } catch {}
    this.deadline = null;
    this.suspended = false;
    this.pairing = { state: 'unauthenticated', since: nowSec() };
  }

  private pruneStarts(): void {
    const cutoff = Date.now() - RATE_WINDOW_MS;
    this.starts = this.starts.filter((t) => t > cutoff);
  }

  private async withMutex<T>(fn: () => Promise<T>): Promise<T> {
    const prev = this.mu;
    let release!: () => void;
    this.mu = new Promise<void>((r) => {
      release = r;
    });
    await prev;
    try {
      return await fn();
    } finally {
      release();
    }
  }
}

function nowSec(): number {
  return Math.floor(Date.now() / 1000);
}

// authDirExistsFn: utility used by main.ts to expose creds-presence to
// the bot without tying WhapdBot to a specific path.
export function authDirHasCreds(dir: string): boolean {
  try {
    return fs.statSync(`${dir}/creds.json`).size > 0;
  } catch {
    return false;
  }
}
