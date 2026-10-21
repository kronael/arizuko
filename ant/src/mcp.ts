import net from 'net';

const MCP_SOCK = '/run/ipc/gated.sock';

// ModelUsage is one model's per-turn accounting as the harness reports it.
// routd logs it and stores nothing: the model gateway is the ledger, and a
// self-reported number is what a bypassed gateway looks like (spec 6/39).
// Mirrors the SDK's ModelUsage in snake_case; cost_cents is costUSD × 100
// rounded.
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
  error?: string;
  // Per-model usage from the SDK's result message. Optional; gated
  // no-ops if absent.
  models?: Record<string, ModelUsage>;
  // True when this turn's result is the graceful query-timeout summary, not a
  // normal completion. routd logs a WARN annotation; outcome stays OK.
  timed_out?: boolean;
}

// SubmitStatusPayload declares a summary or updates the turn's progress.
export interface SubmitStatusPayload {
  turn_id: string;
  text: string;
  kind?: 'summary' | 'progress';
}

let nextId = 1;

// rpc fires one JSON-RPC request at routd's MCP socket and resolves when the
// daemon answers (or rejects on rpc-level error). One round-trip, one connection
// — shared by submit_turn (turn-end) and submit_status (mid-turn).
export function rpc(method: string, params: unknown, socketPath = MCP_SOCK): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const sock = net.createConnection(socketPath);
    const id = nextId++;
    let buf = '';
    let settled = false;

    const finish = (err?: Error, result?: unknown) => {
      if (settled) return;
      settled = true;
      sock.end();
      err ? reject(err) : resolve(result);
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
        if (resp.id !== id) throw new Error(`${method}: response id mismatch`);
        finish(undefined, resp.result);
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

export interface TurnAck {
  nudge: string;
}

function nudgeFrom(result: unknown) {
  if (result == null || typeof result !== 'object') throw new Error('invalid turn response');
  const nudge = 'nudge' in result ? result.nudge : '';
  if (typeof nudge !== 'string') throw new Error('invalid turn nudge');
  return nudge;
}

export async function submitTurn(p: SubmitTurnPayload, socketPath = MCP_SOCK) {
  return { nudge: nudgeFrom(await rpc('submit_turn', p, socketPath)) };
}

export async function checkTurn(turnID: string, socketPath = MCP_SOCK) {
  return { nudge: nudgeFrom(await rpc('check_turn', { turn_id: turnID }, socketPath)) };
}

export async function submitStatus(p: SubmitStatusPayload): Promise<void> {
  await rpc('submit_status', p);
}
