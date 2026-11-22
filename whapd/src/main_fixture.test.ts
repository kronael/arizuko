import { mock } from 'bun:test';
import { EventEmitter } from 'node:events';

if (process.env['WHAPD_FIXTURE']) {
  const ev = new EventEmitter();
  mock.module('@whiskeysockets/baileys', () => ({
    default: () => ({ ev, readMessages: async () => {}, end: () => {} }),
    Browsers: { macOS: () => [] },
    DisconnectReason: { loggedOut: 401 },
    downloadMediaMessage: async () => Buffer.alloc(0),
    fetchLatestWaWebVersion: async () => ({}),
    makeCacheableSignalKeyStore: (keys: unknown) => keys,
    useMultiFileAuthState: async () => ({
      state: { creds: {}, keys: {} },
      saveCreds: async () => {},
    }),
  }));
  ev.on('newListener', (event) => {
    if (event === 'messages.upsert')
      queueMicrotask(() =>
        ev.emit('messages.upsert', {
          messages: [
            {
              key: {
                remoteJid: 'human@s.whatsapp.net',
                id: 'human-1',
                fromMe: false,
              },
              pushName: 'Hermes',
              message: { conversation: 'hello' },
            },
          ],
        }),
      );
  });
  await import('./main.js');
}
