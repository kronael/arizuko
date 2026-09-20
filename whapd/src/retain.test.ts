import { afterEach, expect, test } from 'bun:test';
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

// The read receipt tells WhatsApp never to offer the message again, so it may
// only be sent once the message survives a routd restart (BUGS L39).
test('a retained message survives the process that retained it', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'whapd-retain-'));
  dirs.push(dir);
  expect(new InboundLedger(dir).retain(msg('m1'))).toBe(true);

  const reopened = new InboundLedger(dir);
  const held = reopened.pending();
  expect(held).toHaveLength(1);
  expect(held[0].content).toBe('hi m1');
});

test('settling drops the record so it is not delivered twice', () => {
  const l = tmpLedger();
  l.retain(msg('m1'));
  l.settle('m1');
  expect(l.pending()).toHaveLength(0);
  // Settling something already gone is not an error — the sweep and the live
  // path can both reach it.
  l.settle('m1');
});

test('a payload with no id is not retained, so its receipt is withheld', () => {
  const l = tmpLedger();
  expect(l.retain(msg(''))).toBe(false);
  expect(l.pending()).toHaveLength(0);
});

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

// routd being down is the ordinary reason a sweep has work. Walking past the
// failure only repeats the same error and reorders a chat's messages.
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

test('an ids-with-slashes message does not escape the ledger directory', () => {
  const l = tmpLedger();
  expect(l.retain(msg('../../escape'))).toBe(true);
  expect(l.pending().map((m) => m.id)).toEqual(['../../escape']);
});
