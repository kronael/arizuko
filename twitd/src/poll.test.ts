import { describe, expect, it } from 'bun:test';
import type { InboundMsg } from './client';
import { pollMentions, type MentionSink } from './poll';
import type { CursorState, Scraper } from './twitter';

// stubScraper exposes getMentions yielding the given tweets newest-first.
// If `throwOnIterate` is set, iteration throws to simulate an API/auth error.
function stubScraper(
  tweets: Record<string, unknown>[],
  throwOnIterate = false,
): Scraper {
  return {
    async *getMentions() {
      if (throwOnIterate) throw new Error('rate limited');
      for (const t of tweets) yield t;
    },
  } as unknown as Scraper;
}

// recordingSink records delivered ids; fails on any id in failIds.
function recordingSink(failIds: Set<string> = new Set()): {
  sink: MentionSink;
  delivered: string[];
} {
  const delivered: string[] = [];
  const sink: MentionSink = {
    async sendMessage(msg: InboundMsg) {
      if (failIds.has(msg.id)) throw new Error('router 500');
      delivered.push(msg.id);
    },
  };
  return { sink, delivered };
}

describe('pollMentions', () => {
  it('delivers oldest-first and advances cursor to newest', async () => {
    // API yields newest-first: 300, 200, 100.
    const s = stubScraper([
      { id: '300', username: 'a', text: 'c' },
      { id: '200', username: 'a', text: 'b' },
      { id: '100', username: 'a', text: 'a' },
    ]);
    const { sink, delivered } = recordingSink();
    const r = await pollMentions(s, sink, {} as CursorState);
    expect(delivered).toEqual(['100', '200', '300']);
    expect(r.state.mentions).toBe('300');
    expect(r.connected).toBe(true);
    expect(r.delivered).toBe(3);
  });

  it('does NOT advance cursor past a mention that failed to deliver', async () => {
    // 100 delivers, 200 fails → cursor must stop at 100 so 200+300 retry.
    const s = stubScraper([
      { id: '300', username: 'a', text: 'c' },
      { id: '200', username: 'a', text: 'b' },
      { id: '100', username: 'a', text: 'a' },
    ]);
    const { sink, delivered } = recordingSink(new Set(['200']));
    const r = await pollMentions(s, sink, {} as CursorState);
    expect(delivered).toEqual(['100']); // 300 never attempted: batch aborted
    expect(r.state.mentions).toBe('100');
    expect(r.delivered).toBe(1);
  });

  it('flips connected=false when the mentions API throws', async () => {
    const s = stubScraper([], true);
    const { sink } = recordingSink();
    const r = await pollMentions(s, sink, {} as CursorState);
    expect(r.connected).toBe(false);
    expect(r.delivered).toBe(0);
  });

  it('skips already-seen mentions (<= cursor)', async () => {
    const s = stubScraper([
      { id: '300', username: 'a', text: 'c' },
      { id: '200', username: 'a', text: 'b' }, // == cursor → stop
    ]);
    const { sink, delivered } = recordingSink();
    const r = await pollMentions(s, sink, { mentions: '200' } as CursorState);
    expect(delivered).toEqual(['300']);
    expect(r.state.mentions).toBe('300');
  });
});
