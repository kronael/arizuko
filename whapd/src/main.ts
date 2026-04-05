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
import { startServer } from './server.js';

const logger = pino({ level: 'warn' });

function log(level: string, msg: string, attrs?: Record<string, unknown>) {
  const entry = { time: new Date().toISOString(), level, msg, ...attrs };
  process.stderr.write(JSON.stringify(entry) + '\n');
}

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

const GROUP_SYNC_INTERVAL_MS = 24 * 60 * 60 * 1000;

interface OutboundMsg {
  jid: string;
  text: string;
}

// Baileys writes creds.json non-atomically; restore from .bak if a prior crash
// left the live file empty.
function recoverCredsIfEmpty(dir: string): void {
  const creds = `${dir}/creds.json`;
  const backup = `${dir}/creds.json.bak`;
  try {
    if (fs.statSync(creds).size === 0 && fs.existsSync(backup)) {
      const bs = fs.statSync(backup);
      if (bs.size > 0) {
        fs.copyFileSync(backup, creds);
        log('warn', 'restored creds.json from backup', { size: bs.size });
      }
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

async function pair(phone: string): Promise<void> {
  process.stdout.write(`pairing whatsapp with phone ${phone}...\n`);
  const { s, saveCreds } = await makeSocket();

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
        // 515 = restart required after pairing; one reconnect finalises the session.
        if (code === 515) {
          process.stdout.write('reconnecting after pairing...\n');
          pair(phone).then(resolve).catch(reject);
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

const lidToPhoneMap: Record<string, string> = {};
const groupNameCache: Record<string, string> = {};
const outboundQueue: OutboundMsg[] = [];
let flushing = false;

function seedLid(
  id: string | undefined | null,
  lid: string | undefined | null,
): void {
  if (!id || !lid) return;
  const lidUser = lid.split(':')[0].split('@')[0];
  const phoneJid = id.split(':')[0].split('@')[0] + '@s.whatsapp.net';
  lidToPhoneMap[lidUser] = phoneJid;
}

async function translateJid(jid: string): Promise<string> {
  if (!jid.endsWith('@lid')) return jid;
  const lidUser = jid.split('@')[0].split(':')[0];
  const cached = lidToPhoneMap[lidUser];
  if (cached) return cached;
  try {
    const repo = sock!.signalRepository as any;
    const pn = await repo?.lidMapping?.getPNForLID(jid);
    if (pn) {
      const phoneJid = `${pn.split('@')[0].split(':')[0]}@s.whatsapp.net`;
      lidToPhoneMap[lidUser] = phoneJid;
      log('info', 'translated lid jid', { lid: jid, phone: phoneJid });
      return phoneJid;
    }
  } catch (e) {
    log('debug', 'lid translate failed', { jid, err: String(e) });
  }
  return jid;
}

async function syncGroupMetadata(): Promise<void> {
  if (!sock) return;
  try {
    log('info', 'syncing group metadata');
    const groups = await sock.groupFetchAllParticipating();
    let n = 0;
    for (const [jid, meta] of Object.entries(groups)) {
      if (meta.subject) {
        groupNameCache[jid] = meta.subject;
        n++;
      }
    }
    log('info', 'group metadata synced', { count: n });
  } catch (e) {
    log('error', 'group sync failed', { err: String(e) });
  }
}

async function flushOutboundQueue(): Promise<void> {
  if (flushing || outboundQueue.length === 0) return;
  flushing = true;
  try {
    log('info', 'flushing outbound queue', { count: outboundQueue.length });
    while (outboundQueue.length > 0) {
      const item = outboundQueue.shift()!;
      await sock!.sendMessage(item.jid, { text: item.text });
    }
  } catch (e) {
    log('error', 'queue flush error', { err: String(e) });
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
      if (code === DisconnectReason.loggedOut) {
        log('error', 'logged out, delete auth dir and restart');
        process.exit(1);
      }
      // 405 = server-side session termination; retrying will never succeed.
      if (code === 405) {
        log(
          'error',
          'session invalidated by server (405), delete auth dir and re-pair',
        );
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

      if (sock!.user) seedLid(sock!.user.id, sock!.user.lid);

      try {
        const contacts = (sock as any).contacts as
          | Record<string, any>
          | undefined;
        if (contacts) {
          for (const [id, c] of Object.entries(contacts)) seedLid(id, c?.lid);
          log('info', 'seeded lid map from contacts', {
            count: Object.keys(lidToPhoneMap).length,
          });
        }
      } catch (e) {
        log('debug', 'contacts seed failed', { err: String(e) });
      }

      flushOutboundQueue().catch((e) =>
        log('error', 'queue flush failed', { err: String(e) }),
      );
      syncGroupMetadata().catch((e) =>
        log('error', 'initial group sync failed', { err: String(e) }),
      );
    }
  });

  sock.ev.on('contacts.upsert', (contacts) => {
    for (const c of contacts) seedLid(c.id, (c as any).lid);
  });

  sock.ev.on('messages.upsert', async ({ messages }) => {
    for (const msg of messages) {
      if (!msg.message) continue;
      const rawJid = msg.key.remoteJid;
      if (!rawJid || rawJid === 'status@broadcast') continue;
      if (msg.key.fromMe) continue;

      const jid = await translateJid(rawJid);
      const isGroup = jid.endsWith('@g.us');
      const chatJid = `whatsapp:${jid}`;
      const rawSender = msg.key.participant || jid;
      const senderName = msg.pushName || rawSender.split('@')[0];

      // Loop guard: skip our own echoes in group chats (matched by push name).
      if (assistantName && (msg.pushName || '').toLowerCase() === assistantName)
        continue;

      const { content, mediaBuffer, mediaMime, mediaFilename } =
        await extractContent(msg);
      if (!content && !mediaBuffer) continue;

      const groupName = isGroup ? (groupNameCache[jid] ?? '') : '';
      await rc
        .sendChat(chatJid, isGroup ? groupName : senderName, isGroup)
        .catch(() => {});
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
          is_group: isGroup,
          ...(mediaBuffer
            ? {
                attachment: mediaBuffer.toString('base64'),
                attachment_mime: mediaMime,
                attachment_name: mediaFilename,
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
    await pair(phone);
    process.exit(0);
  }

  await connect();

  // Never exit on register failure: docker restart loops race Baileys'
  // non-atomic creds writes and corrupt the session. Stay up; gated catches up.
  registerWithRetry();

  setInterval(() => {
    syncGroupMetadata().catch((e) =>
      log('error', 'periodic group sync failed', { err: String(e) }),
    );
  }, GROUP_SYNC_INTERVAL_MS);

  const srv = startServer(
    listenAddr,
    channelSecret,
    getSocket,
    isConnected,
    queueOutbound,
  );

  async function shutdown() {
    log('info', 'shutting down');
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
