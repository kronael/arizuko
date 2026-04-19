import fs from 'node:fs';
import makeWASocket, {
  Browsers,
  DisconnectReason,
  downloadMediaMessage,
  fetchLatestWaWebVersion,
  makeCacheableSignalKeyStore,
  useMultiFileAuthState,
  type WAMessage,
  type WASocket,
} from '@whiskeysockets/baileys';
import pino from 'pino';
import qrcode from 'qrcode-terminal';
import { RouterClient } from './client.js';
import { log } from './log.js';
import { flushQueue } from './queue.js';
import { extractReplyMeta } from './reply.js';
import { startServer } from './server.js';
import { TypingRefresher } from './typing.js';

const logger = pino({ level: 'warn' });

function env(k: string, def?: string): string {
  const v = process.env[k] || def;
  if (!v) {
    log('error', 'required env var missing', { key: k });
    process.exit(1);
  }
  return v;
}

const assistantName = (process.env['ASSISTANT_NAME'] ?? '').toLowerCase();
const dataDir = process.env['DATA_DIR'] ?? '';

const authDir = env(
  'WHATSAPP_AUTH_DIR',
  dataDir ? `${dataDir}/store/whatsapp-auth` : '/srv/data/store/whatsapp-auth',
);

// Baileys writes creds.json non-atomically; restore from .bak if a prior crash
// left the live file empty.
function recoverCredsIfEmpty(dir: string): void {
  const creds = `${dir}/creds.json`;
  const backup = `${dir}/creds.json.bak`;
  try {
    const missing = !fs.existsSync(creds) || fs.statSync(creds).size === 0;
    if (!missing) return;
    if (!fs.existsSync(backup)) return;
    const bs = fs.statSync(backup);
    if (bs.size > 0) {
      fs.copyFileSync(backup, creds);
      log('warn', 'restored creds.json from backup', { size: bs.size });
    }
  } catch {}
}

function backupCreds(dir: string): void {
  const creds = `${dir}/creds.json`;
  try {
    if (fs.statSync(creds).size > 0)
      fs.copyFileSync(creds, `${dir}/creds.json.bak`);
  } catch {}
}

async function makeSocket(): Promise<{
  s: WASocket;
  saveCreds: () => Promise<void>;
}> {
  fs.mkdirSync(authDir, { recursive: true });
  recoverCredsIfEmpty(authDir);
  const { state, saveCreds: rawSave } = await useMultiFileAuthState(authDir);
  const saveCreds = async () => {
    await rawSave();
    backupCreds(authDir);
  };
  const { version } = await fetchLatestWaWebVersion({}).catch(() => ({
    version: undefined,
  }));
  const s = makeWASocket({
    version,
    auth: {
      creds: state.creds,
      keys: makeCacheableSignalKeyStore(state.keys, logger),
    },
    printQRInTerminal: false,
    logger,
    browser: Browsers.macOS('Chrome'),
    shouldSyncHistoryMessage: () => false,
  });
  s.ev.on('creds.update', saveCreds);
  return { s, saveCreds };
}

// Runs one pairing attempt and resolves on 'open'. Handles Baileys' 515
// "restart required after pairing" by recursing WITHOUT re-requesting a new
// pair code (that confuses the server and invalidates the session).
async function pairOnce(phone?: string): Promise<void> {
  const { s, saveCreds } = await makeSocket();

  if (phone) {
    // Baileys needs a completed handshake before requestPairingCode works.
    setTimeout(async () => {
      try {
        const code = await s.requestPairingCode(phone);
        process.stdout.write(
          `\npairing code: ${code}\n\n` +
            `  1. open WhatsApp on your phone\n` +
            `  2. tap Settings > Linked Devices > Link a Device\n` +
            `  3. tap "Link with phone number instead"\n` +
            `  4. enter: ${code}\n\n`,
        );
      } catch (e) {
        process.stderr.write(`Failed to request pairing code: ${e}\n`);
        process.exit(1);
      }
    }, 3000);
  }

  await new Promise<void>((resolve, reject) => {
    s.ev.on('connection.update', (update) => {
      const { connection, lastDisconnect } = update;
      if (connection === 'open') {
        saveCreds()
          .then(() => {
            process.stdout.write('authenticated — credentials saved\n');
            s.end(undefined);
            resolve();
          })
          .catch(reject);
      }
      if (connection === 'close') {
        const code = (lastDisconnect?.error as any)?.output?.statusCode;
        if (code === 515) {
          process.stdout.write('reconnecting after pairing...\n');
          pairOnce().then(resolve).catch(reject);
          return;
        }
        reject(new Error(`connection closed: ${code}`));
      }
    });
  });
}

