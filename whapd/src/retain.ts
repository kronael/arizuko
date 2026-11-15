import fs from 'node:fs';
import path from 'node:path';
import { keyFile } from './dedup.js';
import { log } from './log.js';
import type { RouterMessage } from './inbound.js';

export class InboundLedger {
  private rows = new Map<string, RouterMessage>();
  private draining = false;
  private sending = Promise.resolve();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private send: ((m: RouterMessage) => Promise<void>) | undefined;
  private retryMs = 30_000;

  constructor(private dir: string) {
    fs.mkdirSync(dir, { recursive: true });
    for (const row of this.pending()) this.rows.set(row.id, row);
  }

  // retain writes the payload down and reports whether it is now durable. A
  // false answer means the caller must NOT acknowledge the message to
  // WhatsApp: acknowledging something neither routd nor the disk holds is
  // exactly the loss this ledger exists to stop.
  retain(payload: RouterMessage) {
    if (!payload.id) return false;
    const file = keyFile(this.dir, payload.id);
    try {
      const tmp = file + '.tmp';
      fs.writeFileSync(tmp, JSON.stringify(payload));
      fs.renameSync(tmp, file);
      this.rows.set(payload.id, { ...payload });
      this.rows = new Map(
        Array.from(this.rows).sort(([, a], [, b]) => a.timestamp - b.timestamp),
      );
      this.schedule(this.retryMs);
      return true;
    } catch (e) {
      log(
        'error',
        'retaining an inbound message failed; not acknowledging it',
        {
          id: payload.id,
          chat_jid: payload.chat_jid,
          err: String(e),
        },
      );
      return false;
    }
  }

  settle(id: string, expected?: RouterMessage) {
    if (expected && this.rows.get(id) !== expected) return false;
    try {
      fs.unlinkSync(keyFile(this.dir, id));
    } catch (e) {
      if (!(e instanceof Error && 'code' in e && e.code === 'ENOENT')) {
        log('error', 'dropping a settled inbound record failed', {
          id,
          err: String(e),
        });
        return false;
      }
    }
    this.rows.delete(id);
    if (this.rows.size === 0) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    return true;
  }

  // pending reads every message routd has not accepted yet, oldest first by
  // the timestamp the payload carries. One unreadable record is logged and left
  // on disk, so it never stops whapd starting (BUGS L889); an unreadable
  // directory still throws.
  pending() {
    let names: string[];
    try {
      names = fs.readdirSync(this.dir);
    } catch (e) {
      log(
        'error',
        'the inbound ledger is unreadable; retained messages are NOT being swept',
        {
          dir: this.dir,
          err: String(e),
        },
      );
      throw e;
    }
    const out: RouterMessage[] = [];
    for (const name of names) {
      if (!name.endsWith('.json')) continue;
      try {
        out.push(
          JSON.parse(
            fs.readFileSync(path.join(this.dir, name), 'utf8'),
          ) as RouterMessage,
        );
      } catch (e) {
        log('error', 'retained inbound record is unreadable; skipped', {
          file: name,
          err: String(e),
        });
      }
    }
    out.sort((a, b) => (a.timestamp || 0) - (b.timestamp || 0));
    return out;
  }

  async sweep(send: (m: RouterMessage) => Promise<void>) {
    if (this.draining) return 0;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.draining = true;
    let delay = this.retryMs;
    let delivered = 0;
    try {
      while (this.rows.size > 0) {
        const row = this.rows.values().next().value;
        if (!row) break;
        try {
          await this.deliver(row, send);
        } catch (e) {
          log('warn', 'inbound sweep stopped; messages still held', {
            held: this.rows.size,
            id: row.id,
            err: String(e),
          });
          break;
        }
        if (this.settle(row.id, row)) delivered++;
        else {
          if (this.rows.get(row.id) !== row) delay = 0;
          break;
        }
      }
      return delivered;
    } finally {
      this.draining = false;
      this.schedule(delay);
    }
  }

  deliver(payload: RouterMessage, send: (m: RouterMessage) => Promise<void>) {
    const result = this.sending.then(() => send(payload));
    this.sending = result.catch(() => {});
    return result;
  }

  start(send: (m: RouterMessage) => Promise<void>, retryMs = 30_000) {
    this.send = send;
    this.retryMs = retryMs;
    this.schedule(0);
    return () => {
      this.send = undefined;
      clearTimeout(this.timer);
      this.timer = undefined;
    };
  }

  private schedule(delay: number) {
    const send = this.send;
    if (!send || this.timer || this.draining || this.rows.size === 0) return;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.sweep(send).catch((e) =>
        log('error', 'inbound sweep failed', { err: String(e) }),
      );
    }, delay);
    this.timer.unref();
  }
}
