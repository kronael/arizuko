// The installed ant runs pi against a scripted provider on loopback and a
// recording gated.sock. Runs inside the image with no network.

import fs from 'fs';
import net from 'net';

const SOCKET = '/run/ipc/gated.sock';
const KEY = 'smoke-key';
const MODEL = 'smoke/model:1b';
const NUDGE = 'smoke nudge: answer once more';

type Rpc = { id?: number; method?: string; params?: Record<string, unknown> };
type Step = { status: number; body?: string; text?: string; tool?: { name: string; args: string } };

const calls: Rpc[] = [];
const requests: { path: string; auth: string; body: Record<string, unknown> }[] = [];
let script: Step[] = [];
let nudges = 0;

function rpcResult(req: Rpc): unknown {
  switch (req.method) {
    case 'initialize':
      return { protocolVersion: req.params?.protocolVersion ?? '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'smoke', version: '1' } };
    case 'tools/list':
      return { tools: [{ name: 'reply', description: 'Reply in the chat.', inputSchema: { type: 'object', properties: { text: { type: 'string' } }, required: ['text'] } }] };
    case 'tools/call':
      return { content: [{ type: 'text', text: '{"ok":true}' }] };
    case 'submit_status':
      return { remind: false };
    case 'submit_turn':
      return { nudge: nudges-- > 0 ? NUDGE : '' };
    default:
      return {};
  }
}

function serveSocket(): net.Server {
  fs.rmSync(SOCKET, { force: true });
  const server = net.createServer((conn) => {
    let buf = '';
    conn.setEncoding('utf8');
    conn.on('data', (chunk: string) => {
      buf += chunk;
      let nl: number;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const req = JSON.parse(buf.slice(0, nl)) as Rpc;
        buf = buf.slice(nl + 1);
        calls.push(req);
        if (req.id !== undefined)
          conn.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: rpcResult(req) }) + '\n');
      }
    });
    conn.on('error', () => {});
  });
  server.listen(SOCKET);
  return server;
}

function chunk(choices: unknown[], usage?: Record<string, number>): string {
  return `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', created: 0, model: 'model:1b', choices, usage })}\n\n`;
}

function completion(step: Step): Response {
  if (step.status !== 200)
    return new Response(step.body ?? '', { status: step.status });
  const delta = step.tool
    ? { role: 'assistant', tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: step.tool.name, arguments: step.tool.args } }] }
    : { role: 'assistant', content: step.text };
  const body = chunk([{ index: 0, delta, finish_reason: null }])
    + chunk([{ index: 0, delta: {}, finish_reason: step.tool ? 'tool_calls' : 'stop' }])
    + chunk([], { prompt_tokens: 100, completion_tokens: 10, total_tokens: 110 })
    + 'data: [DONE]\n\n';
  return new Response(body, { headers: { 'content-type': 'text/event-stream' } });
}

function serveProvider() {
  return Bun.serve({
    hostname: '127.0.0.1',
    port: 23456,
    async fetch(req) {
      const url = new URL(req.url);
      requests.push({ path: url.pathname, auth: req.headers.get('authorization') ?? '', body: await req.json() as Record<string, unknown> });
      return completion(script.shift() ?? { status: 500, body: 'script exhausted' });
    },
  });
}

async function runAnt(id: string) {
  calls.length = 0;
  requests.length = 0;
  const started = Date.now();
  const proc = Bun.spawn(['/usr/local/bin/ant'], {
    cwd: '/home/node',
    stdin: new TextEncoder().encode(JSON.stringify({ prompt: 'say pong with the reply tool', groupFolder: 'smoke', messageId: id })),
    stderr: 'pipe',
    env: { ...process.env, ARIZUKO_BACKEND: 'pi', ARIZUKO_MODEL: MODEL, OPENAI_API_KEY: KEY, ARIZUKO_QUERY_TIMEOUT_MS: '120000' },
    timeout: 120_000,
  });
  const stderr = await new Response(proc.stderr).text();
  return { code: await proc.exited, stderr, seconds: (Date.now() - started) / 1000 };
}

