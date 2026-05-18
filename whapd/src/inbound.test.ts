import { describe, expect, it } from 'bun:test';
import type { WAMessage } from '@whiskeysockets/baileys';
import {
  buildMessagePayload,
  buildReactionPayload,
  classifyEmoji,
  extractContent,
  isGroupJid,
  isOwnEcho,
  type ReactionEvent,
} from './inbound';

// Helper: build a WAMessage-shaped object with sensible defaults.
function wa(opts: {
  remoteJid?: string;
  id?: string;
  participant?: string;
  fromMe?: boolean;
  pushName?: string;
  messageTimestamp?: number;
  message?: Record<string, unknown> | null;
}): WAMessage {
  return {
    key: {
      remoteJid: opts.remoteJid ?? '12345@s.whatsapp.net',
      id: opts.id ?? 'MSG1',
      fromMe: opts.fromMe ?? false,
      ...(opts.participant ? { participant: opts.participant } : {}),
    },
    pushName: opts.pushName ?? 'Alice',
    messageTimestamp: opts.messageTimestamp,
    message:
      opts.message === undefined ? { conversation: 'hello' } : opts.message,
  } as unknown as WAMessage;
}

const FIXED_NOW = 1700_000_000;
const nowSec = () => FIXED_NOW;

// ---------- classifyEmoji ----------

describe('classifyEmoji', () => {
  it.each([
    ['👎', 'dislike'],
    ['💩', 'dislike'],
    ['😡', 'dislike'],
    ['🤬', 'dislike'],
    ['💔', 'dislike'],
    ['🤮', 'dislike'],
    ['😢', 'dislike'],
    ['👍', 'like'],
    ['❤️', 'like'],
    ['🎉', 'like'],
    ['🔥', 'like'],
    ['?', 'like'], // unknown emoji defaults to like
    ['', 'like'],
  ])('classifies %s as %s', (emoji, want) => {
    expect(classifyEmoji(emoji)).toBe(want as 'like' | 'dislike');
  });
});

// ---------- isGroupJid ----------

describe('isGroupJid', () => {
  it('treats @g.us as group', () => {
    expect(isGroupJid('123-456@g.us')).toBe(true);
  });
  it('treats @s.whatsapp.net as DM', () => {
    expect(isGroupJid('12345@s.whatsapp.net')).toBe(false);
  });
  it('treats status@broadcast as non-group', () => {
    expect(isGroupJid('status@broadcast')).toBe(false);
  });
});

// ---------- isOwnEcho ----------

describe('isOwnEcho', () => {
  it('matches case-insensitively', () => {
    expect(isOwnEcho(wa({ pushName: 'Hermes' }), 'hermes')).toBe(true);
    expect(isOwnEcho(wa({ pushName: 'HERMES' }), 'hermes')).toBe(true);
  });
  it('does not match other names', () => {
    expect(isOwnEcho(wa({ pushName: 'Alice' }), 'hermes')).toBe(false);
  });
  it('returns false when assistantName is empty (no guard)', () => {
    expect(isOwnEcho(wa({ pushName: 'Hermes' }), '')).toBe(false);
  });
  it('returns false when pushName missing', () => {
    const msg = wa({});
    (msg as any).pushName = undefined;
    expect(isOwnEcho(msg, 'hermes')).toBe(false);
  });
});

// ---------- extractContent ----------