let sock: WASocket | null = null;
const routerURL = env('ROUTER_URL');
const channelSecret = env('CHANNEL_SECRET', '');
const listenAddr = env('LISTEN_ADDR', ':9002');
const listenURL = env('LISTEN_URL', 'http://whapd:9002');
const rc = new RouterClient(routerURL, channelSecret);
let reconnectAttempts = 0;
let connected = false;

const outboundQueue: { jid: string; text: string }[] = [];
let flushing = false;

async function flushOutboundQueue(): Promise<void> {
  if (flushing || outboundQueue.length === 0) return;
  flushing = true;
  log('info', 'flushing outbound queue', { count: outboundQueue.length });
  try {
    const { sent, failed } = await flushQueue(outboundQueue, (item) =>
      sock!.sendMessage(item.jid, { text: item.text }).then(() => {}),
    );
    log('info', 'outbound queue flushed', {
      sent: sent.length,
      failed: failed.length,
    });
    for (const f of failed) {
      log('error', 'outbound send failed', {
        jid: f.item.jid,
        err: String(f.err),
      });
    }
  } finally {
    flushing = false;
  }
}

async function extractContent(msg: WAMessage): Promise<{
  content: string;
  mediaBuffer?: Buffer;
  mediaMime?: string;
  mediaFilename?: string;
}> {
  const m = msg.message;
  if (!m) return { content: '' };

  const text = m.conversation || m.extendedTextMessage?.text;
  if (text) return { content: text };

  const img = m.imageMessage;
  const vid = m.videoMessage;
  const aud = m.audioMessage;
  const doc = m.documentMessage;
  const sticker = m.stickerMessage;
  if (!img && !vid && !aud && !doc && !sticker) return { content: '' };

  const caption = img?.caption || vid?.caption || doc?.caption || '';
  let description = '';
  if (img) description = '[Image]';
  else if (vid) description = '[Video]';
  else if (aud) description = aud.ptt ? '[Voice Note]' : '[Audio]';
  else if (doc)
    description = doc.fileName ? `[File: ${doc.fileName}]` : '[File]';
  else if (sticker) description = '[Sticker]';

  const content = caption || description;

  let mediaBuffer: Buffer | undefined;
  let mediaMime: string | undefined;
  let mediaFilename: string | undefined;
  try {
    mediaBuffer = (await downloadMediaMessage(
      msg,
      'buffer',
      {},
      { reuploadRequest: sock!.updateMediaMessage, logger },
    )) as Buffer;
    mediaMime =
      img?.mimetype ||
      vid?.mimetype ||
      aud?.mimetype ||
      doc?.mimetype ||
      undefined;
    mediaFilename = doc?.fileName || undefined;
  } catch (e) {
    log('error', 'media download failed', { err: String(e) });
  }

  return { content, mediaBuffer, mediaMime, mediaFilename };
}

async function connect(): Promise<void> {
  ({ s: sock } = await makeSocket());

  sock.ev.on('connection.update', (update) => {
    const { connection, lastDisconnect, qr } = update;

    if (qr) {
      log('info', 'scan QR code to authenticate');
      qrcode.generate(qr, { small: true });
    }

    if (connection === 'close') {
      connected = false;
      const code = (lastDisconnect?.error as any)?.output?.statusCode;
      // 405 = server-side session termination. Both cases require re-pairing.
      if (code === DisconnectReason.loggedOut || code === 405) {
        log('error', 'session invalidated, delete auth dir and re-pair', {
          code,
        });
        process.exit(1);
      }
      reconnectAttempts++;
      if (reconnectAttempts > 10) {
        log('error', 'max reconnect attempts reached, giving up');
        process.exit(1);
      }
      const delay = Math.min(3000 * Math.pow(2, reconnectAttempts - 1), 60000);
      log('info', 'disconnected, reconnecting', {
        code,
        attempt: reconnectAttempts,
        delay,
      });
      setTimeout(
        () =>
          connect().catch((e) => {
            log('error', 'reconnect failed', { err: String(e) });
            process.exit(1);
          }),
        delay,
      );
    }

    if (connection === 'open') {
      reconnectAttempts = 0;
      connected = true;
      log('info', 'connected to whatsapp');
      sock!.sendPresenceUpdate('unavailable').catch(() => {});

      flushOutboundQueue().catch((e) =>
        log('error', 'queue flush failed', { err: String(e) }),
      );
    }
  });

  sock.ev.on('messages.upsert', async ({ messages }) => {
    for (const msg of messages) {
      if (!msg.message) continue;
      const jid = msg.key.remoteJid;
      if (!jid || jid === 'status@broadcast') continue;
      if (msg.key.fromMe) continue;

      const chatJid = `whatsapp:${jid}`;
      const rawSender = msg.key.participant || jid;
      const senderName = msg.pushName || rawSender.split('@')[0];

      // Loop guard: skip our own echoes in group chats (matched by push name).
      if (assistantName && (msg.pushName || '').toLowerCase() === assistantName)
        continue;

      const { content, mediaBuffer, mediaMime, mediaFilename } =
        await extractContent(msg);
      if (!content && !mediaBuffer) continue;

      const replyMeta = extractReplyMeta(msg);

      sock!.readMessages([msg.key]).catch(() => {});

      try {
        await rc.sendMessage({
          id: msg.key.id || '',
          chat_jid: chatJid,
          sender: `whatsapp:${rawSender}`,
          sender_name: senderName,
          content,
          timestamp:
            Number(msg.messageTimestamp) || Math.floor(Date.now() / 1000),
          ...(mediaBuffer
            ? {
                attachment: mediaBuffer.toString('base64'),
                attachment_mime: mediaMime,
                attachment_name: mediaFilename,
              }
            : {}),
          ...(replyMeta
            ? {
                reply_to: replyMeta.replyTo,
                reply_to_text: replyMeta.replyToText,
                ...(replyMeta.replyToSender
                  ? { reply_to_sender: replyMeta.replyToSender }
                  : {}),
              }
            : {}),
        });
      } catch (e) {
        log('error', 'deliver failed', { jid: chatJid, err: String(e) });
      }
    }
  });
}

