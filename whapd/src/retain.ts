// Durable inbound retention — whapd's half of the inbound delivery guarantee
// (spec 5/34). whapd sent the WhatsApp read receipt BEFORE routd had accepted
// the message, and the failure path only logged, so a routd that was restarting
// lost the message and Baileys never offered it again (BUGS L39). slakd and
// discd have had the durable answer since L25, as `chanlib.RouterClient
// .RetainInbound`; whapd is TypeScript and cannot call it, so this is the same
// shape in TS.
//
// ONE FILE PER MESSAGE, not a database, for the reason dedup.ts gives: node:22
// has no stable `node:sqlite`, and the filesystem already provides the only
// atomicity this needs. The directory lives under whapd's own MOUNTED state
// dir — an unmounted one dies with the container, which is the same as having
// no ledger at all.
//
// The record is written BEFORE the read receipt. That ordering is the whole
// fix: after it, losing the message needs the disk to fail, not routd to be
// busy.

import fs from 'node:fs';
import path from 'node:path';
import { keyFile } from './dedup.js';
import { log } from './log.js';
import type { RouterMessage } from './inbound.js';

export class InboundLedger {
  constructor(private dir: string) {
    fs.mkdirSync(dir, { recursive: true });
  }

  // retain writes the payload down and reports whether it is now durable. A
  // false answer means the caller must NOT acknowledge the message to
  // WhatsApp: acknowledging something neither routd nor the disk holds is
  // exactly the loss this ledger exists to stop.
  retain(payload: RouterMessage): boolean {
    if (!payload.id) return false;
    const file = keyFile(this.dir, payload.id);
    try {
      const tmp = file + '.tmp';
      fs.writeFileSync(tmp, JSON.stringify(payload));
      fs.renameSync(tmp, file);
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

  // settle drops the record once routd has accepted the message.
  settle(id: string): void {
    try {
      fs.unlinkSync(keyFile(this.dir, id));
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'ENOENT') return;
      log('error', 'dropping a settled inbound record failed', {
        id,
        err: String(e),
      });
    }
  }

  // pending reads every message routd has not accepted yet, oldest first by
  // the timestamp the payload carries.
  pending(): RouterMessage[] {
    let names: string[];
    try {
      names = fs.readdirSync(this.dir);
    } catch {
      return [];
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
      } catch {
        // A half-written record is not a message. Leave it: the sweep reports
        // the count, and an operator can see the file.
      }
    }
    out.sort((a, b) => (a.timestamp || 0) - (b.timestamp || 0));
    return out;
  }

  // sweep re-offers every retained message to routd, oldest first, and settles
  // each one routd accepts. It stops at the first failure: routd being down is
  // the ordinary reason a sweep has work, and walking the rest only repeats the
  // same error while reordering a chat's messages.
  async sweep(send: (m: RouterMessage) => Promise<void>): Promise<number> {
    const rows = this.pending();
    if (rows.length === 0) return 0;
    let delivered = 0;
    for (const row of rows) {
      try {
        await send(row);
      } catch (e) {
        log('warn', 'inbound sweep stopped; messages still held', {
          held: rows.length - delivered,
          id: row.id,
          err: String(e),
        });
        return delivered;
      }
      this.settle(row.id);
      delivered++;
    }
    log('info', 'inbound sweep delivered messages routd had not accepted', {
      delivered,
    });
    return delivered;
  }
}