describe('extractContent', () => {
  it('returns empty for missing message body', () => {
    const r = extractContent(wa({ message: null }));
    expect(r).toEqual({ content: '', isVoiceNote: false });
  });

  it('reads plain conversation text', () => {
    const r = extractContent(wa({ message: { conversation: 'hi' } }));
    expect(r.content).toBe('hi');
    expect(r.isVoiceNote).toBe(false);
    expect(r.mediaMime).toBeUndefined();
  });

  it('reads extendedTextMessage.text', () => {
    const r = extractContent(
      wa({ message: { extendedTextMessage: { text: 'long form' } } }),
    );
    expect(r.content).toBe('long form');
  });

  it('image with caption uses caption', () => {
    const r = extractContent(
      wa({
        message: {
          imageMessage: { caption: 'sunset', mimetype: 'image/jpeg' },
        },
      }),
    );
    expect(r.content).toBe('sunset');
    expect(r.mediaMime).toBe('image/jpeg');
  });

  it('image without caption gets [Image] placeholder', () => {
    const r = extractContent(
      wa({ message: { imageMessage: { mimetype: 'image/png' } } }),
    );
    expect(r.content).toBe('[Image]');
    expect(r.mediaMime).toBe('image/png');
  });

  it('video with caption', () => {
    const r = extractContent(
      wa({
        message: {
          videoMessage: { caption: 'cat', mimetype: 'video/mp4' },
        },
      }),
    );
    expect(r.content).toBe('cat');
    expect(r.mediaMime).toBe('video/mp4');
  });

  it('video without caption', () => {
    const r = extractContent(
      wa({ message: { videoMessage: { mimetype: 'video/mp4' } } }),
    );
    expect(r.content).toBe('[Video]');
  });

  it('audio non-PTT marked [Audio]', () => {
    const r = extractContent(
      wa({
        message: { audioMessage: { ptt: false, mimetype: 'audio/mpeg' } },
      }),
    );
    expect(r.content).toBe('[Audio]');
    expect(r.isVoiceNote).toBe(false);
    expect(r.mediaMime).toBe('audio/mpeg');
  });

  it('audio PTT marked [Voice Note] and isVoiceNote=true', () => {
    const r = extractContent(
      wa({
        message: { audioMessage: { ptt: true, mimetype: 'audio/ogg' } },
      }),
    );
    expect(r.content).toBe('[Voice Note]');
    expect(r.isVoiceNote).toBe(true);
    expect(r.mediaMime).toBe('audio/ogg');
  });

  it('document with filename [File: name]', () => {
    const r = extractContent(
      wa({
        message: {
          documentMessage: {
            fileName: 'report.pdf',
            mimetype: 'application/pdf',
          },
        },
      }),
    );
    expect(r.content).toBe('[File: report.pdf]');
    expect(r.mediaMime).toBe('application/pdf');
    expect(r.mediaFilename).toBe('report.pdf');
  });

  it('document caption beats placeholder', () => {
    const r = extractContent(
      wa({
        message: {
          documentMessage: {
            fileName: 'a.pdf',
            caption: 'see attached',
            mimetype: 'application/pdf',
          },
        },
      }),
    );
    expect(r.content).toBe('see attached');
  });

  it('document without filename [File]', () => {
    const r = extractContent(
      wa({
        message: { documentMessage: { mimetype: 'application/octet-stream' } },
      }),
    );
    expect(r.content).toBe('[File]');
  });

  it('sticker [Sticker]', () => {
    const r = extractContent(wa({ message: { stickerMessage: {} } }));
    expect(r.content).toBe('[Sticker]');
  });

  it('unknown message kind returns empty content', () => {
    const r = extractContent(wa({ message: { protocolMessage: {} } }));
    expect(r.content).toBe('');
    expect(r.mediaMime).toBeUndefined();
  });
});

// ---------- buildMessagePayload ----------

