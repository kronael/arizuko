import http from 'node:http';
import Busboy from 'busboy';
import type { WASocket } from '@whiskeysockets/baileys';

interface SendReq {
  chat_jid: string;
  content: string;
  thread_id?: string;
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

// Parse multipart/form-data, return fields and file bytes.
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

// Derive MIME type from filename extension.
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
        const waJid = toWaJid(chatJid);
        const mime = extToMime(filename || 'file.bin');
        const mediaType = mimeToMediaType(mime);
        if (mediaType === 'image') {
          await s.sendMessage(waJid, {
            image: fileBytes,
            mimetype: mime,
            caption: caption || undefined,
          });
        } else if (mediaType === 'video') {
          await s.sendMessage(waJid, {
            video: fileBytes,
            mimetype: mime,
            caption: caption || undefined,
          });
        } else if (mediaType === 'audio') {
          await s.sendMessage(waJid, { audio: fileBytes, mimetype: mime });
        } else {
          await s.sendMessage(waJid, {
            document: fileBytes,
            mimetype: mime,
            fileName: filename || 'file',
            caption: caption || undefined,
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
        const waJid = toWaJid(body.chat_jid);
        log('debug', 'typing', { jid: waJid, status });
        s.sendPresenceUpdate(status, waJid).catch((e) =>
          log('warn', 'presence update failed', {
            jid: waJid,
            status,
            err: String(e),
          }),
        );
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
