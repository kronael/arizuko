import { describe, expect, it } from 'bun:test';
import { TypingRefresher } from './typing';

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
const counter = () => {
  let n = 0;
  const fn: (jid: string) => Promise<void> = async () => {
    n++;
  };
  return { fn, get: () => n };
};

describe('TypingRefresher', () => {
  it('sends immediately and repeats until set(false)', async () => {
    const s = counter(),
      c = counter();
    const r = new TypingRefresher(20, 1000, s.fn, c.fn);

    r.set('jid1', true);
    await sleep(75);
    expect(s.get()).toBeGreaterThanOrEqual(3);

    r.set('jid1', false);
    expect(c.get()).toBe(1);
    expect(r.activeCount()).toBe(0);

    const before = s.get();
    await sleep(60);
    expect(s.get()).toBe(before);
  });

  it('stops at maxTtl without set(false)', async () => {
    const s = counter();
    const r = new TypingRefresher(10, 50, s.fn, null);

    r.set('jid1', true);
    await sleep(200);
    const got = s.get();
    expect(got).toBeGreaterThanOrEqual(3);
    expect(got).toBeLessThanOrEqual(12);
    expect(r.activeCount()).toBe(0);

    await sleep(60);
    expect(s.get()).toBe(got);
  });

  it('maxTtl fires clear callback', async () => {
    const c = counter();
    const r = new TypingRefresher(10, 50, async () => {}, c.fn);

    r.set('jid1', true);
    await sleep(200);
    expect(c.get()).toBe(1);
    expect(r.activeCount()).toBe(0);
  });

  it('per-jid isolation', async () => {
    const sends: Record<string, number> = {};
    const r = new TypingRefresher(
      15,
      1000,
      async (jid) => {
        sends[jid] = (sends[jid] ?? 0) + 1;
      },
      null,
    );

    r.set('jidA', true);
    r.set('jidB', true);
    await sleep(60);
    r.set('jidA', false);
    await sleep(60);

    expect(r.activeCount()).toBe(1);
    expect(sends['jidB'] ?? 0).toBeGreaterThan(sends['jidA'] ?? 0);

    r.set('jidB', false);
    expect(r.activeCount()).toBe(0);
  });

  it('re-entrant set(true) cancels prior loop', async () => {
    const s = counter();
    const r = new TypingRefresher(20, 1000, s.fn, null);

    r.set('jid1', true);
    await sleep(30);
    r.set('jid1', true);
    expect(r.activeCount()).toBe(1);
    await sleep(30);
    r.set('jid1', false);

    const before = s.get();
    await sleep(60);
    expect(s.get()).toBe(before);
  });

  it('stop() cancels all active loops', async () => {
    const s = counter();
    const r = new TypingRefresher(15, 1000, s.fn, null);

    r.set('jidA', true);
    r.set('jidB', true);
    await sleep(30);
    r.stop();
    expect(r.activeCount()).toBe(0);

    const before = s.get();
    await sleep(60);
    expect(s.get()).toBe(before);
  });

  it('swallows send() rejections', async () => {
    let n = 0;
    const r = new TypingRefresher(
      15,
      1000,
      async () => {
        n++;
        throw new Error('upstream down');
      },
      null,
    );

    r.set('jid1', true);
    await sleep(50);
    r.set('jid1', false);
    expect(n).toBeGreaterThanOrEqual(2);
  });

  it('rapid on/off does not leak timers', async () => {
    const s = counter(),
      c = counter();
    const r = new TypingRefresher(20, 1000, s.fn, c.fn);

    r.set('jid1', true);
    r.set('jid1', false);
    expect(r.activeCount()).toBe(0);
    expect(s.get()).toBe(1);
    expect(c.get()).toBe(1);

    r.set('jid1', true);
    await sleep(30);
    expect(s.get()).toBeGreaterThanOrEqual(2);
    r.set('jid1', false);
    expect(r.activeCount()).toBe(0);
  });

  it('set(false) on unknown jid fires clear', () => {
    const c = counter();
    const r = new TypingRefresher(20, 1000, async () => {}, c.fn);
    r.set('ghost', false);
    expect(c.get()).toBe(1);
    expect(r.activeCount()).toBe(0);
  });
});
