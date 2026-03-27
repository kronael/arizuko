import fs from 'node:fs';
import makeWASocket, {
  Browsers,
  DisconnectReason,
  fetchLatestWaWebVersion,
  makeCacheableSignalKeyStore,
  useMultiFileAuthState,
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

const dataDir = process.env['DATA_DIR'] ?? '';
const authDir = env(
  'WHATSAPP_AUTH_DIR',
  dataDir ? `${dataDir}/store/whatsapp-auth` : '/srv/data/store/whatsapp-auth',
);

async function makeSocket(): Promise<{
  s: WASocket;
  saveCreds: () => Promise<void>;
}> {
  fs.mkdirSync(authDir, { recursive: true });
  const { state, saveCreds } = await useMultiFileAuthState(authDir);
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

async function connect(): Promise<void> {
  ({ s: sock } = await makeSocket());

  sock.ev.on('connection.update', (update) => {
    const { connection, lastDisconnect, qr } = update;

    if (qr) {
      log('info', 'scan QR code to authenticate');
      qrcode.generate(qr, { small: true });
    }

    if (connection === 'close') {
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
      log('info', 'connected to whatsapp');
      sock!.sendPresenceUpdate('unavailable').catch(() => {});
    }
  });

  sock.ev.on('messages.upsert', async ({ messages }) => {
    for (const msg of messages) {
      if (!msg.message) continue;
      const jid = msg.key.remoteJid;
      if (!jid || jid === 'status@broadcast') continue;
      if (msg.key.fromMe) continue;

      const m = msg.message;
      const content =
        m.conversation ||
        m.extendedTextMessage?.text ||
        m.imageMessage?.caption ||
        m.videoMessage?.caption ||
        '';
      if (!content) continue;

      const isGroup = jid.endsWith('@g.us');
      const chatJid = `whatsapp:${jid}`;
      const rawSender = msg.key.participant || jid;
      const senderName = msg.pushName || rawSender.split('@')[0];

      await rc
        .sendChat(chatJid, isGroup ? '' : senderName, isGroup)
        .catch(() => {});

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
          topic: '', // WhatsApp has no native threading support
        });
      } catch (e) {
        log('error', 'deliver failed', { jid: chatJid, err: String(e) });
      }
    }
  });
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

  try {
    await rc.register('whatsapp', listenURL);
    log('info', 'registered with router', { url: routerURL });
  } catch (e) {
    log('error', 'router registration failed', { err: String(e) });
    process.exit(1);
  }

  const srv = startServer(listenAddr, channelSecret, () => sock);

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
