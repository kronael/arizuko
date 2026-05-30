import type { InboundMsg } from './client.js';
import { log } from './log.js';
import { snowflakeNewer, type CursorState, type Scraper } from './twitter.js';

// Minimal router surface pollMentions needs — lets tests stub delivery.
export interface MentionSink {
  sendMessage(msg: InboundMsg): Promise<void>;
}

export interface PollResult {
  state: CursorState;
  // undefined = link state unchanged (e.g. no getMentions); true/false when
  // the mentions API either drained cleanly or threw.
  connected?: boolean;
  delivered: number;
}

// pollMentions drains mentions since the cursor and posts to the sink. Pure
// over its inputs (no module globals) so it is unit-testable.
//
// getMentions yields newest-first; we buffer the unseen batch, then deliver
// oldest-first and advance the cursor ONLY after each successful delivery. A
// send failure aborts the batch with the cursor parked just past the last
// delivered item, so the next poll retries the failed mention rather than
// dropping it (message-loss fix). A fetch/auth failure flips connected=false.
export async function pollMentions(
  s: Scraper,
  sink: MentionSink,
  state: CursorState,
): Promise<PollResult> {
  const next: CursorState = { ...state };
  let delivered = 0;
  const m = (
    s as unknown as { getMentions?: (n: number) => AsyncIterable<unknown> }
  ).getMentions;
  if (typeof m !== 'function') return { state: next, delivered };
  try {
    const iter = m.call(s, 20);
    const batch: { id: string; msg: InboundMsg }[] = [];
    for await (const t of iter) {
      const tw = t as Record<string, unknown>;
      const id = String(tw['id'] ?? '');
      if (!id) continue;
      if (state.mentions && !snowflakeNewer(id, state.mentions)) break;
      const sender = String(tw['username'] ?? 'unknown');
      const inReplyTo = tw['inReplyToStatusId']
        ? String(tw['inReplyToStatusId'])
        : undefined;
      const msg: InboundMsg = {
        id,
        chat_jid: 'twitter:home',
        sender: `twitter:user/${sender}`,
        sender_name: String(tw['name'] ?? sender),
        content: String(tw['text'] ?? ''),
        timestamp: Number(tw['timestamp']) || Math.floor(Date.now() / 1000),
        verb: inReplyTo ? 'reply' : 'message',
        ...(inReplyTo ? { reply_to: inReplyTo } : {}),
        // Mentions/replies on the public timeline are multi-actor;
        // DM API isn't wired in twitd yet.
        is_group: true,
      };
      batch.push({ id, msg });
    }
    // Oldest-first so the cursor only ever advances over delivered items.
    for (let i = batch.length - 1; i >= 0; i--) {
      const { id, msg } = batch[i]!;
      try {
        await sink.sendMessage(msg);
      } catch (e) {
        log('error', 'deliver mention failed, aborting batch', {
          id,
          err: String(e),
        });
        // Cursor NOT advanced past this item: next poll retries it.
        return { state: next, connected: true, delivered };
      }
      delivered++;
      if (!next.mentions || snowflakeNewer(id, next.mentions))
        next.mentions = id;
    }
    // The mentions API responded and drained without throwing → link is up.
    return { state: next, connected: true, delivered };
  } catch (e) {
    // Auth/network failure surfaces here — flip /health to disconnected so it
    // stops lying that the platform link is alive.
    log('warn', 'mentions poll failed', { err: String(e) });
    return { state: next, connected: false, delivered };
  }
}
