import net from 'net';

const MCP_SOCK = '/run/ipc/gated.sock';

// ModelUsage is one model's per-turn accounting, forwarded so gated can
// write a cost_log row per call. Spec 5/34. Mirrors the SDK's ModelUsage
// in snake_case; cost_cents is the SDK's costUSD × 100 rounded.
export interface ModelUsage {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  cost_cents: number;
}

export interface SubmitTurnPayload {
  turn_id: string;
  session_id?: string;
  status: 'success' | 'error';
  result?: string;
  error?: string;
  // Per-model usage from the SDK's result message. Optional; gated
  // no-ops if absent.
  models?: Record<string, ModelUsage>;
  // user_sub the turn ran on behalf of. Empty/undefined = channel-scoped.
  caller_sub?: string;
  // True when this turn's result is the graceful query-timeout summary, not a
  // normal completion. routd logs a WARN annotation; outcome stays OK.
  timed_out?: boolean;
}

// SubmitStatusPayload is a mid-turn progress notice. routd delivers the text
// immediately as a "⏳ ..." interim message and does NOT end the turn.
export interface SubmitStatusPayload {
  turn_id: string;
  text: string;
}

let nextId = 1;

// rpc fires one JSON-RPC request at the gated MCP socket and resolves when the
// daemon answers (or rejects on rpc-level error). One round-trip, one connection
// — shared by submit_turn (turn-end) and submit_status (mid-turn).
function rpc(method: string, params: unknown): Promise<void> {
  return new Promise((resolve, reject) => {
    const sock = net.createConnection(MCP_SOCK);
    const id = nextId++;
    let buf = '';
    let settled = false;

    const finish = (err?: Error) => {
      if (settled) return;
      settled = true;
      sock.end();
      err ? reject(err) : resolve();
    };

    sock.setEncoding('utf8');
    sock.on('data', (chunk: string | Buffer) => {
      buf += typeof chunk === 'string' ? chunk : chunk.toString('utf8');
      const nl = buf.indexOf('\n');
      if (nl < 0) return;
      const line = buf.slice(0, nl);
      try {
        const resp = JSON.parse(line);
        if (resp.error) {
          finish(new Error(`${method} rpc error: ${resp.error.message ?? line}`));
          return;
        }
        finish();
      } catch (err) {
        finish(err instanceof Error ? err : new Error(String(err)));
      }
    });
    sock.on('error', err => finish(err));
    sock.on('end', () => finish(new Error(`${method}: socket closed before response`)));

    sock.on('connect', () => {
      sock.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
    });
  });
}

export async function submitTurn(p: SubmitTurnPayload): Promise<void> {
  return rpc('submit_turn', p);
}

export async function submitStatus(p: SubmitStatusPayload): Promise<void> {
  return rpc('submit_status', p);
}
