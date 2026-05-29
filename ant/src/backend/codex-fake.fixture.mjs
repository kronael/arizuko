// Fake `codex app-server` for the codex backend test. Speaks JSON-RPC 2.0
// over stdio: answers thread/start + turn/start, then replays a scripted
// sequence of event notifications and a turn/finished. No real model, no auth.
// Pointed at via CODEX_BIN in codex.test.ts.

let buf = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => {
  buf += chunk;
  let nl;
  while ((nl = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, nl);
    buf = buf.slice(nl + 1);
    if (!line.trim()) continue;
    handle(JSON.parse(line));
  }
});

function send(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n');
}

function notify(method, params) {
  send({ jsonrpc: '2.0', method, params });
}

function handle(req) {
  if (req.method === 'thread/start') {
    send({ jsonrpc: '2.0', id: req.id, result: { threadId: 'thr-1' } });
    notify('thread/started', { threadId: 'thr-1' });
    return;
  }
  if (req.method === 'turn/start') {
    send({ jsonrpc: '2.0', id: req.id, result: { turnId: 'turn-1' } });
    // Scripted turn output → exercises the 5/K mapping table.
    notify('item/agentMessage/delta', { delta: 'hel' });
    notify('item/agentMessage/done', { text: 'hello world' });
    notify('item/mcpToolCall', { name: 'send' });
    notify('item/toolResult', { ok: true });
    notify('turn/finished', {
      text: 'hello world',
      usage: { model: 'gpt-5', inputTokens: 10, outputTokens: 5, cachedInputTokens: 2, costUSD: 0.03 },
    });
    return;
  }
  if (req.method === 'thread/close') {
    send({ jsonrpc: '2.0', id: req.id, result: {} });
    return;
  }
  // Unknown request: answer with empty result so the session doesn't hang.
  if (req.id !== undefined) send({ jsonrpc: '2.0', id: req.id, result: {} });
}
