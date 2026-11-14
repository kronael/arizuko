import { afterEach, expect, spyOn, test } from 'bun:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { InboundLedger } from './retain';
import type { RouterMessage } from './inbound';

const dirs: string[] = [];

function tmpLedger(): InboundLedger {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-retain-'));
  dirs.push(dir);
  return new InboundLedger(dir);
}

afterEach(() => {
  for (const d of dirs.splice(0))
    fs.rmSync(d, { recursive: true, force: true });
});

function msg(id: string, ts = 1): RouterMessage {
  return {
    id,
    chat_jid: 'whatsapp:1@s.whatsapp.net',
    sender: 'whatsapp:1@s.whatsapp.net',
    sender_name: 'a',
    content: 'hi ' + id,
    timestamp: ts,
    is_group: false,
  };
}

/** A process restart recovers retained work and can deliver it without another write. */
test('a retained message survives the process that retained it', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-retain-'));
  dirs.push(dir);
  expect(new InboundLedger(dir).retain(msg('m1'))).toBe(true);

  const reopened = new InboundLedger(dir);
  const held = reopened.pending();
  expect(held).toHaveLength(1);
  expect(held[0].content).toBe('hi m1');
  expect(
    await reopened.sweep(async (m) => {
      expect(m.id).toBe('m1');
    }),
  ).toBe(1);
});

/** Acceptance removes retained data, and repeated acknowledgments stay harmless. */
test('settling drops the record so it is not delivered twice', () => {
  const l = tmpLedger();
  l.retain(msg('m1'));
  l.settle('m1');
  expect(l.pending()).toHaveLength(0);
  l.settle('m1');
});

/** Missing identity cannot create an acknowledgeable retained record. */
test('a payload with no id is not retained, so its receipt is withheld', () => {
  const l = tmpLedger();
  expect(l.retain(msg(''))).toBe(false);
  expect(l.pending()).toHaveLength(0);
});

/** A drain preserves the retained timestamp order and clears accepted work. */
test('the sweep delivers what routd had not accepted, oldest first', async () => {
  const l = tmpLedger();
  l.retain(msg('m2', 20));
  l.retain(msg('m1', 10));
  l.retain(msg('m3', 30));

  const seen: string[] = [];
  const n = await l.sweep(async (m) => {
    seen.push(m.id);
  });

  expect(n).toBe(3);
  expect(seen).toEqual(['m1', 'm2', 'm3']);
  expect(l.pending()).toHaveLength(0);
});

/** A delivery failure leaves that row and all later rows pending. */
test('the sweep stops at the first failure and holds the rest', async () => {
  const l = tmpLedger();
  l.retain(msg('m1', 10));
  l.retain(msg('m2', 20));
  l.retain(msg('m3', 30));

  const seen: string[] = [];
  const n = await l.sweep(async (m) => {
    seen.push(m.id);
    if (m.id === 'm2') throw new Error('routd is restarting');
  });

  expect(n).toBe(1);
  expect(seen).toEqual(['m1', 'm2']);
  const held = l
    .pending()
    .map((m) => m.id)
    .sort();
  expect(held).toEqual(['m2', 'm3']);
});

/** An ID with path separators remains inside the configured ledger. */
test('an ids-with-slashes message does not escape the ledger directory', () => {
  const l = tmpLedger();
  expect(l.retain(msg('../../escape'))).toBe(true);
  expect(l.pending().map((m) => m.id)).toEqual(['../../escape']);
});

/** A missing ledger reports its error instead of reporting an empty queue. */
test('pending says so when the ledger cannot be read', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-unreadable-'));
  const ledger = new InboundLedger(dir);
  fs.rmSync(dir, { recursive: true, force: true });

  const written: string[] = [];
  const realWrite = process.stderr.write.bind(process.stderr);
  process.stderr.write = ((chunk: string) => {
    written.push(String(chunk));
    return true;
  }) as typeof process.stderr.write;
  try {
    expect(() => ledger.pending()).toThrow();
  } finally {
    process.stderr.write = realWrite;
  }
  expect(written.join('')).toContain('the inbound ledger is unreadable');
});

/** Repeated drains use the writer-maintained set without directory discovery. */
test('drains never reread retained files', async () => {
  const ledger = tmpLedger();
  ledger.retain(msg('one'));
  const directory = spyOn(fs, 'readdirSync');
  const reads = spyOn(fs, 'readFileSync');
  try {
    expect(await ledger.sweep(async () => {})).toBe(1);
    expect(await ledger.sweep(async () => {})).toBe(0);
    expect(directory.mock.calls).toHaveLength(0);
    expect(reads.mock.calls).toHaveLength(0);
  } finally {
    directory.mockRestore();
    reads.mockRestore();
  }
});

