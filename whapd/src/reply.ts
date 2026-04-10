import type { WAMessage } from '@whiskeysockets/baileys';

export interface ReplyMeta {
  replyTo: string;
  replyToText: string;
  replyToSender?: string;
}

export function extractReplyMeta(msg: WAMessage): ReplyMeta | undefined {
  const m = msg.message;
  if (!m) return undefined;
  const ci =
    m.extendedTextMessage?.contextInfo ||
    m.imageMessage?.contextInfo ||
    m.videoMessage?.contextInfo ||
    m.audioMessage?.contextInfo ||
    m.documentMessage?.contextInfo ||
    m.stickerMessage?.contextInfo;
  if (!ci?.stanzaId) return undefined;

  const q = ci.quotedMessage;
  let replyToText = '';
  if (q) {
    replyToText =
      q.conversation ||
      q.extendedTextMessage?.text ||
      q.imageMessage?.caption ||
      q.videoMessage?.caption ||
      q.documentMessage?.caption ||
      '';
    if (!replyToText) {
      if (q.imageMessage) replyToText = '[Image]';
      else if (q.videoMessage) replyToText = '[Video]';
      else if (q.audioMessage) replyToText = '[Audio]';
      else if (q.documentMessage) replyToText = '[File]';
      else if (q.stickerMessage) replyToText = '[Sticker]';
    }
  }

  const out: ReplyMeta = { replyTo: ci.stanzaId, replyToText };
  if (ci.participant) out.replyToSender = `whatsapp:${ci.participant}`;
  return out;
}
