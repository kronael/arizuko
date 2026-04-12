// TypingRefresher keeps the WhatsApp "composing" presence alive across long
// agent runs. Baileys sendPresenceUpdate('composing') decays in ~25s on the
// server side, so a single call would drop the indicator mid-run. On
// set(jid, true) we send once, then re-send every refreshMs up to maxTtlMs,
// or until set(jid, false) / stop(). Set(jid, false) sends the 'paused'
// state once. Mirrors chanlib.TypingRefresher on the Go adapters.

type SendFn = (jid: string) => Promise<void>;

interface Entry {
  interval: ReturnType<typeof setInterval>;
  deadline: ReturnType<typeof setTimeout>;
}

export class TypingRefresher {
  private readonly active = new Map<string, Entry>();

  constructor(
    private readonly refreshMs: number,
    private readonly maxTtlMs: number,
    private readonly send: SendFn,
    private readonly clear: SendFn | null,
  ) {}

  set(jid: string, on: boolean): void {
    const existing = this.active.get(jid);
    if (existing) {
      clearInterval(existing.interval);
      clearTimeout(existing.deadline);
      this.active.delete(jid);
    }
    if (!on) {
      if (this.clear) this.clear(jid).catch(() => {});
      return;
    }
    this.send(jid).catch(() => {});
    const interval = setInterval(() => {
      this.send(jid).catch(() => {});
    }, this.refreshMs);
    const deadline = setTimeout(() => {
      const cur = this.active.get(jid);
      if (cur) {
        clearInterval(cur.interval);
        this.active.delete(jid);
      }
      if (this.clear) this.clear(jid).catch(() => {});
    }, this.maxTtlMs);
    this.active.set(jid, { interval, deadline });
  }

  stop(): void {
    for (const { interval, deadline } of this.active.values()) {
      clearInterval(interval);
      clearTimeout(deadline);
    }
    this.active.clear();
  }

  activeCount(): number {
    return this.active.size;
  }
}
