import type { Scraper } from './twitter.js';

export interface ParsedJid {
  kind: 'home' | 'tweet' | 'dm' | 'user' | 'unknown';
  id: string;
}

// parseJid splits "x:<kind>/<id>" or "x:home" into kind+id.
export function parseJid(jid: string): ParsedJid {
  const bare = jid.replace(/^x:/, '');
  if (bare === 'home') return { kind: 'home', id: '' };
  const slash = bare.indexOf('/');
  if (slash < 0) return { kind: 'unknown', id: bare };
  const kind = bare.slice(0, slash);
  const id = bare.slice(slash + 1);
  if (kind === 'tweet' || kind === 'dm' || kind === 'user') {
    return { kind, id };
  }
  return { kind: 'unknown', id };
}

// readResponseId pulls the new tweet id from the JSON body returned by
// agent-twitter-client's tweet-creating methods (sendTweet/sendQuoteTweet).
// Best effort — the library returns a fetch Response; we drain it.
export async function readResponseId(
  resp: Response | unknown,
): Promise<string> {
  if (!resp || typeof resp !== 'object') return '';
  const r = resp as {
    json?: () => Promise<unknown>;
    text?: () => Promise<string>;
  };
  if (typeof r.json !== 'function') return '';
  try {
    const body = (await r.json()) as Record<string, unknown>;
    const data = body['data'] as Record<string, unknown> | undefined;
    const create = data?.['create_tweet'] as
      | Record<string, unknown>
      | undefined;
    const tweet = create?.['tweet_results'] as
      | Record<string, unknown>
      | undefined;
    const result = tweet?.['result'] as Record<string, unknown> | undefined;
    const restId = result?.['rest_id'];
    if (typeof restId === 'string') return restId;
  } catch {
    // ignore
  }
  return '';
}

// send delivers a DM to a conversation parsed from "x:dm/<conv_id>".
export async function send(
  s: Scraper,
  chatJid: string,
  text: string,
): Promise<void> {
  const j = parseJid(chatJid);
  if (j.kind !== 'dm')
    throw new Error(`send requires x:dm/<id>, got ${chatJid}`);
  await s.sendDirectMessage(j.id, text);
}

// post writes a tweet to the user's timeline. chatJid is "x:home" (or "x:user/<self>");
// we don't read it — the library always posts as the authenticated user.
export async function post(
  s: Scraper,
  text: string,
  media?: { data: Buffer; mediaType: string }[],
): Promise<string> {
  const r = await s.sendTweet(text, undefined, media);
  return readResponseId(r);
}

// reply threads a tweet under replyTo (a tweet_id, e.g. "x:tweet/<id>" parsed).
export async function reply(
  s: Scraper,
  replyTo: string,
  text: string,
  media?: { data: Buffer; mediaType: string }[],
): Promise<string> {
  const id = stripTweetPrefix(replyTo);
  const r = await s.sendTweet(text, id, media);
  return readResponseId(r);
}

// repost retweets the target.
export async function repost(s: Scraper, target: string): Promise<void> {
  await s.retweet(stripTweetPrefix(target));
}

// quote sends a quote-tweet of target.
export async function quote(
  s: Scraper,
  target: string,
  text: string,
  media?: { data: Buffer; mediaType: string }[],
): Promise<string> {
  const r = await s.sendQuoteTweet(text, stripTweetPrefix(target), media);
  return readResponseId(r);
}

// like favorites a tweet. The reaction param is ignored — X likes have no emoji.
export async function like(s: Scraper, target: string): Promise<void> {
  await s.likeTweet(stripTweetPrefix(target));
}

// del deletes one of the bot's own tweets.
export async function del(s: Scraper, target: string): Promise<void> {
  await s.deleteTweet(stripTweetPrefix(target));
}

// stripTweetPrefix accepts either a bare id "1234..." or "x:tweet/1234..." / "tweet/1234".
export function stripTweetPrefix(target: string): string {
  const j = parseJid(target);
  if (j.kind === 'tweet') return j.id;
  // already-bare id
  if (/^\d+$/.test(target)) return target;
  // best-effort fallback
  return j.id || target;
}
