import http from 'node:http';
import { log } from './log.js';
import type { Scraper } from './twitter.js';
import * as v from './verbs.js';

interface SendReq {
  chat_jid: string;
  content: string;
  reply_to?: string;
}

interface PostReq {
  content: string;
}

interface ReplyReq {
  reply_to: string;
  content: string;
}

interface KeyedReq {
  chat_jid?: string;
  target_id: string;
}

interface QuoteReq {
  target_id: string;
  content: string;
}

const STALE_THRESHOLD_SECONDS = 5 * 60;

function json(res: http.ServerResponse, code: number, body: unknown) {
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
}

function unsupported(res: http.ServerResponse, tool: string, hint: string) {
  json(res, 501, {
    ok: false,
    error: 'unsupported',
    tool,
    platform: 'twitter',
    hint,
  });
}

async function readBody<T = Record<string, unknown>>(
  req: http.IncomingMessage,
): Promise<T> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return JSON.parse(Buffer.concat(chunks).toString()) as T;
}

function requireScraper(
  res: http.ServerResponse,
  scraper: () => Scraper | null,
  isConnected: () => boolean,
): Scraper | null {
  const s = scraper();
  if (!s || !isConnected()) {
    json(res, 502, { ok: false, error: 'not connected' });
    return null;
  }
  return s;
}

async function act(res: http.ServerResponse, fn: () => Promise<string | void>) {
  try {
    const id = (await fn()) ?? '';
    json(res, 200, id ? { ok: true, id } : { ok: true });
  } catch (e) {
    json(res, 502, { ok: false, error: String(e) });
  }
}

export function startServer(
  addr: string,
  secret: string,
  scraper: () => Scraper | null,
  isConnected: () => boolean,
  lastInboundAt: () => number,
): http.Server {
  const srv = http.createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/health') {
      const last = lastInboundAt();
      const staleSec = Math.floor(Date.now() / 1000) - last;
      let status = 'ok';
      let code = 200;
      if (!isConnected()) {
        status = 'disconnected';
        code = 503;
      } else if (last > 0 && staleSec > STALE_THRESHOLD_SECONDS) {
        status = 'stale';
        code = 503;
      }
      const body: Record<string, unknown> = {
        status,
        name: 'twitter',
        jid_prefixes: ['twitter:'],
        connected: isConnected(),
        last_inbound_at: last,
      };
      if (status === 'stale') body['stale_seconds'] = staleSec;
      json(res, code, body);
      return;
    }

    if (secret) {
      const tok = (req.headers.authorization || '').replace('Bearer ', '');
      if (tok !== secret) {
        json(res, 401, { ok: false, error: 'invalid secret' });
        return;
      }
    }

    if (req.method !== 'POST') {
      json(res, 404, { ok: false, error: 'not found' });
      return;
    }

    switch (req.url) {
      case '/send': {
        const body = await readBody<SendReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        await act(res, async () => {
          await v.send(s, body.chat_jid, body.content);
        });
        return;
      }

      case '/post': {
        const body = await readBody<PostReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        await act(res, () => v.post(s, body.content));
        return;
      }

      case '/reply': {
        const body = await readBody<ReplyReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        await act(res, () => v.reply(s, body.reply_to, body.content));
        return;
      }

      case '/repost': {
        const body = await readBody<KeyedReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        await act(res, async () => {
          await v.repost(s, body.target_id);
        });
        return;
      }

      case '/quote': {
        const body = await readBody<QuoteReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        if (!body.content) {
          unsupported(
            res,
            'quote',
            'X quote requires non-empty body. Use `repost(target_id=...)` for plain re-amplification.',
          );
          return;
        }
        await act(res, () => v.quote(s, body.target_id, body.content));
        return;
      }

      case '/like': {
        const body = await readBody<KeyedReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        await act(res, async () => {
          await v.like(s, body.target_id);
        });
        return;
      }

      case '/delete': {
        const body = await readBody<KeyedReq>(req);
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        await act(res, async () => {
          await v.del(s, body.target_id);
        });
        return;
      }

      case '/send-file': {
        // Bun-friendly: parse multipart via Web FormData.
        const s = requireScraper(res, scraper, isConnected);
        if (!s) return;
        try {
          const ctype = req.headers['content-type'] || '';
          if (!ctype.includes('multipart/form-data')) {
            json(res, 400, {
              ok: false,
              error: 'multipart/form-data required',
            });
            return;
          }
          const chunks: Buffer[] = [];
          for await (const chunk of req) chunks.push(chunk as Buffer);
          const buf = Buffer.concat(chunks);
          const parsed = parseMultipart(buf, ctype);
          if (!parsed) {
            json(res, 400, { ok: false, error: 'malformed multipart' });
            return;
          }
          const { chatJid, filename, caption, fileBytes } = parsed;
          if (!fileBytes) {
            json(res, 400, { ok: false, error: 'file required' });
            return;
          }
          const mediaType = extToMime(filename || 'file.bin');
          const media = [{ data: fileBytes, mediaType }];
          await act(res, async () => {
            const j = v.parseJid(chatJid || 'twitter:home');
            if (j.kind === 'dm') {
              // Library doesn't expose DM media; fall back to text-only with note.
              await s.sendDirectMessage(
                j.id,
                caption || `[attachment: ${filename}]`,
              );
              return;
            }
            return await v.post(s, caption || '', media);
          });
        } catch (e) {
          json(res, 502, { ok: false, error: String(e) });
        }
        return;
      }

      // Hint-only verbs.
      case '/forward':
        unsupported(
          res,
          'forward',
          'X has no DM forward primitive. Use `send(chat_jid="twitter:dm/<id>", content=...)` with the original quoted in the body and a permalink.',
        );
        return;

      case '/dislike':
        unsupported(
          res,
          'dislike',
          'X has no downvote. Use `reply(reply_to=..., content=<disagreement>)` or skip.',
        );
        return;

      case '/edit':
        unsupported(
          res,
          'edit',
          'X tweet edit is X Premium-only and not exposed by agent-twitter-client. Use `delete(target_id=...)` then `post(content=...)`.',
        );
        return;

      default:
        json(res, 404, { ok: false, error: 'not found' });
    }
  });

  const [host, port] = parseAddr(addr);
  srv.listen(parseInt(port), host, () => {
    log('info', 'http server starting', { addr });
  });
  return srv;
}

