import http from 'node:http';
import type { WASocket } from '@whiskeysockets/baileys';

interface SendReq {
  chat_jid: string;
  content: string;
  thread_id?: string;
}

interface SendFileReq {
  chat_jid: string;
  // base64-encoded file bytes
  data: string;
  mime: string;
  filename?: string;
  caption?: string;
}

interface TypingReq {
  chat_jid: string;
  on: boolean;
}

function log(level: string, msg: string, attrs?: Record<string, unknown>) {
  const entry = { time: new Date().toISOString(), level, msg, ...attrs };
  process.stderr.write(JSON.stringify(entry) + '\n');
}

// Convert markdown to WhatsApp formatting
function mdToWa(text: string): string {
  return text
    .replace(/\*\*(.*?)\*\*/g, '*$1*') // **bold** → *bold*
    .replace(/~~(.*?)~~/g, '~$1~'); // ~~strike~~ → ~strike~
}

function toWaJid(jid: string): string {
  const bare = jid.replace(/^whatsapp:/, '');
  if (bare.includes('@')) return bare;
  return `${bare}@s.whatsapp.net`;
}

function mimeToMediaType(
  mime: string,
): 'image' | 'video' | 'audio' | 'document' {
  if (mime.startsWith('image/')) return 'image';
  if (mime.startsWith('video/')) return 'video';
  if (mime.startsWith('audio/')) return 'audio';
  return 'document';
}

export function startServer(
  addr: string,
  secret: string,
  sock: () => WASocket | null,
  isConnected: () => boolean,
  queueOutbound: (jid: string, text: string) => void,
): http.Server {
  const srv = http.createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/health') {
      json(res, 200, {
        status: 'ok',
        name: 'whatsapp',
        jid_prefixes: ['whatsapp:'],
      });
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
        // Queue for delivery on reconnect
        queueOutbound(waJid, text);
        json(res, 200, { ok: true, queued: true });
        return;
      }
      try {
        await s.sendMessage(waJid, { text });
        json(res, 200, { ok: true });
      } catch (e: unknown) {
        // Fallback: queue if send fails mid-connection
        queueOutbound(waJid, text);
        json(res, 200, { ok: true, queued: true });
      }
      return;
    }

    if (req.method === 'POST' && req.url === '/send-file') {
      const body = (await readBody(req)) as SendFileReq;
      const s = sock();
      if (!s || !isConnected()) {
        json(res, 502, { ok: false, error: 'not connected' });
        return;
      }
      try {
        const waJid = toWaJid(body.chat_jid);
        const buf = Buffer.from(body.data, 'base64');
        const mediaType = mimeToMediaType(body.mime);

        if (mediaType === 'image') {
          await s.sendMessage(waJid, {
            image: buf,
            mimetype: body.mime,
            caption: body.caption,
          });
        } else if (mediaType === 'video') {
          await s.sendMessage(waJid, {
            video: buf,
            mimetype: body.mime,
            caption: body.caption,
          });
        } else if (mediaType === 'audio') {
          await s.sendMessage(waJid, {
            audio: buf,
            mimetype: body.mime,
          });
        } else {
          await s.sendMessage(waJid, {
            document: buf,
            mimetype: body.mime,
            fileName: body.filename || 'file',
            caption: body.caption,
          });
        }
        json(res, 200, { ok: true });
      } catch (e: unknown) {
        json(res, 502, { ok: false, error: String(e) });
      }
      return;
    }

    if (req.method === 'POST' && req.url === '/typing') {
      const body = (await readBody(req)) as TypingReq;
      const s = sock();
      if (s) {
        const status = body.on ? ('composing' as const) : ('paused' as const);
        s.sendPresenceUpdate(status, toWaJid(body.chat_jid)).catch(() => {});
      }
      json(res, 200, { ok: true });
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
