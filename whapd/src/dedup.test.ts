import { afterEach, describe, expect, it } from 'bun:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { DedupLedger, DeliveryUnresolvedError, deliverOnce } from './dedup';

const dirs: string[] = [];

function tmpLedger(): DedupLedger {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-dedup-'));
  dirs.push(dir);
  return new DedupLedger(dir);
}

afterEach(() => {
  while (dirs.length) fs.rmSync(dirs.pop()!, { recursive: true, force: true });
});

// counter records how many times WhatsApp was actually called.
function counter(id = 'wa-1') {
  let n = 0;
  return {
    calls: () => n,
    post: async () => {
      n++;
      return id;
    },
  };
}

describe('outbound dedup ledger', () => {
  // The D5 case: routd re-sends a row that stays `pending`, so the same key
  // arrives twice by design. The second must post NOTHING.
  it('returns the recorded id without posting again', async () => {
    const l = tmpLedger();
    const c = counter('wa-1');

    const first = await deliverOnce(
      l,
      'row-1',
      'whatsapp:x@s.whatsapp.net',
      c.post,
    );
    const second = await deliverOnce(
      l,
      'row-1',
      'whatsapp:x@s.whatsapp.net',
      c.post,
    );

    expect(first).toBe('wa-1');
    expect(second).toBe('wa-1');
    expect(c.calls()).toBe(1);
  });

  // A lost response leaves the claim inflight. The next attempt must refuse
  // rather than guess: posting again may duplicate, skipping may drop.
  it('refuses a redelivery whose earlier attempt never settled', async () => {
    const l = tmpLedger();
    const boom = async () => {
      throw new Error('socket died mid-send');
    };
    await expect(
      deliverOnce(l, 'row-2', 'whatsapp:x@s.whatsapp.net', boom),
    ).rejects.toThrow('socket died mid-send');

    const c = counter();
    await expect(
      deliverOnce(l, 'row-2', 'whatsapp:x@s.whatsapp.net', c.post),
    ).rejects.toBeInstanceOf(DeliveryUnresolvedError);
    expect(c.calls()).toBe(0);
  });

  // release frees a key for routd's retry after a call that provably did not
  // deliver.
  it('lets a released key be retried', async () => {
    const l = tmpLedger();
    const boom = async () => {
      throw new Error('nope');
    };
    await expect(deliverOnce(l, 'row-3', 'jid', boom)).rejects.toThrow('nope');
    l.release('row-3');

    const c = counter('wa-3');
    expect(await deliverOnce(l, 'row-3', 'jid', c.post)).toBe('wa-3');
    expect(c.calls()).toBe(1);
  });

  // release must never undo a DELIVERED message's record — that would let the
  // next attempt post a second copy.
  it('refuses to release a settled key', async () => {
    const l = tmpLedger();
    const c = counter('wa-4');
    await deliverOnce(l, 'row-4', 'jid', c.post);
    l.release('row-4');

    expect(await deliverOnce(l, 'row-4', 'jid', c.post)).toBe('wa-4');
    expect(c.calls()).toBe(1);
  });

  // The ledger is the durability: a restart must not forget a delivered key.
  it('survives a restart', async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-dedup-'));
    dirs.push(dir);
    const c = counter('wa-5');
    await deliverOnce(new DedupLedger(dir), 'row-5', 'jid', c.post);

    const reopened = new DedupLedger(dir);
    expect(await deliverOnce(reopened, 'row-5', 'jid', c.post)).toBe('wa-5');
    expect(c.calls()).toBe(1);
  });

  // No key means no dedup: routd issues keyless sends (SendVia) and they must
  // still go out.
  it('always posts when the key is empty', async () => {
    const l = tmpLedger();
    const c = counter();
    await deliverOnce(l, '', 'jid', c.post);
    await deliverOnce(l, '', 'jid', c.post);
    expect(c.calls()).toBe(2);
  });

  // A routd message id carries '/' and ':' on some platforms; a raw key as a
  // filename would escape the ledger directory.
  it('handles a key with path separators', async () => {
    const l = tmpLedger();
    const key = 'slack:T/C1/17.99';
    const c = counter('wa-6');
    expect(await deliverOnce(l, key, 'jid', c.post)).toBe('wa-6');
    expect(await deliverOnce(l, key, 'jid', c.post)).toBe('wa-6');
    expect(c.calls()).toBe(1);
  });
});