describe('buildMessagePayload', () => {
  it('returns null when fromMe', () => {
    const msg = wa({ fromMe: true });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec);
    expect(out).toBeNull();
  });

  it('returns null when remoteJid missing', () => {
    const msg = wa({});
    (msg.key as any).remoteJid = null;
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec);
    expect(out).toBeNull();
  });

  it('returns null for status@broadcast', () => {
    const msg = wa({ remoteJid: 'status@broadcast' });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec);
    expect(out).toBeNull();
  });

  it('returns null when content empty and no media', () => {
    const msg = wa({ message: { protocolMessage: {} } });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec);
    expect(out).toBeNull();
  });

  it('builds DM text payload with whatsapp: prefix', () => {
    const msg = wa({
      remoteJid: '12345@s.whatsapp.net',
      id: 'M1',
      messageTimestamp: 1600,
      message: { conversation: 'hi' },
    });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.chat_jid).toBe('whatsapp:12345@s.whatsapp.net');
    expect(out.sender).toBe('whatsapp:12345@s.whatsapp.net');
    expect(out.sender_name).toBe('Alice');
    expect(out.content).toBe('hi');
    expect(out.id).toBe('M1');
    expect(out.is_group).toBe(false);
    expect(out.timestamp).toBe(1600);
    expect(out.attachment).toBeUndefined();
    expect(out.reply_to).toBeUndefined();
  });

  it('falls back to nowSec when messageTimestamp missing', () => {
    const msg = wa({ messageTimestamp: undefined });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.timestamp).toBe(FIXED_NOW);
  });

  it('falls back to jid-prefix sender_name when pushName missing', () => {
    const msg = wa({
      remoteJid: '99999@s.whatsapp.net',
      pushName: '',
    });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.sender_name).toBe('99999');
  });

  it('group jid: participant becomes sender, is_group true', () => {
    const msg = wa({
      remoteJid: '120363@g.us',
      participant: '67890@s.whatsapp.net',
      message: { conversation: 'group chat' },
    });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.chat_jid).toBe('whatsapp:120363@g.us');
    expect(out.sender).toBe('whatsapp:67890@s.whatsapp.net');
    expect(out.is_group).toBe(true);
  });

  it('image with caption + media buffer fills attachment fields', () => {
    const msg = wa({
      message: { imageMessage: { caption: 'pic', mimetype: 'image/jpeg' } },
    });
    const buf = Buffer.from('binary data');
    const out = buildMessagePayload(msg, extractContent(msg), buf, nowSec)!;
    expect(out.content).toBe('pic');
    expect(out.attachment).toBe(buf.toString('base64'));
    expect(out.attachment_mime).toBe('image/jpeg');
    expect(out.attachment_name).toBeUndefined();
  });

  it('document includes attachment_name', () => {
    const msg = wa({
      message: {
        documentMessage: {
          fileName: 'spec.pdf',
          mimetype: 'application/pdf',
        },
      },
    });
    const buf = Buffer.from('%PDF');
    const out = buildMessagePayload(msg, extractContent(msg), buf, nowSec)!;
    expect(out.content).toBe('[File: spec.pdf]');
    expect(out.attachment_name).toBe('spec.pdf');
    expect(out.attachment_mime).toBe('application/pdf');
  });

  it('voice note: content [Voice Note], audio mime preserved', () => {
    const msg = wa({
      message: {
        audioMessage: { ptt: true, mimetype: 'audio/ogg; codecs=opus' },
      },
    });
    const buf = Buffer.from('ogg');
    const out = buildMessagePayload(msg, extractContent(msg), buf, nowSec)!;
    expect(out.content).toBe('[Voice Note]');
    expect(out.attachment_mime).toBe('audio/ogg; codecs=opus');
  });

  it('media buffer present but no mime: attachment_mime omitted', () => {
    const msg = wa({
      message: { stickerMessage: {} },
    });
    const buf = Buffer.from('sticker');
    const out = buildMessagePayload(msg, extractContent(msg), buf, nowSec)!;
    expect(out.attachment).toBe(buf.toString('base64'));
    expect(out.attachment_mime).toBeUndefined();
  });

  it('media download failed (mediaBuffer null) but caption present → text-only payload', () => {
    const msg = wa({
      message: {
        imageMessage: { caption: 'see image', mimetype: 'image/jpeg' },
      },
    });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.content).toBe('see image');
    expect(out.attachment).toBeUndefined();
    expect(out.attachment_mime).toBeUndefined();
  });

  it('reply: extracts reply_to/text/sender from extendedTextMessage', () => {
    const msg = wa({
      message: {
        extendedTextMessage: {
          text: 'reply body',
          contextInfo: {
            stanzaId: 'STZ1',
            participant: '67890@s.whatsapp.net',
            quotedMessage: { conversation: 'parent' },
          },
        },
      },
    });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.reply_to).toBe('STZ1');
    expect(out.reply_to_text).toBe('parent');
    expect(out.reply_to_sender).toBe('whatsapp:67890@s.whatsapp.net');
  });

  it('reply with no participant: reply_to_sender omitted', () => {
    const msg = wa({
      message: {
        extendedTextMessage: {
          text: 'r',
          contextInfo: {
            stanzaId: 'STZ2',
            quotedMessage: { conversation: 'p' },
          },
        },
      },
    });
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.reply_to).toBe('STZ2');
    expect(out.reply_to_text).toBe('p');
    expect(out.reply_to_sender).toBeUndefined();
  });

  it('empty id key becomes empty string id field', () => {
    const msg = wa({ id: undefined });
    (msg.key as any).id = undefined;
    const out = buildMessagePayload(msg, extractContent(msg), null, nowSec)!;
    expect(out.id).toBe('');
  });
});

// ---------- buildReactionPayload ----------

