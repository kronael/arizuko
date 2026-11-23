// pi harness mapping, tested against RECORDED `pi -p --mode json` streams (pi
// 0.51.6, captured 2026-08-20). Both live captures errored, so the success shape
// is constructed from pi-ai's published types.

import { test, expect, spyOn } from 'bun:test';
import { PiTurn, piArgs, piEnv, piMcpConfig, codexAuth, PI_SETTINGS } from './pi.js';
import type { Frame } from './backend.js';

// A model call that fails is retried three times with a doubling delay before
// the turn fails. With two retries one 30 s network fault failed a sloth turn
// (2026-11-23, three `fetch failed` in 28 s).
test('pi retries a failed model call three times before the turn fails', () => {
  expect(PI_SETTINGS.retry).toMatchObject({ enabled: true, maxRetries: 3, baseDelayMs: 2000 });
  expect(PI_SETTINGS.retry.maxAgentDelayMs).toBeGreaterThanOrEqual(8000);
});

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

test('PiTurn: a success stream yields no status frame without a block, and one ok result with usage', () => {
  const turn = new PiTurn();
  const frames = play(turn, [
    { type: 'session', id: 's-1' },
    { type: 'message_end', message: OK_ASSISTANT },
  ]);
  expect(frames.map((f) => f.kind)).toEqual(['continuity']);
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
  expect(turn.push({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', contentIndex: 0, delta: '<sta' } })).toEqual([]);
  expect(turn.push({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', contentIndex: 0, delta: 'tus>work</status>' } })).toEqual([{ kind: 'status', text: '<status>work</status>' }]);
  turn.push({ type: 'message_end', message: { role: 'assistant', content: [], stopReason: 'toolUse' } });
  expect(turn.finish(0, '').ok).toBe(true);
});

test('PiTurn: a block is handed on once per part; message_end carries only what is new', () => {
  const turn = new PiTurn();
  const d = (contentIndex: number, delta: string) => turn.push({ type: 'message_update', assistantMessageEvent: { type: 'text_delta', contentIndex, delta } });
  turn.push({ type: 'message_start', message: { role: 'assistant' } });
  expect(d(0, 'a <status>one</status> then')).toEqual([{ kind: 'status', text: 'a <status>one</status>' }]);
  expect(d(0, ' more')).toEqual([]);
  expect(d(1, '<status>two</status>')).toEqual([{ kind: 'status', text: '<status>two</status>' }]);
  expect(d(0, ' <status>three</status><status>four</status>')).toEqual([{ kind: 'status', text: ' then more <status>three</status><status>four</status>' }]);
  const end = { role: 'assistant', stopReason: 'stop', content: [{ type: 'text', text: 'a <status>one</status> then more <status>three</status><status>four</status>' }, { type: 'text', text: '<status>two</status> <status>five</status>' }] };
  expect(turn.push({ type: 'message_end', message: end })).toEqual([{ kind: 'status', text: ' <status>five</status>' }]);
  turn.push({ type: 'message_start', message: { role: 'assistant' } });
  expect(d(0, '<status>one</status>')).toEqual([{ kind: 'status', text: '<status>one</status>' }]);
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

// Borrowed Codex login: pi refreshes the token inside its temporary agent dir, and
// ant carries the refresh back to ~/.codex/auth.json (BUGS L902).
const jwt = (exp: number) => `h.${Buffer.from(JSON.stringify({ exp })).toString('base64url')}.s`;
const OLD = { access: jwt(1000), refresh: 'old-refresh-TOKEN' };
const NEW = { access: jwt(2000), refresh: 'new-refresh-TOKEN' };
const piRecord = (t: { access: string; refresh: string }, accountId = 'acct') =>
  JSON.stringify({ 'openai-codex': { type: 'oauth', ...t, accountId, expires: 2000000 } });
const OAUTH_LOGIN = {
  OPENAI_API_KEY: null,
  tokens: { id_token: 'id.tok.en', access_token: OLD.access, refresh_token: OLD.refresh, account_id: 'acct' },
  last_refresh: '2020-01-01T00:00:00.000Z',
  extra: { keep: ['me', 1] },
};

interface LoginRun { cwd: string; file: string; frames: Frame[]; logs: string[]; before: string; agentAuthBefore: boolean }

// Runs one turn whose ~/.codex/auth.json holds `login`. `mount` is the mountinfo
// line: 'file' = the file is itself a mount (rw), 'ro' = the same, read-only,
// 'dir' = only the enclosing ~/.codex directory is mounted. `pi` is the fake pi's
// shell body after it has looked at its agent dir.
async function runWithLogin(login: unknown, opts: { mount: 'file' | 'ro' | 'dir'; pi: string; env?: Record<string, string>; failWrite?: boolean }): Promise<LoginRun> {
  const fs = (await import('fs')).default;
  const os = await import('os');
  const path = await import('path');
  const { piHarness } = await import('./pi.js');
  const cwd = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'pi-login-')));
  const file = path.join(cwd, '.codex', 'auth.json');
  fs.mkdirSync(path.dirname(file));
  const before = typeof login === 'string' ? login : JSON.stringify(login, null, 2) + '\n';
  fs.writeFileSync(file, before);
  fs.writeFileSync(path.join(cwd, 'pi'), `#!/bin/sh
if [ -f "$PI_CODING_AGENT_DIR/auth.json" ]; then echo yes > "$CAPTURE"; else echo no > "$CAPTURE"; fi
${opts.pi}
printf '%s\\n' '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop"}}'
`, { mode: 0o755 });
  const mountinfo = opts.mount === 'dir'
    ? `36 35 98:0 / ${path.dirname(file)} rw,relatime - ext4 /dev/x rw\n`
    : `36 35 98:0 /auth.json ${file} ${opts.mount === 'ro' ? 'ro' : 'rw'},relatime - ext4 /dev/x rw\n`;
  const read = fs.readFileSync, write = fs.writeFileSync;
  const logs: string[] = [];
  const spies = [
    spyOn(fs, 'readFileSync').mockImplementation(((f: string, ...rest: unknown[]) =>
      f === '/proc/self/mountinfo' ? mountinfo : (read as (...a: unknown[]) => unknown)(f, ...rest)) as typeof fs.readFileSync),
    spyOn(console, 'error').mockImplementation(((m: string) => { logs.push(String(m)); }) as typeof console.error),
  ];
  if (opts.failWrite) spies.push(spyOn(fs, 'writeFileSync').mockImplementation(((f: string, ...rest: unknown[]) => {
    if (f === file) throw Object.assign(new Error(`EROFS: read-only file system, open '${f}'`), { code: 'EROFS' });
    return (write as (...a: unknown[]) => unknown)(f, ...rest);
  }) as typeof fs.writeFileSync));
  const frames: Frame[] = [];
  try {
    for await (const frame of piHarness.run({ cwd, prompt: 'hi', turnID: 't', deadline: Date.now() + 10000,
      env: { PATH: `${cwd}:/usr/bin:/bin`, CAPTURE: path.join(cwd, 'capture'), ...opts.env } })) frames.push(frame);
  } finally {
    for (const spy of spies) spy.mockRestore();
  }
  return { cwd, file, frames, logs, before, agentAuthBefore: fs.readFileSync(path.join(cwd, 'capture'), 'utf8').trim() === 'yes' };
}
const REFRESH_SH = `printf '%s' "$REFRESHED" > "$PI_CODING_AGENT_DIR/auth.json"`;
const refreshEnv = (t = NEW) => ({ REFRESHED: piRecord(t) });

test('a token pi refreshes during the turn reaches ~/.codex/auth.json in Codex format', async () => {
  const fs = (await import('fs')).default;
  const r = await runWithLogin(OAUTH_LOGIN, { mount: 'file', pi: REFRESH_SH, env: refreshEnv() });
  const after = JSON.parse(fs.readFileSync(r.file, 'utf8'));
  expect(after.tokens.access_token).toBe(NEW.access);
  expect(after.tokens.refresh_token).toBe(NEW.refresh);
  expect(after.tokens.account_id).toBe('acct');
  expect(after.tokens.id_token).toBe('id.tok.en');
  expect(after.OPENAI_API_KEY).toBeNull();
  expect(after.extra).toEqual({ keep: ['me', 1] });
  expect(after.last_refresh).not.toBe('2020-01-01T00:00:00.000Z');
  expect(Math.abs(Date.parse(after.last_refresh) - Date.now())).toBeLessThan(60000);
  expect(r.frames.at(-1)).toMatchObject({ kind: 'result', ok: true });
});

test('a refresh that changes the account id writes it too', async () => {
  const fs = (await import('fs')).default;
  const r = await runWithLogin(OAUTH_LOGIN, { mount: 'file', pi: REFRESH_SH, env: { REFRESHED: piRecord(NEW, 'acct-2') } });
  expect(JSON.parse(fs.readFileSync(r.file, 'utf8')).tokens.account_id).toBe('acct-2');
});

test('a pi that refreshes nothing leaves ~/.codex/auth.json byte-identical', async () => {
  const fs = (await import('fs')).default;
  const odd = `{"tokens":{"access_token":"${OLD.access}","refresh_token":"${OLD.refresh}","account_id":"acct"},"last_refresh":"x"}\n\n`;
  const r = await runWithLogin(odd, { mount: 'file', pi: 'true' });
  expect(fs.readFileSync(r.file, 'utf8')).toBe(odd);
  const same = await runWithLogin(odd, { mount: 'file', pi: REFRESH_SH, env: { REFRESHED: piRecord(OLD) } });
  expect(fs.readFileSync(same.file, 'utf8')).toBe(odd);
});

test('a pi that fails after refreshing still saves the token', async () => {
  const fs = (await import('fs')).default;
  const r = await runWithLogin(OAUTH_LOGIN, { mount: 'file', pi: `${REFRESH_SH}\nexit 1`, env: refreshEnv() });
  expect(r.frames.at(-1)).toMatchObject({ kind: 'result', ok: false });
  expect(JSON.parse(fs.readFileSync(r.file, 'utf8')).tokens.refresh_token).toBe(NEW.refresh);
});

test('an API-key login is never rewritten', async () => {
  const fs = (await import('fs')).default;
  const r = await runWithLogin({ OPENAI_API_KEY: 'sk-fake' }, { mount: 'file', pi: REFRESH_SH, env: refreshEnv() });
  expect(fs.readFileSync(r.file, 'utf8')).toBe(r.before);
});

test('a login that is not its own mount point is refused', async () => {
  // Only the enclosing ~/.codex directory is mounted: the file is the agent's to write.
  await expect(runWithLogin(OAUTH_LOGIN, { mount: 'dir', pi: REFRESH_SH, env: refreshEnv() })).rejects.toThrow('mount point');
});

test('a failed save is logged without token text and does not change the result', async () => {
  const fs = (await import('fs')).default;
  const r = await runWithLogin(OAUTH_LOGIN, { mount: 'ro', pi: REFRESH_SH, env: refreshEnv(), failWrite: true });
  expect(r.frames.at(-1)).toMatchObject({ kind: 'result', ok: true });
  const line = r.logs.find((l) => l.includes('not saved'));
  expect(line).toBeDefined();
  for (const secret of [NEW.access, NEW.refresh, OLD.access, OLD.refresh, 'id.tok.en']) {
    for (const l of r.logs) expect(l).not.toContain(secret);
  }
  expect(fs.readFileSync(r.file, 'utf8')).toBe(r.before);
});

test('gateway mode imports no login and writes nothing', async () => {
  const fs = (await import('fs')).default;
  const r = await runWithLogin(OAUTH_LOGIN, { mount: 'file', pi: REFRESH_SH,
    env: { ...refreshEnv(), MODEL_GATEWAY_URL: 'http://gateway', MODEL_GATEWAY_KEY: 'vk' } });
  expect(r.agentAuthBefore).toBe(false);
  expect(fs.readFileSync(r.file, 'utf8')).toBe(r.before);
});
