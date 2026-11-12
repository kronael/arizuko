import net from 'net';
import type { ProviderFailure } from './backend.js';

const MCP_SOCK = '/run/ipc/gated.sock';

// ModelUsage is one model's accounting for THIS turn, never a session total,
// as the harness reports it. routd stores it as the turn's usage row and logs
// it (specs/6/39-gateway-owns-spend.md §"The turn usage record"). cost_cents
// is costUSD × 100 rounded. The credit fields are codex's account facts read
// from its rollout:
// the balance after the turn, the balance drop across the turn and the plan
// window's used share; absent where the harness has no credit account.
export interface ModelUsage {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  cost_cents: number;
  credits_spent?: number;
  credits_balance?: number;
  window_used_percent?: number;
  plan_type?: string;
}

export interface SubmitTurnPayload {
  turn_id: string;
  session_id?: string;
  status: 'success' | 'error';
  error?: string;
  // harness names the occupant that ran the turn: claude, codex or pi.
  harness?: string;
  // Per-model usage for this turn. Absent when the harness reported none.
  models?: Record<string, ModelUsage>;
  // True when this turn's result is the graceful query-timeout summary, not a
  // normal completion. routd logs a WARN annotation; outcome stays OK.
  timed_out?: boolean;
  provider_failure?: ProviderFailure;
}

// SubmitStatusPayload updates the turn's progress line.
export interface SubmitStatusPayload {
  turn_id: string;
  text?: string;
  source?: 'claude' | 'codex';
  snapshot?: import('./policy.js').ToolEvent;
}

export interface TurnStatusAck {
  remind: boolean;
  reminder_after_ms?: number;
}

export function parseStatusAck(value: unknown): TurnStatusAck {
  if (!value || typeof value !== 'object' || !('remind' in value) || typeof value.remind !== 'boolean')
    throw new Error('submit_status: invalid reminder decision');
  if ('reminder_after_ms' in value) {
    const delay = value.reminder_after_ms;
    if (typeof delay !== 'number' || !Number.isFinite(delay) || delay <= 0)
      throw new Error('submit_status: invalid reminder delay');
    return { remind: value.remind, reminder_after_ms: delay };
  }
  return { remind: value.remind };
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

export async function submitStatus(p: SubmitStatusPayload): Promise<TurnStatusAck> {
  return parseStatusAck(await rpc('submit_status', p));
}
