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
import {
  buildMessagePayload,
  buildReactionPayload,
  extractContent as extractContentPure,
  isOwnEcho,
} from './inbound.js';
import { log } from './log.js';
import { flushQueue } from './queue.js';
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

// Live socket handle. Kept as a module-level binding so existing helpers
// (queue flush, presence, message handlers) read the current connection
// without threading the bot through every callsite. `bot.sock` is the
// authoritative source; we mirror writes here.
let sock: WASocket | null = null;

// pairSocketBuilder: feeds WhapdBot.requestPair. Builds a fresh socket,
// kicks requestPairingCode after the handshake, and exposes both a
// code-resolved promise and an open-resolved promise so the bot can
// transition states distinctly (requesting -> pending -> idle).
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
      if (code === 515) return; // Baileys restarts after pairing; ignore.
      openReject(new Error(`connection closed: ${code}`));
    }
  });
  return { sock: s, codePromise, openPromise };
};

const bot = new WhapdBot(pairSocketBuilder, () => authDirHasCreds(authDir));

const routerURL = env('ROUTER_URL');
const listenAddr = env('LISTEN_ADDR', ':9002');
const listenURL = env('LISTEN_URL', 'http://whapd:9002');

// Split (spec 5/1) routd↔adapter auth — ES256 service tokens, no CHANNEL_SECRET
// (HMAC retire step 6). Outbound: exchange AUTHD_SERVICE_KEY for a
// service:<daemon> JWT (principal = AUTHD_SERVICE_NAME, the base daemon name,
// NOT the channel name) and present it on every routd call. Inbound: verifyRoutd
// pins service:routd against authd's JWKS. AUTHD_URL unset → local dev: bearer
// '' + verifier null (gate open), mirroring Go chanlib (run.go exits if a key is
// missing only when AUTHD_URL is set; chanlib.Auth opens the gate at ks==nil).
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
// Unix seconds of the most recent successful inbound delivery to the
// router. Initialized so /health doesn't flip stale before the first
// message. Updated only after rc.sendMessage resolves.
let lastInboundAt = Math.floor(Date.now() / 1000);

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

// Wraps the pure extractContent + Baileys media download, since downloads
// need a live socket. Pure logic lives in inbound.ts; this function is
// the only place the socket leaks in.
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
      // 405 / loggedOut = server-side session termination. Re-pair required;
      // mark the bot so /v1/pair/status surfaces it, then exit so the
      // container restart can pick up the new auth dir post-rebind.
      if (code === DisconnectReason.loggedOut || code === 405) {
        log('error', 'session invalidated, delete auth dir and re-pair', {
          code,
        });
        bot.markUnauthenticated();
        process.exit(1);
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

      flushOutboundQueue().catch((e) =>
        log('error', 'queue flush failed', { err: String(e) }),
      );
    }
  });

  // Inbound emoji reactions: classify to like/dislike, propagate so
  // the gateway can route the engagement signal. Reaction removal
  // (text falsey) is dropped — we only signal additions.
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
      // Loop guard: skip our own echoes in group chats (matched by push name).
      if (isOwnEcho(msg, assistantName)) continue;

      const { extracted, mediaBuffer } = await extractWithMedia(msg);
      const payload = buildMessagePayload(msg, extracted, mediaBuffer, () =>
        Math.floor(Date.now() / 1000),
      );
      if (!payload) continue;

      sock!.readMessages([msg.key]).catch(() => {});

      try {
        await rc.sendMessage(payload);
        lastInboundAt = Math.floor(Date.now() / 1000);
      } catch (e) {
        log('error', 'deliver failed', {
          jid: payload.chat_jid,
          err: String(e),
        });
      }
    }
  });
}

export function getSocket(): WASocket | null {
  // bot.sock is the post-pair-swap authority; fall back to the module
  // binding for boot-time callers that fire before the first connect().
  return bot.sock ?? sock;
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
    verifier,
    getSocket,
    isConnected,
    queueOutbound,
    setTyping,
    () => lastInboundAt,
    bot,
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
