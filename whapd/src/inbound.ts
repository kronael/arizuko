// Pure inbound-dispatch helpers, factored out of main.ts so they can be
// unit-tested without spinning up a Baileys socket. The runtime in
// main.ts is the only sink; tests are the second consumer.
//
// Two paths converge here:
//   - messages.upsert  -> buildMessagePayload  -> RouterClient.sendMessage
//   - messages.reaction-> buildReactionPayload -> RouterClient.sendMessage
// Both produce the same shape (RouterMessage), keeping the gateway's
// inbound contract identical for text/media and reactions.

import type { WAMessage } from '@whiskeysockets/baileys';
import { extractReplyMeta } from './reply.js';

// Mirror chanlib.ClassifyEmoji: only negatives are listed; everything
// else (including unknown emoji) defaults to "like".
const NEGATIVE_EMOJI = new Set(['👎', '💩', '😡', '🤬', '💔', '🤮', '😢']);

export function classifyEmoji(emoji: string): 'like' | 'dislike' {
  return NEGATIVE_EMOJI.has(emoji) ? 'dislike' : 'like';
}

export interface RouterMessage {
  id: string;
  chat_jid: string;
  sender: string;
  sender_name: string;
  content: string;
  timestamp: number;
  is_group: boolean;
  verb?: string;
  reaction?: string;
  reply_to?: string;
  reply_to_text?: string;
  reply_to_sender?: string;
  attachment?: string;
  attachment_mime?: string;
  attachment_name?: string;
}

export interface ExtractedContent {
  content: string;
  mediaMime?: string;
  mediaFilename?: string;
  isVoiceNote: boolean;
}

// Build the textual representation + media metadata from a raw WAMessage.
// Media bytes are NOT downloaded here; the caller provides them via
// buildMessagePayload's mediaBuffer arg, because downloads need the
// live socket. This split keeps the function pure & testable.
export function extractContent(msg: WAMessage): ExtractedContent {
  const m = msg.message;
  if (!m) return { content: '', isVoiceNote: false };

  const text = m.conversation || m.extendedTextMessage?.text;
  if (text) return { content: text, isVoiceNote: false };

  const img = m.imageMessage;
  const vid = m.videoMessage;
  const aud = m.audioMessage;
  const doc = m.documentMessage;
  const sticker = m.stickerMessage;
  if (!img && !vid && !aud && !doc && !sticker)
    return { content: '', isVoiceNote: false };

  const caption = img?.caption || vid?.caption || doc?.caption || '';
  let description = '';
  if (img) description = '[Image]';
  else if (vid) description = '[Video]';
  else if (aud) description = aud.ptt ? '[Voice Note]' : '[Audio]';
  else if (doc)
    description = doc.fileName ? `[File: ${doc.fileName}]` : '[File]';
  else if (sticker) description = '[Sticker]';

  return {
    content: caption || description,
    mediaMime:
      img?.mimetype ||
      vid?.mimetype ||
      aud?.mimetype ||
      doc?.mimetype ||
      undefined,
    mediaFilename: doc?.fileName || undefined,
    isVoiceNote: !!aud?.ptt,
  };
}

// True when a message's pushName matches the assistant's own name —
// used as a loop guard for group chats where the bot's own echo can
// otherwise round-trip back through the upsert handler.
export function isOwnEcho(msg: WAMessage, assistantName: string): boolean {
  if (!assistantName) return false;
  const name = (msg.pushName || '').toLowerCase();
  return name === assistantName.toLowerCase();
}

// True when this is a group JID (suffix @g.us). Broadcast lists and
// 1:1 chats land on the false branch.
export function isGroupJid(jid: string): boolean {
  return jid.endsWith('@g.us');
}

// Build the RouterMessage for a regular (non-reaction) inbound message.
// mediaBuffer is passed in from the caller (main.ts) so the pure
// function stays free of Baileys socket plumbing.
export function buildMessagePayload(
  msg: WAMessage,
  extracted: ExtractedContent,
  mediaBuffer: Buffer | null,
  nowSec: () => number,
): RouterMessage | null {
  const jid = msg.key.remoteJid;
  if (!jid || jid === 'status@broadcast') return null;
  if (msg.key.fromMe) return null;
  if (!extracted.content && !mediaBuffer) return null;

  const rawSender = msg.key.participant || jid;
  const senderName = msg.pushName || rawSender.split('@')[0];
  const ts = Number(msg.messageTimestamp) || nowSec();

  const payload: RouterMessage = {
    id: msg.key.id || '',
    chat_jid: `whatsapp:${jid}`,
    sender: `whatsapp:${rawSender}`,
    sender_name: senderName,
    content: extracted.content,
    timestamp: ts,
    is_group: isGroupJid(jid),
  };

  if (mediaBuffer) {
    payload.attachment = mediaBuffer.toString('base64');
    if (extracted.mediaMime) payload.attachment_mime = extracted.mediaMime;
    if (extracted.mediaFilename)
      payload.attachment_name = extracted.mediaFilename;
  }

  const replyMeta = extractReplyMeta(msg);
  if (replyMeta) {
    payload.reply_to = replyMeta.replyTo;
    payload.reply_to_text = replyMeta.replyToText;
    if (replyMeta.replyToSender)
      payload.reply_to_sender = replyMeta.replyToSender;
  }

  return payload;
}

export interface ReactionEvent {
  key: { remoteJid?: string | null; id?: string | null; participant?: string };
  reaction?: {
    text?: string | null;
    key?: { participant?: string };
    senderTimestampMs?: number | bigint;
  };
}

// Build the RouterMessage for a reaction. `verb` is set from
// classifyEmoji; the emoji itself is mirrored into the reaction field
// so the agent can render fidelity beyond the like/dislike split.
export function buildReactionPayload(
  ev: ReactionEvent,
  nowSec: () => number,
): RouterMessage | null {
  const text = ev.reaction?.text;
  if (!text) return null;
  const jid = ev.key.remoteJid;
  if (!jid || jid === 'status@broadcast') return null;
  const targetId = ev.key.id || '';
  const senderJid = ev.reaction?.key?.participant || ev.key.participant || jid;
  const tsMs = Number(ev.reaction?.senderTimestampMs ?? 0);
  const ts = tsMs > 0 ? Math.floor(tsMs / 1000) : nowSec();

  return {
    id: `${targetId}:r:${text}`,
    chat_jid: `whatsapp:${jid}`,
    sender: `whatsapp:${senderJid}`,
    sender_name: senderJid.split('@')[0],
    content: text,
    timestamp: ts,
    verb: classifyEmoji(text),
    reaction: text,
    reply_to: targetId,
    is_group: isGroupJid(jid),
  };
}
