import { describe, expect, test } from 'bun:test';
import { extractContent, pressedID } from './inbound';
import type { WAMessage } from '@whiskeysockets/baileys';

const msg = (message: unknown): WAMessage =>
  ({ key: { id: 'm1', remoteJid: '1@s.whatsapp.net' }, message }) as WAMessage;

describe('pressedID', () => {
  // WhatsApp has four spellings for the same event across client versions.
  // Reading the ID — never the display text — is what makes a press identical
  // to typing the command it stands for.
  test('reads the id back from every button-response shape', () => {
    const cases: Array<[string, unknown]> = [
      [
        'buttonsResponseMessage',
        { buttonsResponseMessage: { selectedButtonId: '/approve ab12' } },
      ],
      [
        'templateButtonReplyMessage',
        { templateButtonReplyMessage: { selectedId: '/approve ab12' } },
      ],
      [
        'listResponseMessage',
        {
          listResponseMessage: {
            singleSelectReply: { selectedRowId: '/approve ab12' },
          },
        },
      ],
      [
        'interactiveResponseMessage',
        {
          interactiveResponseMessage: {
            nativeFlowResponseMessage: { paramsJson: '{"id":"/approve ab12"}' },
          },
        },
      ],
    ];
    for (const [name, message] of cases) {
      expect(pressedID(msg(message)), name).toBe('/approve ab12');
    }
  });

  test('never reads the display text instead of the id', () => {
    const m = msg({
      buttonsResponseMessage: {
        selectedButtonId: '/approve ab12',
        selectedDisplayText: 'Approve',
      },
    });
    expect(pressedID(m)).toBe('/approve ab12');
  });

  test('a message that is not a press reads as no press', () => {
    expect(pressedID(msg({ conversation: 'hello' }))).toBe('');
    expect(pressedID(msg(undefined))).toBe('');
    expect(
      pressedID(
        msg({
          interactiveResponseMessage: {
            nativeFlowResponseMessage: { paramsJson: 'not json' },
          },
        }),
      ),
    ).toBe('');
  });
});

describe('extractContent', () => {
  // A press must reach routd as ordinary chat text, because that is what makes
  // it meet the same operator gate a typed command meets.
  test('a press becomes the command it stands for', () => {
    const got = extractContent(
      msg({ buttonsResponseMessage: { selectedButtonId: '/approve ab12' } }),
    );
    expect(got.content).toBe('/approve ab12');
    expect(got.isVoiceNote).toBe(false);
  });

  test('plain text still wins over everything', () => {
    expect(extractContent(msg({ conversation: 'hi' })).content).toBe('hi');
  });
});
