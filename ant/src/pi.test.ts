// pi harness mapping, tested against RECORDED `pi -p --mode json` streams (pi
// 0.51.6, captured 2026-08-20). Both live captures errored, so the success shape
// is constructed from pi-ai's published types.

import { test, expect, spyOn } from 'bun:test';
import { PiTurn, piArgs, piEnv, piMcpConfig, codexAuth } from './pi.js';
import type { Frame } from './backend.js';

// Recorded verbatim (trimmed to the load-bearing fields): the codex-serve
// endpoint answering 404 — stopReason "error" with an errorMessage.
const ERR_ASSISTANT = {
  role: 'assistant',
  content: [],
  provider: 'codex-serve',
  model: 'gpt-5.2-codex',
  usage: {
    input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  },
  stopReason: 'error',
  errorMessage: '404 404 page not found\n',
};

const ERR_FIXTURE = [
  { type: 'session', version: 3, id: '7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c' },
  { type: 'agent_start' },
  { type: 'turn_start' },
  { type: 'message_end', message: { role: 'user', content: [{ type: 'text', text: 'ping' }] } },
  { type: 'message_end', message: ERR_ASSISTANT },
  { type: 'turn_end', message: ERR_ASSISTANT, toolResults: [] },
  { type: 'agent_end', messages: [ERR_ASSISTANT] },
];

// The success shape per pi-ai types: StopReason "stop", text content parts.
const OK_ASSISTANT = {
  role: 'assistant',
  content: [{ type: 'text', text: 'pong' }],
  provider: 'claude-serve',
  model: 'haiku',
  usage: {
    input: 12, output: 3, cacheRead: 4, cacheWrite: 1, totalTokens: 20,
    cost: { total: 0.0234 },
  },
  stopReason: 'stop',
};

function play(turn: PiTurn, lines: Array<Record<string, unknown>>): Frame[] {
  const out: Frame[] = [];
  for (const l of lines) out.push(...turn.push(l));
  return out;
}

test('PiTurn: the recorded error stream ends as one error result — even on exit 0', () => {
  const turn = new PiTurn();
  const frames = play(turn, ERR_FIXTURE);
  // No assistant text ever streamed; only the session init surfaced.
  expect(frames).toEqual([{
    kind: 'continuity',
    token: '7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c',
    sessionId: '7b1c6bc5-8b45-4edd-acb9-c7bbd9deb90c',
  }]);
  // pi exits 0 after a failed retry loop; the terminal must still be an error.
  const result = turn.finish(0, '');
  expect(result.ok).toBe(false);
  expect(result.text).toContain('404');
});

