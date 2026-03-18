import fs from 'node:fs';
import makeWASocket, {
  Browsers,
  DisconnectReason,
  makeCacheableSignalKeyStore,
  useMultiFileAuthState,
  type WASocket,
} from '@whiskeysockets/baileys';
import pino from 'pino';
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

const routerURL = env('ROUTER_URL');
const channelSecret = env('CHANNEL_SECRET', '');
const listenAddr = env('LISTEN_ADDR', ':9002');
const listenURL = env('LISTEN_URL', 'http://whapd:9002');
const dataDir = env('DATA_DIR', '');
const authDir = env(
  'WHATSAPP_AUTH_DIR',
  dataDir ? `${dataDir}/store/whatsapp-auth` : '/srv/data/store/whatsapp-auth',
);

let sock: WASocket | null = null;
const rc = new RouterClient(routerURL, channelSecret);

async function connect(): Promise<void> {
  fs.mkdirSync(authDir, { recursive: true });
  const { state, saveCreds } = await useMultiFileAuthState(authDir);

  sock = makeWASocket({
    auth: {
      creds: state.creds,
      keys: makeCacheableSignalKeyStore(state.keys, logger),
    },
    printQRInTerminal: true,
    logger,
    browser: Browsers.macOS('Chrome'),
  });

  sock.ev.on('creds.update', saveCreds);

  sock.ev.on('connection.update', (update) => {
    const { connection, lastDisconnect, qr } = update;

    if (qr) {
      log('info', 'scan QR code in terminal to authenticate');
    }

    if (connection === 'close') {
      const code = (lastDisconnect?.error as any)?.output?.statusCode;
      if (code === DisconnectReason.loggedOut) {
        log('error', 'logged out, delete auth dir and restart');
        process.exit(1);
      }
      log('info', 'disconnected, reconnecting', { code });
      setTimeout(
        () =>
          connect().catch((e) => {
            log('error', 'reconnect failed', { err: String(e) });
            process.exit(1);
          }),
        3000,
      );
    }

    if (connection === 'open') {
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

      rc.sendChat(chatJid, isGroup ? '' : senderName, isGroup);

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
        });
      } catch (e) {
        log('error', 'deliver failed', { jid: chatJid, err: String(e) });
      }
    }
  });
}

async function main() {
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