function parseAddr(addr: string): [string, string] {
  if (addr.startsWith(':')) return ['0.0.0.0', addr.slice(1)];
  const i = addr.lastIndexOf(':');
  return [addr.slice(0, i), addr.slice(i + 1)];
}

const MIME_BY_EXT = {
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.png': 'image/png',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.mp4': 'video/mp4',
  '.mov': 'video/quicktime',
} satisfies Record<string, string>;

function extToMime(filename: string): string {
  const ext = filename.slice(filename.lastIndexOf('.')).toLowerCase();
  return (
    (MIME_BY_EXT as Record<string, string>)[ext] ?? 'application/octet-stream'
  );
}

interface MultipartParsed {
  chatJid: string;
  filename: string;
  caption: string;
  fileBytes: Buffer | null;
}

// parseMultipart: minimal RFC 7578 parser sufficient for our 3-field shape
// (chat_jid, filename, file). Avoids pulling in busboy for one route.
export function parseMultipart(
  body: Buffer,
  contentType: string,
): MultipartParsed | null {
  const m = /boundary=(?:"([^"]+)"|([^;]+))/i.exec(contentType);
  if (!m) return null;
  const boundary = '--' + (m[1] || m[2]).trim();
  const result: MultipartParsed = {
    chatJid: '',
    filename: '',
    caption: '',
    fileBytes: null,
  };

  let offset = body.indexOf(boundary);
  if (offset < 0) return null;
  offset += boundary.length;
  while (offset < body.length) {
    if (body[offset] === 0x2d && body[offset + 1] === 0x2d) break; // closing --
    // skip CRLF after boundary
    if (body[offset] === 0x0d) offset += 2;
    const headerEnd = body.indexOf('\r\n\r\n', offset);
    if (headerEnd < 0) break;
    const headers = body.slice(offset, headerEnd).toString('utf8');
    const partStart = headerEnd + 4;
    const nextBoundary = body.indexOf(boundary, partStart);
    if (nextBoundary < 0) break;
    // strip trailing CRLF before the next boundary
    const partEnd = nextBoundary - 2;
    const partBody = body.slice(partStart, partEnd);

    const nameMatch = /name="([^"]+)"/i.exec(headers);
    const filenameMatch = /filename="([^"]+)"/i.exec(headers);
    const name = nameMatch ? nameMatch[1] : '';
    if (filenameMatch) {
      result.filename = filenameMatch[1] || result.filename;
      if (name === 'file') result.fileBytes = partBody;
    } else if (name === 'chat_jid') {
      result.chatJid = partBody.toString('utf8');
    } else if (name === 'filename') {
      result.filename = partBody.toString('utf8');
    } else if (name === 'caption') {
      result.caption = partBody.toString('utf8');
    }
    offset = nextBoundary + boundary.length;
  }
  return result;
}