export function getSocket(): WASocket | null {
  return sock;
}

export function isConnected(): boolean {
  return connected;
}

export function queueOutbound(jid: string, text: string): void {
  outboundQueue.push({ jid, text });
  log('info', 'outbound queued (disconnected)', {
    jid,
    queue: outboundQueue.length,
  });
}

// WhatsApp 'composing' presence decays ~25s server-side, so long agent runs
// need periodic re-sends. 15s refresh gives safety margin; 10min hard cap
// matches the Go adapters via chanlib.TypingRefresher.
async function pushPresence(
  status: 'composing' | 'paused',
  jid: string,
): Promise<void> {
  const s = sock;
  if (!s || !connected) return;
  try {
    await s.sendPresenceUpdate(status, jid);
  } catch (e) {
    log('warn', 'presence update failed', { jid, status, err: String(e) });
  }
}

const typing = new TypingRefresher(
  15_000,
  10 * 60 * 1000,
  (jid) => pushPresence('composing', jid),
  (jid) => pushPresence('paused', jid),
);

export function setTyping(jid: string, on: boolean): void {
  typing.set(jid, on);
}

async function registerWithRetry(): Promise<void> {
  let attempt = 0;
  while (true) {
    try {
      await rc.register('whatsapp', listenURL);
      log('info', 'registered with router', { url: routerURL, attempt });
      return;
    } catch (e) {
      attempt++;
      const delay = Math.min(2000 * attempt, 30000);
      log('warn', 'router registration failed, retrying', {
        attempt,
        delay,
        err: String(e),
      });
      await new Promise((r) => setTimeout(r, delay));
    }
  }
}

async function main() {
  const pairIdx = process.argv.indexOf('--pair');
  if (pairIdx >= 0) {
    const phone = process.argv[pairIdx + 1];
    if (!phone) {
      process.stderr.write('Usage: node dist/main.js --pair <phone>\n');
      process.exit(1);
    }
    process.stdout.write(`pairing whatsapp with phone ${phone}...\n`);
    await pairOnce(phone);
    process.exit(0);
  }

  await connect();

  // Never exit on register failure: docker restart loops race Baileys'
  // non-atomic creds writes and corrupt the session. Stay up; gated catches up.
  //
  // Note: whapd does NOT expose `fetch_history`. Baileys has no reliable
  // history API — the in-memory sync store was removed in current versions
  // and LID/JID translation makes offline lookup unsafe. The gateway falls
  // back to its local-DB cache for history queries.
  registerWithRetry();

  const srv = startServer(
    listenAddr,
    channelSecret,
    getSocket,
    isConnected,
    queueOutbound,
    setTyping,
  );

  async function shutdown() {
    log('info', 'shutting down');
    typing.stop();
    await rc.deregister();
    sock?.end(undefined);
    srv.close();
    process.exit(0);
  }

  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

main().catch((e) => {
  log('error', 'fatal', { err: String(e) });
  process.exit(1);
});