test('PiTurn: a success stream yields status text and one ok result with usage', () => {
  const turn = new PiTurn();
  const frames = play(turn, [
    { type: 'session', id: 's-1' },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(frames.map((f) => f.kind)).toEqual(['continuity', 'status']);
  const status = frames[1];
  if (status.kind !== 'status') throw new Error('expected status frame');
  expect(status.text).toBe('pong');
  const result = turn.finish(0, '');
  expect(result.ok).toBe(true);
  expect(result.text).toBe('pong');
  expect(result.usage).toEqual({
    'claude-serve/haiku': { input: 12, output: 3, cache_read: 4, cache_write: 1, cost_cents: 2 },
  });
});

// Each assistant message carries that one model call's usage, so a multi-message
// turn reports the SUM — keeping only the last dropped every earlier call, and
// per-message cent rounding zeroed a turn of sub-cent calls.
test('PiTurn: usage accumulates across assistant messages, rounded once', () => {
  const turn = new PiTurn();
  play(turn, [
    { type: 'message_end', message: OK_ASSISTANT },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(turn.finish(0, '').usage).toEqual({
    // 2 × (12, 3, 4, 1); cost 2 × 2.34 cents rounds to 5, not 2 × round(2.34).
    'claude-serve/haiku': { input: 24, output: 6, cache_read: 8, cache_write: 2, cost_cents: 5 },
  });
});

test('PiTurn: a retried attempt that finally succeeds delivers the last text', () => {
  // auto_retry: agent_end fires once PER ATTEMPT; the terminal is process
  // exit, so an error attempt followed by a success attempt is a success.
  const turn = new PiTurn();
  play(turn, [
    { type: 'message_end', message: ERR_ASSISTANT },
    { type: 'auto_retry_start', attempt: 1 },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(turn.finish(0, '').ok).toBe(true);
});

test('PiTurn: non-zero exit is an error result carrying stderr', () => {
  const result = new PiTurn().finish(1, 'boom\n');
  expect(result.ok).toBe(false);
  expect(result.text).toBe('boom');
});

test('piArgs: no self-granted extensions, scanned skills only, @file prompt', () => {
  const args = piArgs(
    { cwd: '/home/node', model: 'claude-serve/sonnet', systemPrompt: 'SYS' },
    '/tmp/prompt.md',
    ['/home/node/.claude/skills/diary'],
  );
  expect(args).not.toContain('--no-session');
  // ~/.pi extensions are agent-authored tool registrations (J14 class);
  // --no-skills drops pi's own discovery while explicit --skill still loads.
  expect(args).toContain('--no-extensions');
  expect(args).toContain('--no-skills');
  // Discovery off, but an explicit -e still loads — that is how arizuko's
  // policy rows run on pi while nothing the agent wrote under ~/.pi does.
  expect(args).toContain('/app/src/pi-extension.ts');
  expect(args).toContain('builtin:mcp');
  expect(args).toContain('--no-approve');
  expect(args).toContain('--offline');
  const i = args.indexOf('--skill');
  expect(args[i + 1]).toBe('/home/node/.claude/skills/diary');
  expect(args).toContain('claude-serve/sonnet');
  expect(args).toContain('SYS');
  // The envelope travels as an @file, never argv (kernel per-arg cap).
  expect(args.at(-1)).toBe('@/tmp/prompt.md');
});

test('piArgs: no rendered system text keeps pi\'s own default', () => {
  // The claude_code preset is claude wiring and never crosses the seam; an
  // absent systemPrompt must not become a --system-prompt flag.
  expect(piArgs({ cwd: '/home/node' }, '/tmp/p.md', [])).not.toContain('--system-prompt');
});

test('piArgs: a fresh turn names the session dir but resumes nothing', () => {
  // --session-dir on every turn: on a fresh turn it is where the new rollout
  // lands, and on a resume it is what the bare id is resolved against.
  const args = piArgs({ cwd: '/home/node' }, '/tmp/p.md', []);
  expect(args[args.indexOf('--session-dir') + 1]).toBe('/home/node/.pi/sessions');
  expect(args).not.toContain('--session');
});

test('piArgs: a stored id resumes against this group own session dir', () => {
  const args = piArgs(
    { cwd: '/home/node', continuity: 's-1' },
    '/tmp/p.md',
    [],
  );
  expect(args[args.indexOf('--session') + 1]).toBe('s-1');
  expect(args[args.indexOf('--session-dir') + 1]).toBe('/home/node/.pi/sessions');
});

test('piArgs: the session dir follows the group home, never a fixed path', () => {
  // Every group gets its own dir, so one group id can never resolve inside
  // another group home.
  const args = piArgs({ cwd: '/home/node/other' }, '/tmp/p.md', []);
  expect(args[args.indexOf('--session-dir') + 1]).toBe('/home/node/other/.pi/sessions');
});

test('PiTurn: a missing rollout retries fresh instead of delivering the diagnostic', () => {
  // pi prints this and exits 1 when the id resolves to no rollout in our dir
  // (main.js:331-333). Delivering it would reply to the user with a harness
  // error; the chassis must run the turn again with no continuity.
  const t = new PiTurn();
  const r = t.finish(1, "No session found matching 's-gone'");
  expect(r.ok).toBe(false);
  expect(r.retryFresh).toBe(true);
  expect(r.text).toBeUndefined();
});

test('PiTurn: an ordinary failure is delivered, not retried', () => {
  const t = new PiTurn();
  const r = t.finish(1, 'provider auth failed');
  expect(r.ok).toBe(false);
  expect(r.retryFresh).toBeUndefined();
  expect(r.text).toBe('provider auth failed');
});


test('pi uses the assembled MCP server without copying secrets', () => {
  expect(piMcpConfig({ arizuko: { command: 'socat', args: ['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock'], env: { TOKEN: 'secret' } } })).toEqual({
    mcpServers: { arizuko: { command: 'socat', args: ['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock'], exposure: 'direct' } },
  });
});

test('pi forwards effort and existing credential aliases', () => {
  const args = piArgs({ cwd: '/tmp', effort: 'high' }, '/tmp/p', []);
  expect(args[args.indexOf('--thinking') + 1]).toBe('high');
  expect(piEnv({ CODEX_API_KEY: 'c', CLAUDE_CODE_OAUTH_TOKEN: 'a' })).toMatchObject({ OPENAI_API_KEY: 'c', ANTHROPIC_OAUTH_TOKEN: 'a' });
  expect(piEnv({ CODEX_API_KEY: 'c', OPENAI_API_KEY: 'native' }).OPENAI_API_KEY).toBe('native');
});

test('pi emits split status and accepts a tool-only completion', () => {
  const turn = new PiTurn();
  turn.push({ type: 'message_start', message: { role: 'assistant' } });
  expect(turn.push({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', contentIndex: 0, delta: '<sta' } })).toEqual([{ kind: 'status', text: '<sta' }]);
  expect(turn.push({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', contentIndex: 0, delta: 'tus>work</status>' } })).toEqual([{ kind: 'status', text: '<status>work</status>' }]);
  turn.push({ type: 'message_end', message: { role: 'assistant', content: [], stopReason: 'toolUse' } });
  expect(turn.finish(0, '').ok).toBe(true);
});


test('pi converts the existing Codex login without inventing expiry', () => {
  const access = `header.${Buffer.from(JSON.stringify({ exp: 1234 })).toString('base64url')}.signature`;
  expect(codexAuth(JSON.stringify({ tokens: { access_token: access, refresh_token: 'refresh', account_id: 'account' } }))).toEqual({
    'openai-codex': { type: 'oauth', access, refresh: 'refresh', accountId: 'account', expires: 1234000 },
  });
  expect(codexAuth(JSON.stringify({ OPENAI_API_KEY: 'key' }))).toEqual({ openai: { type: 'api_key', key: 'key' } });
  expect(() => codexAuth(JSON.stringify({ tokens: {} }))).toThrow();
});

test('pi writes and cleans its per-turn MCP configuration', async () => {
  const fs = await import('fs');
  const os = await import('os');
  const path = await import('path');
  const { piHarness } = await import('./pi.js');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pi-test-'));
  const capture = path.join(dir, 'capture');
  const binary = path.join(dir, 'pi');
  fs.writeFileSync(binary, '#!/bin/sh\nprintf "%s\\n" "$PI_CODING_AGENT_DIR" > "$CAPTURE"\ncat "$PI_CODING_AGENT_DIR/mcp.json" >> "$CAPTURE"\nprintf \'%s\\n\' \'{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop"}}\'\n', { mode: 0o755 });
  const frames = [];
  for await (const frame of piHarness.run({ cwd: dir, prompt: 'hi', turnID: 't', deadline: Date.now() + 10000,
    tools: { arizuko: { command: 'socat', args: ['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock'], env: { KEY: 'secret' } } },
    env: { PATH: `${dir}:/usr/bin:/bin`, CAPTURE: capture } })) frames.push(frame);
  const [agentDir, config] = fs.readFileSync(capture, 'utf8').trim().split('\n');
  expect(JSON.parse(config)).toEqual(piMcpConfig({ arizuko: { command: 'socat', args: ['STDIO', 'UNIX-CONNECT:/run/ipc/gated.sock'] } }));
  expect(fs.existsSync(agentDir)).toBe(false);
  expect(frames.at(-1)).toMatchObject({ kind: 'result', ok: true });
});

/**
 * A resumed pi session reports the turn, not the session: `pi --mode json`
 * prints the session header and then only the events of this run
 * (print-mode.js subscribes after the session loads), so the second turn on one
 * session reports only its own call.
 */
test('pi on a resumed session reports only the turn it ran', async () => {
  const fs = await import('fs');
  const os = await import('os');
  const path = await import('path');
  const { piHarness } = await import('./pi.js');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pi-resume-'));
  const call = (input: number) => JSON.stringify({ type: 'message_end', message: { ...OK_ASSISTANT,
    usage: { ...OK_ASSISTANT.usage, input, cost: { total: 0.01 } } } });
  fs.writeFileSync(path.join(dir, 'pi'), `#!/bin/sh
printf '%s\\n' '{"type":"session","id":"s-1"}'
case "$*" in
  *"--session s-1"*) printf '%s\\n' '${call(7)}' ;;
  *) printf '%s\\n' '${call(1000)}' ;;
esac
`, { mode: 0o755 });
  const usages = [];
  for (const continuity of [undefined, 's-1']) {
    for await (const frame of piHarness.run({ cwd: dir, prompt: 'hi', turnID: 't', continuity, deadline: Date.now() + 10000,
      env: { PATH: `${dir}:/usr/bin:/bin` } }))
      if (frame.kind === 'result') usages.push(frame.usage);
  }
  expect(usages).toEqual([
    { 'claude-serve/haiku': { input: 1000, output: 3, cache_read: 4, cache_write: 1, cost_cents: 1 } },
    { 'claude-serve/haiku': { input: 7, output: 3, cache_read: 4, cache_write: 1, cost_cents: 1 } },
  ]);
});

/** Opaque Pi errors, aborts, malformed streams and CLI exits retry at the consumer (BUGS L887). */
test('Pi failures are retryable through runTurn', async () => {
  const { runTurn } = await import('./index.js');
  for (const [reason, exit] of [['error', 0], ['aborted', 0], ['', 0], ['', 1]] as const) {
    const turn = new PiTurn();
    if (reason) turn.push({ type: 'message_end', message: { ...ERR_ASSISTANT, stopReason: reason } });
    else turn.push({ type: 'malformed' });
    const frame = turn.finish(exit, '');
    const harness = { name: 'pi', profile: { continuity: 'none', tools: 'none', guard: 'none', steering: 'between-turn', manual: 'native', status: 'terminal-only' }, run: async function* () { yield frame; } } as const;
    const result = await runTurn(harness, 'p', undefined, { prompt: 'p', groupFolder: 'g', chatJid: 'j' }, {}, 't', async () => {}, async () => ({ remind: false }));
    expect(result.failed).toBe('retryable');
  }
});

// Runs a turn with `models` as the read-only /mnt/pi/models.json and returns the
// models.json the fake pi found in its agent directory (undefined if none).
async function runWithMountedModels(models: unknown, env: Record<string, string>): Promise<string | undefined> {
  const fs = (await import('fs')).default;
  const os = await import('os');
  const path = await import('path');
  const { piHarness } = await import('./pi.js');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pi-models-'));
  const capture = path.join(dir, 'capture');
  fs.writeFileSync(path.join(dir, 'pi'), '#!/bin/sh\ncat "$PI_CODING_AGENT_DIR/models.json" > "$CAPTURE" 2>/dev/null\nprintf \'%s\\n\' \'{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop"}}\'\n', { mode: 0o755 });
  const file = '/mnt/pi/models.json';
  const exists = fs.existsSync, real = fs.realpathSync, read = fs.readFileSync;
  const spies = [
    spyOn(fs, 'existsSync').mockImplementation(((f: string) => f === file || exists(f)) as typeof fs.existsSync),
    spyOn(fs, 'realpathSync').mockImplementation(((f: string) => f === file ? f : real(f)) as typeof fs.realpathSync),
    spyOn(fs, 'readFileSync').mockImplementation(((f: string, ...rest: unknown[]) =>
      f === '/proc/self/mountinfo' ? '36 35 98:0 / /mnt/pi ro,relatime - ext4 /dev/x rw\n'
        : f === file ? JSON.stringify(models)
        : (read as (...a: unknown[]) => unknown)(f, ...rest)) as typeof fs.readFileSync),
  ];
  try {
    for await (const _ of piHarness.run({ cwd: dir, prompt: 'hi', turnID: 't', deadline: Date.now() + 10000,
      env: { PATH: `${dir}:/usr/bin:/bin`, CAPTURE: capture, ...env } })) { /* drain */ }
  } finally {
    for (const spy of spies) spy.mockRestore();
  }
  return exists(capture) ? read(capture, 'utf8') || undefined : undefined;
}

test('pi in gateway mode accepts a models.json provider under the gateway URL', async () => {
  const models = { providers: {
    gw: { baseUrl: 'http://gateway/v1', api: 'openai-completions', models: [{ id: 'local-large' }] },
    root: { baseUrl: 'http://gateway/', api: 'openai-completions', models: [{ id: 'm' }] },
    bare: { baseUrl: 'http://gateway', api: 'openai-completions', models: [{ id: 'm' }] },
  } };
  const env = { MODEL_GATEWAY_URL: 'http://gateway/', MODEL_GATEWAY_KEY: 'vk' };
  expect(JSON.parse((await runWithMountedModels(models, env))!)).toEqual(models);
});

test('pi in gateway mode rejects a models.json provider that bypasses the gateway', async () => {
  const env = { MODEL_GATEWAY_URL: 'http://gateway', MODEL_GATEWAY_KEY: 'vk' };
  for (const baseUrl of ['http://elsewhere/v1', 'http://gateway.evil', 'http://gateway.evil/v1', 'http://gatewayx/v1'])
    await expect(runWithMountedModels({ providers: { evil: { baseUrl } } }, env)).rejects.toThrow('evil');
  await expect(runWithMountedModels({ providers: { nourl: { api: 'openai-completions' } } }, env)).rejects.toThrow('nourl');
  await expect(runWithMountedModels({ providers: { gw: { baseUrl: 'http://gateway/v1' }, evil: { baseUrl: 'http://x' } } }, env)).rejects.toThrow('evil');
});

test('pi without a gateway passes any models.json provider through', async () => {
  const models = { providers: { local: { baseUrl: 'http://localhost:11434/v1' }, nourl: { api: 'openai-completions' } } };
  expect(JSON.parse((await runWithMountedModels(models, {}))!)).toEqual(models);
});