const failures: string[] = [];

function check(name: string, ok: boolean, detail: unknown) {
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}`);
  if (!ok) {
    failures.push(name);
    console.log(typeof detail === 'string' ? detail : JSON.stringify(detail, null, 2));
  }
}

function submits() {
  return calls.filter((c) => c.method === 'submit_turn').map((c) => c.params ?? {});
}

function toolCalls() {
  return calls.filter((c) => c.method === 'tools/call').map((c) => c.params ?? {});
}

async function replyOverSocket() {
  script = [
    { status: 200, tool: { name: 'mcp__arizuko__reply', args: '{"text":"pong"}' } },
    { status: 200, text: 'final prose, not delivery' },
    { status: 200, text: 'nudged prose, not delivery' },
  ];
  nudges = 1;
  const run = await runAnt('smoke-reply');
  const tools = toolCalls();
  const turns = submits();
  const offered = (requests[0]?.body.tools as { function?: { name?: string } }[] | undefined)?.map((t) => t.function?.name) ?? [];
  check('reply over gated.sock: ant exits 0', run.code === 0, run.stderr);
  check('reply over gated.sock: the model is offered mcp__arizuko__reply', offered.includes('mcp__arizuko__reply'), offered);
  check('reply over gated.sock: one wire tools/call reply {"text":"pong"}', tools.length === 1 && tools[0].name === 'reply'
    && (tools[0].arguments as { text?: string } | undefined)?.text === 'pong', tools);
  check('reply over gated.sock: final prose never becomes delivery', !JSON.stringify(tools).includes('prose'), tools);
  check('reply over gated.sock: the nudge continues the same turn and session', turns.length === 2
    && turns.every((t) => t.turn_id === 'smoke-reply' && t.status === 'success')
    && typeof turns[0].session_id === 'string' && turns[0].session_id === turns[1].session_id, turns);
  check('reply over gated.sock: the nudged run resumes the session', requests.length === 3
    && JSON.stringify(requests[2].body.messages).includes('pong') && JSON.stringify(requests[2].body.messages).includes(NUDGE), requests.map((r) => r.body.messages));
  const usage = (turns[0]?.models as Record<string, { input: number; output: number }> | undefined)?.[MODEL];
  check('reply over gated.sock: usage sums both model calls of the first run', usage?.input === 200 && usage?.output === 20, turns[0]?.models);
}

function nativeProvider() {
  check('native provider configuration: every call reaches the fixture endpoint', requests.length > 0
    && requests.every((r) => r.path === '/v1/chat/completions' && r.body.model === 'model:1b'), requests.map((r) => [r.path, r.body.model]));
  check('native provider configuration: the key comes from $OPENAI_API_KEY', requests.every((r) => r.auth === `Bearer ${KEY}`), requests.map((r) => r.auth));
}

async function boundedFailure() {
  script = Array.from({ length: 10 }, () => ({ status: 502, body: '{"error":{"message":"upstream unreachable"}}' }));
  nudges = 0;
  const run = await runAnt('smoke-502');
  const turns = submits();
  check('bounded provider failure: ant exits non-zero', run.code !== 0 && run.code !== null, run);
  check('bounded provider failure: the retry budget is finite', requests.length >= 1 && requests.length <= 3, requests.length);
  check('bounded provider failure: submit_turn reports the error', turns.length === 1 && turns[0].status === 'error', turns);
  check('bounded provider failure: nothing is delivered', toolCalls().length === 0, toolCalls());
  check('bounded provider failure: the turn ends within a minute', run.seconds < 60, run.seconds);
}

const socket = serveSocket();
const provider = serveProvider();
try {
  await replyOverSocket();
  nativeProvider();
  await boundedFailure();
} finally {
  provider.stop(true);
  socket.close();
}
if (failures.length) {
  console.log(`pi-smoke: ${failures.length} failed`);
  process.exit(1);
}
console.log('pi-smoke: all checks passed');
