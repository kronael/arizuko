import { describe, expect, it } from 'bun:test';
import { TypingRefresher } from './typing';

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

describe('TypingRefresher', () => {
  it('sends immediately on set(true) and repeats until set(false)', async () => {
    let sends = 0;
    let clears = 0;
    const r = new TypingRefresher(
      20,
      1000,
      async () => {
        sends++;
      },
      async () => {
        clears++;
      },
    );

    r.set('jid1', true);
    await sleep(75); // immediate + ~3 ticks
    expect(sends).toBeGreaterThanOrEqual(3);

    r.set('jid1', false);
    expect(clears).toBe(1);
    expect(r.activeCount()).toBe(0);

    const before = sends;
    await sleep(60);
    expect(sends).toBe(before); // no more sends after stop
  });

  it('stops at maxTtl even without set(false)', async () => {
    let sends = 0;
    const r = new TypingRefresher(
      10,
      50,
      async () => {
        sends++;
      },
      null,
    );

    r.set('jid1', true);
    await sleep(200);
    const afterTtl = sends;
    expect(afterTtl).toBeGreaterThanOrEqual(3);
    expect(afterTtl).toBeLessThanOrEqual(12);
    expect(r.activeCount()).toBe(0);

    await sleep(60);
    expect(sends).toBe(afterTtl); // really stopped
  });

  it('per-jid isolation: set(false) on A does not stop B', async () => {
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

    expect(r.activeCount()).toBe(1); // only jidB active
    expect(sends['jidB'] ?? 0).toBeGreaterThan(sends['jidA'] ?? 0);

    r.set('jidB', false);
    expect(r.activeCount()).toBe(0);
  });

  it('re-entrant set(true) cancels prior loop', async () => {
    let sends = 0;
    const r = new TypingRefresher(
      20,
      1000,
      async () => {
        sends++;
      },
      null,
    );

    r.set('jid1', true);
    await sleep(30);
    r.set('jid1', true); // cancel + restart
    expect(r.activeCount()).toBe(1);
    await sleep(30);
    r.set('jid1', false);

    const before = sends;
    await sleep(60);
    expect(sends).toBe(before);
  });

  it('stop() cancels all active loops', async () => {
    let sends = 0;
    const r = new TypingRefresher(
      15,
      1000,
      async () => {
        sends++;
      },
      null,
    );

    r.set('jidA', true);
    r.set('jidB', true);
    await sleep(30);
    r.stop();
    expect(r.activeCount()).toBe(0);

    const before = sends;
    await sleep(60);
    expect(sends).toBe(before);
  });

  it('swallows send() rejections (no unhandled promise)', async () => {
    let sends = 0;
    const r = new TypingRefresher(
      15,
      1000,
      async () => {
        sends++;
        throw new Error('upstream down');
      },
      null,
    );

    r.set('jid1', true);
    await sleep(50);
    r.set('jid1', false);
    expect(sends).toBeGreaterThanOrEqual(2);
  });

  it('set(false) on unknown jid is a no-op', () => {
    let clears = 0;
    const r = new TypingRefresher(
      20,
      1000,
      async () => {},
      async () => {
        clears++;
      },
    );
    r.set('ghost', false);
    expect(clears).toBe(1); // clear is still fired (mirrors Go chanlib)
    expect(r.activeCount()).toBe(0);
  });
});
