import net from 'net';

const MCP_SOCK = '/workspace/ipc/gated.sock';

export interface SubmitTurnPayload {
  turn_id: string;
  session_id?: string;
  status: 'success' | 'error';
  result?: string;
  error?: string;
}

let nextId = 1;

export async function submitTurn(p: SubmitTurnPayload): Promise<void> {
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
          finish(new Error(`submit_turn rpc error: ${resp.error.message ?? line}`));
          return;
        }
        finish();
      } catch (err) {
        finish(err instanceof Error ? err : new Error(String(err)));
      }
    });
    sock.on('error', err => finish(err));
    sock.on('end', () => finish(new Error('submit_turn: socket closed before response')));

    sock.on('connect', () => {
      const req = JSON.stringify({ jsonrpc: '2.0', id, method: 'submit_turn', params: p });
      sock.write(req + '\n');
    });
  });
}
