import http from 'node:http';
import Busboy from 'busboy';
import type { WASocket } from '@whiskeysockets/baileys';
import { log } from './log.js';

interface SendReq {
  chat_jid: string;
  content: string;
}

interface TypingReq {
  chat_jid: string;
  on: boolean;
}

function mdToWa(text: string): string {
  return text.replace(/\*\*(.*?)\*\*/g, '*$1*').replace(/~~(.*?)~~/g, '~$1~');
}

function toWaJid(jid: string): string {
  const bare = jid.replace(/^whatsapp:/, '');
  return bare.includes('@') ? bare : `${bare}@s.whatsapp.net`;
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

function extToMime(filename: string): string {
  const ext = filename.slice(filename.lastIndexOf('.')).toLowerCase();
  const m: Record<string, string> = {
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
  };
  return m[ext] ?? 'application/octet-stream';
}

// whatsapp adapter uses the 5m realtime threshold (matches the Go default).
const STALE_THRESHOLD_SECONDS = 5 * 60;

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

    if (req.method === 'POST' && req.url === '/send') {
      const body = (await readBody(req)) as SendReq;
      const waJid = toWaJid(body.chat_jid);
      const text = mdToWa(body.content);
      const s = sock();
      if (!s || !isConnected()) {
        queueOutbound(waJid, text);
        json(res, 200, { ok: true, queued: true });
        return;
      }
      try {
        await s.sendMessage(waJid, { text });
        json(res, 200, { ok: true });
      } catch {
        queueOutbound(waJid, text);
        json(res, 200, { ok: true, queued: true });
      }
      return;
    }

    if (req.method === 'POST' && req.url === '/send-file') {
      const s = sock();
      if (!s || !isConnected()) {
        json(res, 502, { ok: false, error: 'not connected' });
        return;
      }
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
      } catch (e: unknown) {
        json(res, 502, { ok: false, error: String(e) });
      }
      return;
    }

    if (req.method === 'POST' && req.url === '/typing') {
      const body = (await readBody(req)) as TypingReq;
      const waJid = toWaJid(body.chat_jid);
      log('debug', 'typing', { jid: waJid, on: body.on });
      setTyping(waJid, body.on);
      json(res, 200, { ok: true });
      return;
    }

    // Forward: native WhatsApp forward via Baileys relayMessage with
    // forwardingScore. source_msg_id is the WhatsApp message id; for
    // forwarded relays we synthesize an extendedTextMessage with
    // contextInfo.isForwarded = true. SourceMsgID alone (no source jid)
    // is not enough to fetch the original; we embed `comment` as text.
    if (req.method === 'POST' && req.url === '/forward') {
      const body = (await readBody(req)) as {
        source_msg_id: string;
        target_jid: string;
        comment?: string;
      };
      const s = sock();
      if (!s || !isConnected()) {
        json(res, 502, { ok: false, error: 'not connected' });
        return;
      }
      try {
        const target = toWaJid(body.target_jid);
        const text = body.comment
          ? body.comment
          : `Forwarded message ${body.source_msg_id}`;
        const sent = await s.sendMessage(target, {
          text,
          contextInfo: {
            isForwarded: true,
            forwardingScore: 1,
          } as any,
        } as any);
        const id = (sent as any)?.key?.id ?? '';
        json(res, 200, { ok: true, id });
      } catch (e: unknown) {
        json(res, 502, { ok: false, error: String(e) });
      }
      return;
    }

    // Edit: native WhatsApp edit for own bot messages.
    if (req.method === 'POST' && req.url === '/edit') {
      const body = (await readBody(req)) as {
        chat_jid: string;
        target_id: string;
        content: string;
      };
      const s = sock();
      if (!s || !isConnected()) {
        json(res, 502, { ok: false, error: 'not connected' });
        return;
      }
      try {
        const waJid = toWaJid(body.chat_jid);
        await s.sendMessage(waJid, {
          text: mdToWa(body.content),
          edit: { remoteJid: waJid, id: body.target_id, fromMe: true } as any,
        } as any);
        json(res, 200, { ok: true });
      } catch (e: unknown) {
        json(res, 502, { ok: false, error: String(e) });
      }
      return;
    }

    if (req.method === 'POST' && req.url === '/quote') {
      json(res, 501, {
        ok: false,
        error: 'unsupported',
        tool: 'quote',
        platform: 'whatsapp',
        hint: 'WhatsApp has no quote primitive. Use `reply(replyToId=...)` to thread, or `send` with quoted text.',
      });
      return;
    }

    if (req.method === 'POST' && req.url === '/repost') {
      json(res, 501, {
        ok: false,
        error: 'unsupported',
        tool: 'repost',
        platform: 'whatsapp',
        hint: 'WhatsApp is not a feed. Use `forward(target_jid=..., source_msg_id=...)` to relay.',
      });
      return;
    }

    if (req.method === 'POST' && req.url === '/dislike') {
      json(res, 501, {
        ok: false,
        error: 'unsupported',
        tool: 'dislike',
        platform: 'whatsapp',
        hint: 'WhatsApp has no native downvote. Use `reply` with textual disagreement.',
      });
      return;
    }

    if (req.method === 'POST' && req.url === '/post') {
      json(res, 501, {
        ok: false,
        error: 'unsupported',
        tool: 'post',
        platform: 'whatsapp',
        hint: 'WhatsApp has no public-feed post primitive. Use `send(jid=<chat>, content=...)` to deliver to a specific chat.',
      });
      return;
    }

    if (req.method === 'POST' && req.url === '/like') {
      json(res, 501, {
        ok: false,
        error: 'unsupported',
        tool: 'like',
        platform: 'whatsapp',
        hint: 'WhatsApp message reactions are not implemented; use `reply` with text instead.',
      });
      return;
    }

    if (req.method === 'POST' && req.url === '/delete') {
      json(res, 501, {
        ok: false,
        error: 'unsupported',
        tool: 'delete',
        platform: 'whatsapp',
        hint: 'WhatsApp message deletion via the bot is not implemented; use `edit(target_id=..., content=\"[redacted]\")` for a soft retract.',
      });
      return;
    }

    json(res, 404, { ok: false, error: 'not found' });
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

function json(res: http.ServerResponse, code: number, body: unknown) {
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
}

async function readBody(
  req: http.IncomingMessage,
): Promise<Record<string, any>> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return JSON.parse(Buffer.concat(chunks).toString());
}
