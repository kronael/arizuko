import http from 'node:http';
import Busboy from 'busboy';
import type { WAMessage, WASocket } from '@whiskeysockets/baileys';
import { log } from './log.js';

interface SendReq {
  chat_jid: string;
  content: string;
  reply_to?: string;
}

interface TypingReq {
  chat_jid: string;
  on: boolean;
}

interface KeyedReq {
  chat_jid: string;
  target_id: string;
}

function mdToWa(text: string): string {
  return text.replace(/\*\*(.*?)\*\*/g, '*$1*').replace(/~~(.*?)~~/g, '~$1~');
}

function toWaJid(jid: string): string {
  const bare = jid.replace(/^whatsapp:/, '');
  return bare.includes('@') ? bare : `${bare}@s.whatsapp.net`;
}

function quotedStub(remoteJid: string, id: string): WAMessage {
  return {
    key: { remoteJid, id, fromMe: false },
    message: { conversation: '' },
  } as WAMessage;
}

function parseMultipart(req: http.IncomingMessage): Promise<{
  chatJid: string;
  filename: string;
  caption: string;
  fileBytes: Buffer | null;
}> {
  return new Promise((resolve, reject) => {
    const bb = Busboy({ headers: req.headers as Record<string, string> });
    const fields: Record<string, string> = {};
    const chunks: Buffer[] = [];
    bb.on('field', (name, val) => {
      fields[name] = val;
    });
    bb.on('file', (_name, stream) => {
      stream.on('data', (d: Buffer) => chunks.push(d));
    });
    bb.on('finish', () =>
      resolve({
        chatJid: fields['chat_jid'] ?? '',
        filename: fields['filename'] ?? '',
        caption: fields['caption'] ?? '',
        fileBytes: chunks.length > 0 ? Buffer.concat(chunks) : null,
      }),
    );
    bb.on('error', reject);
    req.pipe(bb);
  });
}

const MIME_BY_EXT = {
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.png': 'image/png',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.mp4': 'video/mp4',
  '.mov': 'video/quicktime',
  '.mp3': 'audio/mpeg',
  '.ogg': 'audio/ogg',
  '.m4a': 'audio/mp4',
  '.pdf': 'application/pdf',
} satisfies Record<string, string>;

function extToMime(filename: string): string {
  const ext = filename.slice(filename.lastIndexOf('.')).toLowerCase();
  return (
    (MIME_BY_EXT as Record<string, string>)[ext] ?? 'application/octet-stream'
  );
}

// 5-min staleness matches the chanlib Go default.
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
    platform: 'whatsapp',
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

// requireSock returns the live socket or writes 502 and returns null.
function requireSock(
  res: http.ServerResponse,
  sock: () => WASocket | null,
  isConnected: () => boolean,
): WASocket | null {
  const s = sock();
  if (!s || !isConnected()) {
    json(res, 502, { ok: false, error: 'not connected' });
    return null;
  }
  return s;
}

