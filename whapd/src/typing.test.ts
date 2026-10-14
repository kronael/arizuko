import { afterEach, beforeEach, describe, expect, it, jest } from 'bun:test';
import { TypingRefresher } from './typing';

function counter() {
  let n = 0;
  const fn: (jid: string) => Promise<void> = async () => {
    n++;
  };
  return { fn, get: () => n };
}

describe('TypingRefresher', () => {
  beforeEach(() => jest.useFakeTimers());
  afterEach(() => {
    jest.clearAllTimers();
    jest.useRealTimers();
  });
  it('sends immediately and repeats until set(false)', () => {
    const s = counter(),
      c = counter();
    const r = new TypingRefresher(20, 1000, s.fn, c.fn);

    r.set('jid1', true);
    expect(s.get()).toBe(1);
    jest.advanceTimersByTime(19);
    expect(s.get()).toBe(1);
    jest.advanceTimersByTime(1);
    expect(s.get()).toBe(2);
    jest.advanceTimersByTime(40);
    expect(s.get()).toBe(4);

    r.set('jid1', false);
    expect(c.get()).toBe(1);
    expect(r.activeCount()).toBe(0);

    const before = s.get();
    jest.advanceTimersByTime(60);
    expect(s.get()).toBe(before);
  });

  it('stops at maxTtl without set(false)', () => {
    const s = counter();
    const r = new TypingRefresher(10, 55, s.fn, null);

    r.set('jid1', true);
    jest.advanceTimersByTime(54);
    expect(s.get()).toBe(6);
    expect(r.activeCount()).toBe(1);
    jest.advanceTimersByTime(1);
    const got = s.get();
    expect(got).toBe(6);
    expect(r.activeCount()).toBe(0);

    jest.advanceTimersByTime(60);
    expect(s.get()).toBe(got);
  });

  it('maxTtl fires clear callback', () => {
    const c = counter();
    const r = new TypingRefresher(10, 55, async () => {}, c.fn);

    r.set('jid1', true);
    jest.advanceTimersByTime(54);
    expect(c.get()).toBe(0);
    jest.advanceTimersByTime(1);
    expect(c.get()).toBe(1);
    expect(r.activeCount()).toBe(0);
  });

  it('per-jid isolation', () => {
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
    jest.advanceTimersByTime(60);
    r.set('jidA', false);
    jest.advanceTimersByTime(60);

    expect(r.activeCount()).toBe(1);
    expect(sends['jidA']).toBe(5);
    expect(sends['jidB']).toBe(9);

    r.set('jidB', false);
    expect(r.activeCount()).toBe(0);
  });

  it('re-entrant set(true) cancels prior loop', () => {
    const s = counter();
    const r = new TypingRefresher(20, 1000, s.fn, null);

    r.set('jid1', true);
    jest.advanceTimersByTime(30);
    expect(s.get()).toBe(2);
    r.set('jid1', true);
    expect(r.activeCount()).toBe(1);
    expect(s.get()).toBe(3);
    jest.advanceTimersByTime(10);
    expect(s.get()).toBe(3);
    jest.advanceTimersByTime(10);
    expect(s.get()).toBe(4);
    jest.advanceTimersByTime(960);
    expect(r.activeCount()).toBe(1);
    jest.advanceTimersByTime(20);
    expect(r.activeCount()).toBe(0);
    r.set('jid1', false);

    const before = s.get();
    jest.advanceTimersByTime(60);
    expect(s.get()).toBe(before);
  });

  it('stop() cancels all active loops', () => {
    const s = counter();
    const r = new TypingRefresher(15, 1000, s.fn, null);

    r.set('jidA', true);
    r.set('jidB', true);
    jest.advanceTimersByTime(30);
    r.stop();
    expect(r.activeCount()).toBe(0);

    const before = s.get();
    jest.advanceTimersByTime(60);
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
    jest.advanceTimersByTime(50);
    r.set('jid1', false);
    expect(n).toBe(4);
    await Promise.resolve();
  });

  it('rapid on/off does not leak timers', () => {
    const s = counter(),
      c = counter();
    const r = new TypingRefresher(20, 1000, s.fn, c.fn);

    r.set('jid1', true);
    r.set('jid1', false);
    expect(r.activeCount()).toBe(0);
    expect(s.get()).toBe(1);
    expect(c.get()).toBe(1);

    r.set('jid1', true);
    jest.advanceTimersByTime(30);
    expect(s.get()).toBe(3);
    r.set('jid1', false);
    expect(r.activeCount()).toBe(0);
    expect(jest.getTimerCount()).toBe(0);
  });

  it('set(false) on unknown jid fires clear', () => {
    const c = counter();
    const r = new TypingRefresher(20, 1000, async () => {}, c.fn);
    r.set('ghost', false);
    expect(c.get()).toBe(1);
    expect(r.activeCount()).toBe(0);
  });
});