describe('buildReactionPayload', () => {
  function reactEv(
    opts: Partial<ReactionEvent['reaction']> & {
      remoteJid?: string;
      id?: string;
      keyParticipant?: string;
      reactionParticipant?: string;
    } = {},
  ): ReactionEvent {
    return {
      key: {
        remoteJid: opts.remoteJid ?? '12345@s.whatsapp.net',
        id: opts.id ?? 'TARGET1',
        ...(opts.keyParticipant ? { participant: opts.keyParticipant } : {}),
      },
      reaction: {
        text: opts.text ?? '👍',
        senderTimestampMs: opts.senderTimestampMs,
        ...(opts.reactionParticipant
          ? { key: { participant: opts.reactionParticipant } }
          : {}),
      },
    };
  }

  it('returns null when reaction text is empty (removal)', () => {
    expect(buildReactionPayload(reactEv({ text: '' }), nowSec)).toBeNull();
    const ev = reactEv();
    ev.reaction = { text: null };
    expect(buildReactionPayload(ev, nowSec)).toBeNull();
  });

  it('returns null when remoteJid missing', () => {
    const ev = reactEv();
    ev.key.remoteJid = null;
    expect(buildReactionPayload(ev, nowSec)).toBeNull();
  });

  it('returns null for status@broadcast', () => {
    expect(
      buildReactionPayload(reactEv({ remoteJid: 'status@broadcast' }), nowSec),
    ).toBeNull();
  });

  it('positive emoji → verb=like, reply_to set to target', () => {
    const out = buildReactionPayload(
      reactEv({ text: '🎉', id: 'TGT99' }),
      nowSec,
    )!;
    expect(out.verb).toBe('like');
    expect(out.reaction).toBe('🎉');
    expect(out.content).toBe('🎉');
    expect(out.reply_to).toBe('TGT99');
    expect(out.id).toBe('TGT99:r:🎉');
  });

  it('negative emoji → verb=dislike', () => {
    const out = buildReactionPayload(reactEv({ text: '👎' }), nowSec)!;
    expect(out.verb).toBe('dislike');
    expect(out.reaction).toBe('👎');
  });

  it('DM: sender derives from key.remoteJid when no participant', () => {
    const out = buildReactionPayload(
      reactEv({ remoteJid: '12345@s.whatsapp.net' }),
      nowSec,
    )!;
    expect(out.sender).toBe('whatsapp:12345@s.whatsapp.net');
    expect(out.sender_name).toBe('12345');
    expect(out.is_group).toBe(false);
  });

  it('group: prefers reaction.key.participant, then key.participant, then remoteJid', () => {
    const out = buildReactionPayload(
      reactEv({
        remoteJid: '120363@g.us',
        keyParticipant: '111@s.whatsapp.net',
        reactionParticipant: '222@s.whatsapp.net',
      }),
      nowSec,
    )!;
    expect(out.sender).toBe('whatsapp:222@s.whatsapp.net');
    expect(out.is_group).toBe(true);
  });

  it('group fallback when reaction.key.participant missing', () => {
    const out = buildReactionPayload(
      reactEv({
        remoteJid: '120363@g.us',
        keyParticipant: '111@s.whatsapp.net',
      }),
      nowSec,
    )!;
    expect(out.sender).toBe('whatsapp:111@s.whatsapp.net');
  });

  it('timestamp: uses reaction.senderTimestampMs when > 0', () => {
    const out = buildReactionPayload(
      reactEv({ senderTimestampMs: 5_000_000 }),
      nowSec,
    )!;
    expect(out.timestamp).toBe(5000); // ms → sec
  });

  it('timestamp: falls back to nowSec when senderTimestampMs absent', () => {
    const out = buildReactionPayload(reactEv({}), nowSec)!;
    expect(out.timestamp).toBe(FIXED_NOW);
  });

  it('timestamp: falls back when senderTimestampMs is zero', () => {
    const out = buildReactionPayload(
      reactEv({ senderTimestampMs: 0 }),
      nowSec,
    )!;
    expect(out.timestamp).toBe(FIXED_NOW);
  });

  it('chat_jid prefixed with whatsapp:', () => {
    const out = buildReactionPayload(reactEv({}), nowSec)!;
    expect(out.chat_jid).toBe('whatsapp:12345@s.whatsapp.net');
  });
});