// act runs the action and writes 200 ok / 502 error. Optional id is included on success.
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
  sock: () => WASocket | null,
  isConnected: () => boolean,
  queueOutbound: (jid: string, text: string) => void,
  setTyping: (jid: string, on: boolean) => void,
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
        name: 'whatsapp',
        jid_prefixes: ['whatsapp:'],
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
        const waJid = toWaJid(body.chat_jid);
        const text = mdToWa(body.content);
        const s = sock();
        if (!s || !isConnected()) {
          queueOutbound(waJid, text);
          json(res, 200, { ok: true, queued: true });
          return;
        }
        try {
          const opts = body.reply_to
            ? { quoted: quotedStub(waJid, body.reply_to) }
            : undefined;
          await s.sendMessage(waJid, { text }, opts);
          json(res, 200, { ok: true });
        } catch {
          queueOutbound(waJid, text);
          json(res, 200, { ok: true, queued: true });
        }
        return;
      }

      case '/send-file': {
        const s = requireSock(res, sock, isConnected);
        if (!s) return;
        try {
          const { chatJid, filename, caption, fileBytes } =
            await parseMultipart(req);
          if (!chatJid) {
            json(res, 400, { ok: false, error: 'chat_jid required' });
            return;
          }
          if (!fileBytes) {
            json(res, 400, { ok: false, error: 'file required' });
            return;
          }
          const mime = extToMime(filename || 'file.bin');
          const cap = caption || undefined;
          const content: Record<string, unknown> = { mimetype: mime };
          if (mime.startsWith('image/')) {
            content['image'] = fileBytes;
            content['caption'] = cap;
          } else if (mime.startsWith('video/')) {
            content['video'] = fileBytes;
            content['caption'] = cap;
          } else if (mime.startsWith('audio/')) {
            content['audio'] = fileBytes;
          } else {
            content['document'] = fileBytes;
            content['fileName'] = filename || 'file';
            content['caption'] = cap;
          }
          await s.sendMessage(toWaJid(chatJid), content as any);
          json(res, 200, { ok: true });
        } catch (e) {
          json(res, 502, { ok: false, error: String(e) });
        }
        return;
      }

      case '/send-voice': {
        // WhatsApp's voice-note primitive: send_message({audio, ptt:true}).
        // Differs from /send-file's audio branch (which falls through to
        // music/document encoding); ptt renders the recipient bubble as
        // push-to-talk. Audio bytes must be ogg/opus.
        const s = requireSock(res, sock, isConnected);
        if (!s) return;
        try {
          const { chatJid, filename, fileBytes } = await parseMultipart(req);
          if (!chatJid) {
            json(res, 400, { ok: false, error: 'chat_jid required' });
            return;
          }
          if (!fileBytes) {
            json(res, 400, { ok: false, error: 'file required' });
            return;
          }
          const mime = extToMime(filename || 'voice.ogg');
          await s.sendMessage(toWaJid(chatJid), {
            audio: fileBytes,
            mimetype: mime,
            ptt: true,
          } as any);
          json(res, 200, { ok: true });
        } catch (e) {
          json(res, 502, { ok: false, error: String(e) });
        }
        return;
      }

      case '/typing': {
        const body = await readBody<TypingReq>(req);
        const waJid = toWaJid(body.chat_jid);
        log('debug', 'typing', { jid: waJid, on: body.on });
        setTyping(waJid, body.on);
        json(res, 200, { ok: true });
        return;
      }

      case '/like': {
        const body = await readBody<KeyedReq & { reaction?: string }>(req);
        const s = requireSock(res, sock, isConnected);
        if (!s) return;
        const waJid = toWaJid(body.chat_jid);
        await act(res, async () => {
          await s.sendMessage(waJid, {
            react: {
              text: body.reaction || '👍',
              key: { remoteJid: waJid, id: body.target_id, fromMe: false },
            },
          } as any);
        });
        return;
      }

      case '/delete': {
        const body = await readBody<KeyedReq>(req);
        const s = requireSock(res, sock, isConnected);
        if (!s) return;
        const waJid = toWaJid(body.chat_jid);
        await act(res, async () => {
          await s.sendMessage(waJid, {
            delete: { remoteJid: waJid, id: body.target_id, fromMe: true },
          } as any);
        });
        return;
      }

      case '/edit': {
        const body = await readBody<{
          chat_jid: string;
          target_id: string;
          content: string;
        }>(req);
        const s = requireSock(res, sock, isConnected);
        if (!s) return;
        const waJid = toWaJid(body.chat_jid);
        await act(res, async () => {
          await s.sendMessage(waJid, {
            text: mdToWa(body.content),
            edit: { remoteJid: waJid, id: body.target_id, fromMe: true },
          } as any);
        });
        return;
      }

      // Best-effort forward: Baileys needs the original WAMessage to relay
      // properly; we only have the id, so we synthesize an extendedTextMessage
      // tagged isForwarded.
      case '/forward': {
        const body = await readBody<{
          source_msg_id: string;
          target_jid: string;
          comment?: string;
        }>(req);
        const s = requireSock(res, sock, isConnected);
        if (!s) return;
        const target = toWaJid(body.target_jid);
        const text = body.comment || `Forwarded message ${body.source_msg_id}`;
        await act(res, async () => {
          const sent = await s.sendMessage(target, {
            text,
            contextInfo: { isForwarded: true, forwardingScore: 1 },
          } as any);
          return (sent as any)?.key?.id ?? '';
        });
        return;
      }

      case '/quote':
        unsupported(
          res,
          'quote',
          'WhatsApp has no quote primitive. Use `reply(replyToId=...)` to thread, or `send` with quoted text.',
        );
        return;

      case '/repost':
        unsupported(
          res,
          'repost',
          'WhatsApp is not a feed. Use `forward(target_jid=..., source_msg_id=...)` to relay.',
        );
        return;

      case '/dislike':
        unsupported(
          res,
          'dislike',
          'WhatsApp uses emoji reactions, not a downvote primitive. Use `like(target_id=..., emoji="👎")` to express disagreement.',
        );
        return;

      case '/post':
        unsupported(
          res,
          'post',
          'WhatsApp has no public-feed post primitive. Use `send(jid=<chat>, content=...)` to deliver to a specific chat.',
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
