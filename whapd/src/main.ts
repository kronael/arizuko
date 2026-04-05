import fs from 'node:fs';
import makeWASocket, {
  Browsers,
  DisconnectReason,
  downloadMediaMessage,
  fetchLatestWaWebVersion,
  getContentType,
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

// Describes an outbound text message queued while disconnected
interface OutboundMsg {
  jid: string;
  text: string;
}

// Guard against Baileys' non-atomic creds.json writes: if the file is empty
// (truncated by a prior crash/restart mid-write), restore from backup.
function recoverCredsIfEmpty(dir: string): void {
  const creds = `${dir}/creds.json`;
  const backup = `${dir}/creds.json.bak`;
  try {
    const s = fs.statSync(creds);
    if (s.size === 0 && fs.existsSync(backup)) {
      const bs = fs.statSync(backup);
      if (bs.size > 0) {
        fs.copyFileSync(backup, creds);
        log('warn', 'restored creds.json from backup', { size: bs.size });
      }
    }
  } catch {
    // creds.json missing — fresh auth, nothing to recover
  }
}

function backupCreds(dir: string): void {
  const creds = `${dir}/creds.json`;
  const backup = `${dir}/creds.json.bak`;
  try {
    const s = fs.statSync(creds);
    if (s.size > 0) fs.copyFileSync(creds, backup);
  } catch {
    // ignore
  }
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

// --pair <phone>: request pairing code, print it, exit once authenticated.
async function pair(phone: string): Promise<void> {
  process.stdout.write(`pairing whatsapp with phone ${phone}...\n`);
  const { s, saveCreds } = await makeSocket();

  // Request pairing code 3s after socket is ready (Baileys needs handshake first)
  setTimeout(async () => {
    try {
      const code = await s.requestPairingCode(phone);
      process.stdout.write(`\npairing code: ${code}\n\n`);
      process.stdout.write(`  1. open WhatsApp on your phone\n`);
      process.stdout.write(
        `  2. tap Settings > Linked Devices > Link a Device\n`,
      );
      process.stdout.write(`  3. tap "Link with phone number instead"\n`);
      process.stdout.write(`  4. enter: ${code}\n\n`);
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
        // 515 = restart required after pairing — reconnect once
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

// Normal daemon mode
let sock: WASocket | null = null;
const routerURL = env('ROUTER_URL');
const channelSecret = env('CHANNEL_SECRET', '');
const listenAddr = env('LISTEN_ADDR', ':9002');
const listenURL = env('LISTEN_URL', 'http://whapd:9002');
const rc = new RouterClient(routerURL, channelSecret);
let reconnectAttempts = 0;
let connected = false;

// LID→phone JID translation cache
const lidToPhoneMap: Record<string, string> = {};

// In-memory group name cache (populated by syncGroupMetadata)
const groupNameCache: Record<string, string> = {};

// Outbound message queue for reconnect delivery
const outboundQueue: OutboundMsg[] = [];
let flushing = false;
let groupSyncTimerStarted = false;

async function translateJid(jid: string): Promise<string> {
  if (!jid.endsWith('@lid')) return jid;
  const lidUser = jid.split('@')[0].split(':')[0];
  const cached = lidToPhoneMap[lidUser];
  if (cached) return cached;

  try {
    // lidMapping is a runtime property not in the type definitions
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
    let count = 0;
    for (const [jid, meta] of Object.entries(groups)) {
      if (meta.subject) {
        groupNameCache[jid] = meta.subject;
        count++;
      }
    }
    log('info', 'group metadata synced', { count });
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

// Extract text content and a media description from an inbound message.
// Returns { content, mediaBuffer, mediaMime, mediaFilename }.
async function extractContent(msg: WAMessage): Promise<{
  content: string;
  mediaBuffer?: Buffer;
  mediaMime?: string;
  mediaFilename?: string;
}> {
  const m = msg.message;
  if (!m) return { content: '' };

  const type = getContentType(m);

  // Plain text paths
  const text = m.conversation || m.extendedTextMessage?.text;
  if (text) return { content: text };

  // Media messages
  const mediaTypes = [
    'imageMessage',
    'videoMessage',
    'audioMessage',
    'documentMessage',
    'stickerMessage',
  ] as const;

  const isMedia = type && (mediaTypes as readonly string[]).includes(type);
  if (!isMedia) return { content: '' };

  const imgMsg = m.imageMessage;
  const vidMsg = m.videoMessage;
  const audMsg = m.audioMessage;
  const docMsg = m.documentMessage;

  const caption = imgMsg?.caption || vidMsg?.caption || docMsg?.caption || '';

  // Determine a human-readable description for media with no caption
  let description = '';
  if (type === 'imageMessage') description = '[Image]';
  else if (type === 'videoMessage') description = '[Video]';
  else if (type === 'audioMessage') {
    description = audMsg?.ptt ? '[Voice Note]' : '[Audio]';
  } else if (type === 'documentMessage') {
    description = docMsg?.fileName ? `[File: ${docMsg.fileName}]` : '[File]';
  } else if (type === 'stickerMessage') description = '[Sticker]';

  const content = caption || description;

  // Download media buffer
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
      imgMsg?.mimetype ||
      vidMsg?.mimetype ||
      audMsg?.mimetype ||
      docMsg?.mimetype ||
      undefined;

    mediaFilename = docMsg?.fileName || undefined;
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
      // 405: session terminated server-side — auth is invalid, no point retrying.
      if (code === 405) {
        log(
          'error',
          'session invalidated by server (405), delete auth dir and re-pair',
        );
        process.exit(1);
      }
      reconnectAttempts++;
      const maxAttempts = 10;
      if (reconnectAttempts > maxAttempts) {
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

      // Populate LID→phone map from our own user identity
      if (sock!.user) {
        const phoneUser = sock!.user.id.split(':')[0];
        const lidUser = sock!.user.lid?.split(':')[0];
        if (lidUser && phoneUser) {
          lidToPhoneMap[lidUser] = `${phoneUser}@s.whatsapp.net`;
        }
      }

      // Seed LID map from contacts store (available after connection)
      try {
        const contacts = (sock as any).contacts as Record<string, any>;
        if (contacts) {
          for (const [id, c] of Object.entries(contacts)) {
            const lid = c?.lid as string | undefined;
            if (lid) {
              const lidUser = lid.split(':')[0].split('@')[0];
              const phoneJid =
                id.split(':')[0].split('@')[0] + '@s.whatsapp.net';
              lidToPhoneMap[lidUser] = phoneJid;
            }
          }
          log('info', 'seeded lid map from contacts', {
            count: Object.keys(lidToPhoneMap).length,
          });
        }
      } catch (e) {
        log('debug', 'contacts seed failed', { err: String(e) });
      }

      // Flush queued outbound messages
      flushOutboundQueue().catch((e) =>
        log('error', 'queue flush failed', { err: String(e) }),
      );

      // Sync group metadata on startup and then daily
      syncGroupMetadata().catch((e) =>
        log('error', 'initial group sync failed', { err: String(e) }),
      );
      if (!groupSyncTimerStarted) {
        groupSyncTimerStarted = true;
        setInterval(() => {
          syncGroupMetadata().catch((e) =>
            log('error', 'periodic group sync failed', { err: String(e) }),
          );
        }, GROUP_SYNC_INTERVAL_MS);
      }
    }
  });

  // Build LID→phone map from contact updates
  sock.ev.on('contacts.upsert', (contacts) => {
    for (const c of contacts) {
      const lid = (c as any).lid as string | undefined;
      if (lid && c.id) {
        const lidUser = lid.split(':')[0].split('@')[0];
        const phoneJid = c.id.split(':')[0].split('@')[0] + '@s.whatsapp.net';
        lidToPhoneMap[lidUser] = phoneJid;
      }
    }
  });

  sock.ev.on('messages.upsert', async ({ messages }) => {
    for (const msg of messages) {
      if (!msg.message) continue;
      const rawJid = msg.key.remoteJid;
      if (!rawJid || rawJid === 'status@broadcast') continue;
      if (msg.key.fromMe) continue;

      // Translate LID JID to phone-based JID for modern WA accounts
      const jid = await translateJid(rawJid);

      const isGroup = jid.endsWith('@g.us');
      const chatJid = `whatsapp:${jid}`;
      const rawSender = msg.key.participant || jid;
      const senderName = msg.pushName || rawSender.split('@')[0];

      // Skip messages from the bot itself (loop guard for group chats)
      if (assistantName && (msg.pushName || '').toLowerCase() === assistantName)
        continue;

      const { content, mediaBuffer, mediaMime, mediaFilename } =
        await extractContent(msg);

      // Skip pure protocol messages with nothing to deliver
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
          topic: '',
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
  // --pair <phone>: pair mode — print code and exit
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

  // Retry registration with backoff. Do NOT exit on failure — exiting causes
  // docker compose restart cycles that race with Baileys' non-atomic creds
  // writes and corrupt the session. Stay up; gated will eventually come up.
  registerWithRetry();

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