/** A second drain cannot send a row while the first drain owns it. */
test('overlapping drains deliver once', async () => {
  const ledger = tmpLedger();
  ledger.retain(msg('one'));
  let release!: () => void;
  const blocked = new Promise<void>((resolve) => {
    release = resolve;
  });
  let sent = 0;
  const first = ledger.sweep(async () => {
    sent++;
    await blocked;
  });
  expect(
    await ledger.sweep(async () => {
      sent++;
    }),
  ).toBe(0);
  release();
  expect(await first).toBe(1);
  expect(sent).toBe(1);
});

/** Acknowledging an old payload cannot remove a newer retained generation. */
test('a replacement survives the old delivery', async () => {
  const ledger = tmpLedger();
  ledger.retain(msg('one', 1));
  expect(
    await ledger.sweep(async () => {
      ledger.retain(msg('one', 2));
    }),
  ).toBe(0);
  expect(ledger.pending().map((m) => m.timestamp)).toEqual([2]);
  expect(
    await ledger.sweep(async (m) => {
      expect(m.timestamp).toBe(2);
    }),
  ).toBe(1);
});

/** A failed unlink keeps the accepted payload scheduled until removal succeeds. */
test('unlink failure retains the obligation', async () => {
  const ledger = tmpLedger();
  ledger.retain(msg('one'));
  const unlink = spyOn(fs, 'unlinkSync').mockImplementation(() => {
    throw new Error('disk unavailable');
  });
  try {
    expect(await ledger.sweep(async () => {})).toBe(0);
    expect(ledger.pending()).toHaveLength(1);
  } finally {
    unlink.mockRestore();
  }
  expect(await ledger.sweep(async () => {})).toBe(1);
});

/** Idle state owns no timer; settling a retained row cancels its retry. */
test('idle ledger owns no retry timer', async () => {
  const ledger = tmpLedger();
  const timer = spyOn(globalThis, 'setTimeout');
  let sent = 0;
  const stop = ledger.start(async () => {
    sent++;
  }, 1);
  try {
    expect(timer.mock.calls).toHaveLength(0);
    ledger.retain(msg('one'));
    expect(timer.mock.calls).toHaveLength(1);
    ledger.settle('one');
    await Bun.sleep(10);
    expect(sent).toBe(0);
    expect(timer.mock.calls).toHaveLength(1);
  } finally {
    stop();
    timer.mockRestore();
  }
});

/** A retain during a drain schedules the new row without waiting for retry backoff. */
test('retain during delivery wakes the next drain', async () => {
  const ledger = tmpLedger();
  let delivered!: () => void;
  const next = new Promise<void>((resolve) => {
    delivered = resolve;
  });
  const seen: string[] = [];
  async function send(m: RouterMessage) {
    seen.push(m.id);
    if (m.id === 'one') ledger.retain(msg('two', 2));
    else delivered();
  }
  const stop = ledger.start(send, 60_000);
  try {
    ledger.retain(msg('one'));
    await ledger.sweep(send);
    await Promise.race([
      next,
      Bun.sleep(1000).then(() => {
        throw new Error('new work was not scheduled');
      }),
    ]);
    expect(seen).toEqual(['one', 'two']);
  } finally {
    stop();
  }
});

/** Invalid persisted JSON prevents a successful recovery with missing work. */
test('corrupt retained data fails recovery', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-corrupt-'));
  dirs.push(dir);
  fs.writeFileSync(path.join(dir, 'bad.json'), '{');
  expect(() => new InboundLedger(dir)).toThrow();
  expect(fs.readFileSync(path.join(dir, 'bad.json'), 'utf8')).toBe('{');
});

/** A new earlier row takes its place before the next send after an await. */
test('retaining during delivery preserves current timestamp order', async () => {
  const ledger = tmpLedger();
  ledger.retain(msg('one', 10));
  ledger.retain(msg('three', 30));
  const seen: string[] = [];
  await ledger.sweep(async (m) => {
    seen.push(m.id);
    if (m.id === 'one') ledger.retain(msg('two', 20));
  });
  expect(seen).toEqual(['one', 'two', 'three']);
});

/** Reusing the caller object still creates a new retained generation. */
test('reusing a payload object preserves replacement work', async () => {
  const ledger = tmpLedger();
  const payload = msg('one', 10);
  ledger.retain(payload);
  expect(
    await ledger.sweep(async () => {
      payload.timestamp = 20;
      ledger.retain(payload);
    }),
  ).toBe(0);
  expect(ledger.pending().map((m) => m.timestamp)).toEqual([20]);
});

/** Failed-retention delivery waits behind the active retained transport call. */
test('fallback transport shares the delivery serializer', async () => {
  const ledger = tmpLedger();
  ledger.retain(msg('retained'));
  let release!: () => void;
  const blocked = new Promise<void>((resolve) => {
    release = resolve;
  });
  let active = 0;
  let peak = 0;
  async function send(m: RouterMessage) {
    active++;
    peak = Math.max(peak, active);
    if (m.id === 'retained') await blocked;
    active--;
  }
  const drain = ledger.sweep(send);
  const fallback = ledger.deliver(msg('fallback'), send);
  release();
  await Promise.all([drain, fallback]);
  expect(peak).toBe(1);
});
