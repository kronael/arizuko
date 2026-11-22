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
import {
  ServiceTokenSource,
  buildVerifier,
  verifyRoutd,
  type RoutdVerifier,
} from './auth.js';
import { WhapdBot, authDirHasCreds, type SocketBuilder } from './bot.js';
import { RouterClient } from './client.js';
import { DedupLedger } from './dedup.js';
import { InboundLedger } from './retain.js';
import {
  buildMessagePayload,
  buildReactionPayload,
  extractContent as extractContentPure,
  isOwnEcho,
} from './inbound.js';
import type { RouterMessage } from './inbound.js';
import { log } from './log.js';
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
// Outbound dedup is a delivery guarantee, not a nicety: without the ledger
// routd's re-send of a still-pending row posts a second WhatsApp message
// (BUGS L32). The directory must be MOUNTED — an unmounted one dies with the
// container and every claim is forgotten, which is the same as having none.
const stateDir = env(
  'WHAPD_STATE_DIR',
  dataDir ? `${dataDir}/store/whapd` : '/srv/data/store/whapd',
);
const ledger = new DedupLedger(`${stateDir}/outbound-dedup`);
// Inbound retention is the other half of the same guarantee: the read receipt
// tells WhatsApp never to offer the message again, so it may only be sent once
// the message survives a routd restart (BUGS L39).
const inbound = new InboundLedger(`${stateDir}/inbound-pending`);
const INBOUND_SWEEP_MS = 30000;
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
async function pairOnce(phone?: string): Promise<void> {
  const { s, saveCreds } = await makeSocket();
  if (phone) {
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
const pairSocketBuilder: SocketBuilder = async (phone) => {
  const { s, saveCreds } = await makeSocket();
  let codeResolve: (v: string) => void;
  let codeReject: (e: unknown) => void;
  const codePromise = new Promise<string>((res, rej) => {
    codeResolve = res;
    codeReject = rej;
  });
  let openResolve: () => void;
  let openReject: (e: unknown) => void;
  const openPromise = new Promise<void>((res, rej) => {
    openResolve = res;
    openReject = rej;
  });
  setTimeout(async () => {
    try {
      const code = await s.requestPairingCode(phone);
      codeResolve(code);
    } catch (e) {
      codeReject(e);
    }
  }, 3000);
  s.ev.on('connection.update', (update) => {
    const { connection, lastDisconnect } = update;
    if (connection === 'open') {
      saveCreds()
        .then(() => openResolve())
        .catch(openReject);
    }
    if (connection === 'close') {
      const code = (lastDisconnect?.error as any)?.output?.statusCode;
      if (code === 515) return;
      openReject(new Error(`connection closed: ${code}`));
    }
  });
  return { sock: s, codePromise, openPromise };
};
const bot = new WhapdBot(pairSocketBuilder, () => authDirHasCreds(authDir));
const routerURL = env('ROUTER_URL');
const listenAddr = env('LISTEN_ADDR', ':8080');
const listenURL = env('LISTEN_URL', 'http://whapd:8080');
const authdURL = process.env['AUTHD_URL'];
const verifier: RoutdVerifier = buildVerifier();
let bearer: () => Promise<string> = async () => '';
if (authdURL) {
  const svcName = process.env['AUTHD_SERVICE_NAME'] || 'whapd';
  const svcKey = env('AUTHD_SERVICE_KEY');
  const src = new ServiceTokenSource(authdURL, svcName, svcKey);
  bearer = () => src.token();
  log('info', 'service-token auth enabled', {
    daemon: svcName,
    authd: authdURL,
  });
}
const rc = new RouterClient(routerURL, bearer);
let reconnectAttempts = 0;
let connected = false;
let lastInboundAt = Math.floor(Date.now() / 1000);

async function sendInbound(payload: RouterMessage) {
  await rc.sendMessage(payload);
  lastInboundAt = Math.floor(Date.now() / 1000);
}
async function extractWithMedia(msg: WAMessage): Promise<{
  extracted: ReturnType<typeof extractContentPure>;
  mediaBuffer: Buffer | null;
}> {
  const extracted = extractContentPure(msg);
  const m = msg.message;
  const hasMedia = !!(
    m?.imageMessage ||
    m?.videoMessage ||
    m?.audioMessage ||
    m?.documentMessage ||
    m?.stickerMessage
  );
  if (!hasMedia) return { extracted, mediaBuffer: null };
  try {
    const buf = (await downloadMediaMessage(
      msg,
      'buffer',
      {},
      { reuploadRequest: sock!.updateMediaMessage, logger },
    )) as Buffer;
    return { extracted, mediaBuffer: buf };
  } catch (e) {
    log('error', 'media download failed', { err: String(e) });
    return { extracted, mediaBuffer: null };
  }
}
async function connect(): Promise<void> {
  if (bot.suspended) {
    log('info', 'connect: bot suspended for pair flow; skipping reconnect');
    return;
  }
  ({ s: sock } = await makeSocket());
  bot.sock = sock;
  sock.ev.on('connection.update', (update) => {
    const { connection, lastDisconnect, qr } = update;
    if (qr) {
      log('info', 'scan QR code to authenticate');
      qrcode.generate(qr, { small: true });
    }
    if (connection === 'close') {
      connected = false;
      const code = (lastDisconnect?.error as any)?.output?.statusCode;
      // 405 / loggedOut = server-side session termination. Re-pair required.
      //
      // STAY UP. A server-side logout is not a crash and it is not transient, so
      // there is nothing to retry; and the pairing endpoints an operator
      // re-pairs through live in THIS process, so exiting takes away the one
      // surface that fixes it. krons restarted 2,820 times on this branch, and
      // routd could not even resolve the name to say why (BUGS L303).
      //
      // Suspended stops the reconnect loop; /health answers 503 disconnected and
      // /v1/pair/status answers `unauthenticated`, which is where an operator
      // looks.
      if (code === DisconnectReason.loggedOut || code === 405) {
        log(
          'error',
          'session invalidated; staying up unpaired — re-pair through /v1/pair',
          {
            code,
          },
        );
        bot.markUnauthenticated();
        bot.suspended = true;
        return;
      }
      if (bot.suspended) {
        log('info', 'reconnect skipped: pair flow active');
        return;
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
      bot.markIdle();
      log('info', 'connected to whatsapp');
      sock!.sendPresenceUpdate('unavailable').catch(() => {});
    }
  });
  sock.ev.on('messages.reaction', async (events) => {
    for (const ev of events) {
      const payload = buildReactionPayload(ev as any, () =>
        Math.floor(Date.now() / 1000),
      );
      if (!payload) continue;
      try {
        await rc.sendMessage(payload);
        lastInboundAt = Math.floor(Date.now() / 1000);
      } catch (e) {
        log('error', 'deliver reaction failed', {
          jid: payload.chat_jid,
          err: String(e),
        });
      }
    }
  });
  sock.ev.on('messages.upsert', async ({ messages }) => {
    for (const msg of messages) {
      if (!msg.message) continue;
      const jid = msg.key.remoteJid;
      if (!jid || jid === 'status@broadcast') continue;
      if (msg.key.fromMe) continue;
      if (isOwnEcho(msg, assistantName)) continue;
      const { extracted, mediaBuffer } = await extractWithMedia(msg);
      const payload = buildMessagePayload(msg, extracted, mediaBuffer, () =>
        Math.floor(Date.now() / 1000),
      );
      if (!payload) continue;
      // Retain BEFORE the read receipt. readMessages tells WhatsApp the
      // message is handled and Baileys never offers it again, so sending it
      // first meant a routd that was restarting lost the message outright
      // (BUGS L39). A retain that fails withholds the receipt instead.
      const held = inbound.retain(payload);
      if (held) sock!.readMessages([msg.key]).catch(() => {});
      try {
        if (held) await inbound.sweep(sendInbound);
        else await inbound.deliver(payload, sendInbound);
      } catch (e) {
        log(
          'error',
          held
            ? 'deliver failed; the sweep holds the message'
            : 'deliver failed and NOTHING holds the message; it is lost',
          {
            jid: payload.chat_jid,
            id: payload.id,
            retained: held,
            err: String(e),
          },
        );
      }
    }
  });
}
export function getSocket(): WASocket | null {
  return bot.sock ?? sock;
}
export function isConnected(): boolean {
  return connected;
}
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
  15000,
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
  registerWithRetry();
  const stopInbound = inbound.start(sendInbound, INBOUND_SWEEP_MS);
  const srv = startServer(
    listenAddr,
    verifier,
    getSocket,
    isConnected,
    setTyping,
    () => lastInboundAt,
    bot,
    ledger,
  );
  async function shutdown() {
    log('info', 'shutting down');
    stopInbound();
    typing.stop();
    try {
      await rc.deregister();
    } catch (e) {
      log('error', 'deregister failed', { err: String(e) });
    }
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
