// Durable outbound dedup — whapd's half of the D5 delivery contract
// (spec 5/34 § Outbound delivery). Every Go adapter has had this since
// chanlib/dedup.go; whapd was the last one outside it, so a routd resend after
// a lost /send response double-posted on WhatsApp (BUGS L32).
//
// ONE FILE PER KEY, not a database. node:22 has no stable `node:sqlite`, and
// the only operation that must be atomic here is the CLAIM — which
// `open(path, 'wx')` (O_CREAT|O_EXCL) already is, at the filesystem level, with
// no library and no runtime bump. The ledger is a directory of small JSON
// files under whapd's own mounted state dir; nothing else reads it.
//
// The record is written BEFORE the platform call, which is the whole point: a
// lost response leaves `inflight`, and the next attempt on the same key is
// refused rather than duplicated.

import fs from 'node:fs';
import path from 'node:path';
import { log } from './log.js';

// DeliveryUnresolvedError reports a redelivery whose earlier attempt never
// settled. whapd started that WhatsApp call and never learned whether it
// landed, so posting again may duplicate the message and skipping may drop it.
// Neither is the adapter's call to make: it reports the ambiguity, routd
// dead-letters the row, and an operator sees it (mirrors
// chanlib.ErrDeliveryUnresolved).
export class DeliveryUnresolvedError extends Error {
  constructor(key: string) {
    super(`earlier delivery attempt never settled: ${key}`);
    this.name = 'DeliveryUnresolvedError';
  }
}

interface Record {
  state: 'inflight' | 'sent';
  chat_jid: string;
  platform_id?: string;
  created_at: string;
  settled_at?: string;
}

export interface Claim {
  // done is true when the caller must NOT call WhatsApp: the key was already
  // delivered and platformId is the id it got.
  done: boolean;
  platformId?: string;
}

// keyFile maps a ledger key to a filename. The keys are message ids, which
// carry '/' and ':' on some platforms, so they are encoded rather than used raw
// — a raw key would escape the ledger directory. Both ledgers in this daemon
// read it, so the encoding cannot differ between what one writes and another
// looks for.
export function keyFile(dir: string, key: string): string {
  return path.join(dir, encodeURIComponent(key) + '.json');
}

export class DedupLedger {
  constructor(private dir: string) {
    fs.mkdirSync(dir, { recursive: true });
  }

  // claim records the intent to deliver key and reports whether the caller may
  // proceed. Throws DeliveryUnresolvedError when an earlier attempt is still
  // inflight.
  claim(key: string, chatJid: string): Claim {
    const file = keyFile(this.dir, key);
    const rec: Record = {
      state: 'inflight',
      chat_jid: chatJid,
      created_at: new Date().toISOString(),
    };
    try {
      // 'wx' is O_CREAT|O_EXCL: exactly one caller can create the file, and
      // that is the claim. No lock, no read-then-write race.
      fs.writeFileSync(file, JSON.stringify(rec), { flag: 'wx' });
      return { done: false };
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e;
    }
    const prior = this.read(file);
    if (prior?.state === 'sent') {
      log('info', 'outbound already delivered; not posting again', {
        idempotency_key: key,
        chat_jid: chatJid,
        platform_id: prior.platform_id ?? '',
      });
      return {
        done: true,
        ...(prior.platform_id ? { platformId: prior.platform_id } : {}),
      };
    }
    throw new DeliveryUnresolvedError(key);
  }

  // settle records that WhatsApp accepted the message.
  settle(key: string, platformId: string, chatJid: string): void {
    const file = keyFile(this.dir, key);
    const rec: Record = {
      state: 'sent',
      chat_jid: chatJid,
      platform_id: platformId,
      created_at: this.read(file)?.created_at ?? new Date().toISOString(),
      settled_at: new Date().toISOString(),
    };
    try {
      // tmp + rename so a crash never leaves a half-written record that reads
      // as neither inflight nor sent.
      const tmp = file + '.tmp';
      fs.writeFileSync(tmp, JSON.stringify(rec));
      fs.renameSync(tmp, file);
    } catch (e) {
      // The message IS delivered; only the record failed. Say so loudly — the
      // next attempt on this key now refuses instead of resending.
      log('error', 'outbound delivered but the dedup record failed', {
        idempotency_key: key,
        platform_id: platformId,
        err: String(e),
      });
    }
  }

  // release drops a claim after a call that definitively did NOT deliver, so
  // the key is free for routd's retry. Keeping it would refuse every later
  // attempt.
  release(key: string): void {
    const file = keyFile(this.dir, key);
    try {
      if (this.read(file)?.state === 'sent') return;
      fs.unlinkSync(file);
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'ENOENT') return;
      log('error', 'releasing an undelivered dedup claim failed', {
        idempotency_key: key,
        err: String(e),
      });
    }
  }

  private read(file: string): Record | null {
    try {
      return JSON.parse(fs.readFileSync(file, 'utf8')) as Record;
    } catch {
      return null;
    }
  }
}

// deliverOnce runs post exactly once per idempotency key — the single place
// whapd enforces the outbound contract, mirroring chanlib.deliverOnce.
//
// An empty key disables dedup and always proceeds: routd issues keyless sends
// (SendVia) and local dev runs without a ledger.
//
// ON FAILURE THE CLAIM IS HELD, and that is deliberate. Baileys reports no
// signal that separates "WhatsApp refused it" from "the ack was lost", and
// guessing either duplicates a message or drops one. The transient that would
// otherwise become a guaranteed dead-letter is handled EARLIER instead: whapd
// refuses a send outright when the socket is not open, before any claim exists
// (server.ts requireSock), so a routd restart or a dropped link answers 502
// with nothing recorded and routd's sweep retries freely. What remains held is
// the genuinely ambiguous case, which is the one an operator must see.
export async function deliverOnce(
  ledger: DedupLedger | null,
  key: string,
  chatJid: string,
  post: () => Promise<string>,
): Promise<string> {
  if (!ledger || !key) return post();
  const claim = ledger.claim(key, chatJid);
  if (claim.done) return claim.platformId ?? '';
  const id = await post();
  ledger.settle(key, id, chatJid);
  return id;
}
