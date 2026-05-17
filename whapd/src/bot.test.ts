import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { PAIR_TTL_MS, PairError, WhapdBot, type SocketBuilder } from './bot';

// Stub the bits of WASocket the bot interacts with.
function stubSock(onEnd?: () => void) {
  let ended = false;
  return {
    end: () => {
      ended = true;
      onEnd?.();
    },
    isEnded: () => ended,
    sendMessage: async () => ({}),
  } as any;
}

// makeBuilder: deferred-promise harness so each test drives codePromise
// and openPromise on its own timeline.
function makeBuilder() {
  let codeResolve!: (v: string) => void;
  let codeReject!: (e: unknown) => void;
  let openResolve!: () => void;
  let openReject!: (e: unknown) => void;
  let buildReject: ((e: unknown) => void) | null = null;
  const built: {
    sock: any;
    codePromise: Promise<string>;
    openPromise: Promise<void>;
  }[] = [];
  const calls: string[] = [];
  const builder: SocketBuilder = async (phone) => {
    calls.push(phone);
    if (buildReject) {
      const r = buildReject;
      buildReject = null;
      throw r as any;
    }
    const codePromise = new Promise<string>((res, rej) => {
      codeResolve = res;
      codeReject = rej;
    });
    const openPromise = new Promise<void>((res, rej) => {
      openResolve = res;
      openReject = rej;
    });
    const sock = stubSock();
    const b = { sock, codePromise, openPromise };
    built.push(b);
    return b;
  };
  return {
    builder,
    calls,
    built,
    resolveCode: (c: string) => codeResolve(c),
    rejectCode: (e: unknown) => codeReject(e),
    resolveOpen: () => openResolve(),
    rejectOpen: (e: unknown) => openReject(e),
    failBuildOnce: (e: unknown) => {
      buildReject = (e as any) ?? new Error('build');
    },
  };
}

describe('WhapdBot.requestPair', () => {
  let credsExist: boolean;
  beforeEach(() => {
    credsExist = true;
  });

  it('parallel start race: one wins, other gets 429', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    const p1 = bot.requestPair('+1');
    // Run the second start concurrently; mutex serialises.
    const p2 = bot.requestPair('+2').catch((e) => e);
    // Drive first.
    queueMicrotask(() => h.resolveCode('CODE1'));
    const r1 = await p1;
    expect(r1.code).toBe('CODE1');
    const r2 = await p2;
    expect(r2).toBeInstanceOf(PairError);
    expect((r2 as PairError).status).toBe(429);
    expect((r2 as PairError).message).toBe('pair in progress');
  });

  it('status redacts code during pending window', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    const p = bot.requestPair('+1');
    queueMicrotask(() => h.resolveCode('SECRET99'));
    const r = await p;
    expect(r.code).toBe('SECRET99');
    const st = bot.getStatus() as Record<string, unknown>;
    expect(st['state']).toBe('pending');
    expect(st['code']).toBeUndefined();
    expect(typeof st['expires_at']).toBe('number');
  });

  it('60s timeout cleans up; next start succeeds', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    const orig = setTimeout;
    let timerFn: (() => void) | null = null;
    // Capture the deadline timer without sleeping 60s.
    (globalThis as any).setTimeout = ((fn: () => void, ms: number) => {
      if (ms === PAIR_TTL_MS) {
        timerFn = fn;
        return 0 as any;
      }
      return orig(fn, ms);
    }) as any;
    try {
      const p = bot.requestPair('+1');
      queueMicrotask(() => h.resolveCode('C1'));
      await p;
      expect(typeof timerFn).toBe('function');
      timerFn!(); // fire the 60s timeout
    } finally {
      (globalThis as any).setTimeout = orig;
    }
    // After timeout, state must clear so a second start works.
    expect(bot.getStatus().state).not.toBe('pending');
    const h2 = makeBuilder();
    (bot as any).build = h2.builder;
    const p2 = bot.requestPair('+1');
    queueMicrotask(() => h2.resolveCode('C2'));
    const r = await p2;
    expect(r.code).toBe('C2');
  });

  it('requesting -> failed when Baileys throws on requestPairingCode', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    const p = bot.requestPair('+1');
    queueMicrotask(() => h.rejectCode(new Error('baileys boom')));
    let err: PairError | null = null;
    try {
      await p;
    } catch (e) {
      err = e as PairError;
    }
    expect(err).toBeInstanceOf(PairError);
    expect(err!.status).toBe(500);
    // State returns to idle; suspended cleared so connect() resumes.
    expect(bot.getStatus().state).toBe('idle');
    expect(bot.suspended).toBe(false);
  });

  it('rate-limit: 6th start within an hour returns 429', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    // Burn 5 starts, each completing pending then expiring back to idle.
    const orig = setTimeout;
    (globalThis as any).setTimeout = ((fn: () => void, ms: number) => {
      if (ms === PAIR_TTL_MS) {
        setImmediate(fn);
        return 0 as any;
      }
      return orig(fn, ms);
    }) as any;
    try {
      for (let i = 0; i < 5; i++) {
        const h2 = makeBuilder();
        (bot as any).build = h2.builder;
        const p = bot.requestPair('+1');
        queueMicrotask(() => h2.resolveCode(`C${i}`));
        await p;
        // Wait for setImmediate-scheduled expiry to flush.
        await new Promise((r) => setImmediate(r));
      }
    } finally {
      (globalThis as any).setTimeout = orig;
    }
    const h3 = makeBuilder();
    (bot as any).build = h3.builder;
    let err: PairError | null = null;
    try {
      await bot.requestPair('+1');
    } catch (e) {
      err = e as PairError;
    }
    expect(err).toBeInstanceOf(PairError);
    expect(err!.status).toBe(429);
    expect(err!.message).toBe('too many attempts');
    // Crucially: the builder must NOT have been called on the rejected attempt.
    expect(h3.calls.length).toBe(0);
  });

  it('open-side failure (disk write) drops back, state != pending', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    const p = bot.requestPair('+1');
    queueMicrotask(() => h.resolveCode('C'));
    await p;
    expect(bot.getStatus().state).toBe('pending');
    h.rejectOpen(new Error('disk full'));
    // Let microtasks flush so the rejection handler runs.
    await new Promise((r) => setImmediate(r));
    const st = bot.getStatus();
    expect(st.state).not.toBe('pending');
    expect(bot.suspended).toBe(false);
  });

  it('socket-swap: open success leaves bot.sock = built.sock', async () => {
    const h = makeBuilder();
    const bot = new WhapdBot(h.builder, () => credsExist);
    const p = bot.requestPair('+1');
    queueMicrotask(() => h.resolveCode('C'));
    await p;
    const built = h.built[0]!;
    h.resolveOpen();
    await new Promise((r) => setImmediate(r));
    expect(bot.sock).toBe(built.sock);
    expect(bot.getStatus().state).toBe('idle');
    expect(bot.suspended).toBe(false);
  });
});
