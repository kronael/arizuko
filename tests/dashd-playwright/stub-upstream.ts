// stub-upstream: one HTTP server standing in for the sibling daemons dashd
// proxies to — routd, authd, runed, proxyd.
//
// dashd's own pages are the system under test; the daemons behind them are not.
// Booting real routd/authd here would drag in a container runtime, an agent
// image and a signing key for a page whose job is to render whatever the
// sibling answered. So the sibling is stubbed and the CONTRACT is the fixture:
// every response below is the shape the matching dashd decoder declares
// (`resources.SigningKeysRow`, `apiv1.EngagementListResponse`, …), so a change
// to one of those shapes fails a test here instead of shipping.
//
// services.spec.ts covers the other half — what these pages render when the
// sibling is unreachable — by pointing at a dead port.

import {
  createServer,
  IncomingMessage,
  ServerResponse,
  Server,
} from 'node:http';

export const HELD_ID = 'seedheld';

// resolved records the verdicts POSTed during a run so a spec can assert the
// dashboard forwarded what the operator pressed, not merely that it redirected.
export const resolved: Array<{ id: string; verdict: string; body: string }> =
  [];

function json(res: ServerResponse, code: number, body: unknown): void {
  const buf = JSON.stringify(body);
  res.writeHead(code, { 'Content-Type': 'application/json' });
  res.end(buf);
}

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let s = '';
    req.on('data', (c) => (s += c));
    req.on('end', () => resolve(s));
  });
}

const pending = [
  {
    id: HELD_ID,
    group_folder: 'inbox',
    tool: 'like',
    args: '{"chatJid":"telegram:user/seed","targetId":"1","reaction":"+1"}',
    status: 'held',
    chat_jid: 'telegram:user/seed',
    created_at: '2026-08-26T10:00:00Z',
  },
  {
    id: 'seedgone',
    group_folder: 'inbox',
    tool: 'delete',
    args: '{"chatJid":"telegram:user/seed","targetId":"2"}',
    status: 'rejected',
    chat_jid: 'telegram:user/seed',
    created_at: '2026-08-26T09:00:00Z',
    reviewed_by: 'testadmin',
    reviewed_at: '2026-08-26T09:05:00Z',
    reviewer_note: 'not this one',
  },
];

export async function startStubUpstream(): Promise<{
  url: string;
  server: Server;
}> {
  const server = createServer(async (req, res) => {
    const url = new URL(req.url ?? '/', 'http://stub');
    const p = url.pathname;

    // routd
    if (req.method === 'GET' && p === '/v1/pending_actions') {
      return json(res, 200, { pending });
    }
    const verdict = /^\/v1\/pending_actions\/([^/]+)\/(approve|reject)$/.exec(
      p,
    );
    if (req.method === 'POST' && verdict) {
      resolved.push({
        id: verdict[1],
        verdict: verdict[2],
        body: await readBody(req),
      });
      return json(res, 200, { ok: true, id: verdict[1], status: verdict[2] });
    }
    if (req.method === 'GET' && p === '/v1/engagement') {
      return json(res, 200, {
        engaged: [
          {
            jid: 'telegram:user/seed',
            topic: '',
            folder: 'inbox',
            engaged_until: '2099-01-01T00:00:00Z',
          },
        ],
      });
    }
    if (req.method === 'POST' && p === '/v1/engagement') {
      return json(res, 200, { ok: true });
    }
    if (req.method === 'GET' && p === '/v1/dead_letters') {
      return json(res, 200, [
        {
          id: 'dl-1',
          chat_jid: 'telegram:user/seed',
          content: 'undeliverable',
          timestamp: '2026-08-26T08:00:00Z',
          delivery_attempts: 0,
        },
      ]);
    }
    if (req.method === 'POST' && p.startsWith('/v1/dead_letters/')) {
      return json(res, 200, { ok: true });
    }

    // audit is FEDERATED: dashd fans /v1/audit out to routd, runed, authd and
    // onbod and merges the answers, so the page shows nothing until an upstream
    // answers. authd stays unreachable here by design (see global-setup), which
    // also keeps the "one source is down" banner covered.
    if (req.method === 'GET' && p === '/v1/audit') {
      // A bare array — audit.Row[], the shape dashd's decoder declares.
      return json(res, 200, [
        {
          id: 1,
          created_at: '2026-08-26T09:30:00Z',
          category: 'mutation',
          action: 'group.create',
          actor: 'testadmin',
          actor_sub: 'testadmin',
          outcome: 'ok',
          folder: 'inbox',
          surface: 'dash',
          resource: 'groups/inbox',
        },
      ]);
    }

    // authd
    if (req.method === 'GET' && p === '/v1/signing_keys') {
      return json(res, 200, [
        {
          kid: 'k-active',
          alg: 'ES256',
          active: true,
          status: 'active',
          created_at: '2026-08-01T00:00:00Z',
        },
        {
          kid: 'k-old',
          alg: 'ES256',
          active: false,
          status: 'retiring',
          created_at: '2026-07-01T00:00:00Z',
          serves_until: '2026-09-01T00:00:00Z',
        },
      ]);
    }
    if (req.method === 'GET' && p === '/v1/sessions') {
      return json(res, 200, [
        {
          family_id: 'fam-1',
          sub: 'testadmin',
          scope: '**',
          started_at: '2026-08-26T07:00:00Z',
          renewed_at: '2026-08-26T08:00:00Z',
          expires_at: '2099-01-01T00:00:00Z',
          rotations: 2,
          status: 'active',
        },
      ]);
    }
    if (req.method === 'DELETE' && p.startsWith('/v1/sessions/')) {
      return json(res, 200, { ok: true });
    }

    // proxyd
    if (req.method === 'GET' && p === '/v1/proxyd_routes') {
      return json(res, 200, {
        routes: [
          {
            path: '/dash/',
            backend: 'http://dashd:8080',
            auth: 'user',
            owner: 'arizuko',
          },
          {
            path: '/pub/',
            backend: 'http://vited:8080',
            auth: 'public',
            owner: 'arizuko',
          },
        ],
      });
    }
    if (req.method === 'DELETE' && p.startsWith('/v1/proxyd_routes/')) {
      return json(res, 200, { ok: true });
    }

    // runed
    if (req.method === 'POST' && p === '/v1/runs/stop') {
      return json(res, 200, { ok: true, stopped: 1 });
    }

    // Introspection: the specs run in worker processes and cannot read the
    // array above directly, so the stub serves its own record of what dashd
    // forwarded. GET reads it, DELETE clears it between tests.
    if (p === '/_stub/resolved') {
      if (req.method === 'DELETE') {
        resolved.length = 0;
        return json(res, 200, { ok: true });
      }
      return json(res, 200, { resolved });
    }

    // Deliberately 503, not 200. The services hub grades a tile on this probe,
    // and services.spec's invariant (D6) is that a BUILT-but-unhealthy daemon
    // STILL links to its control plane — that is precisely the tile an operator
    // needs to click. An always-green stub would delete that assertion. The
    // /v1 handlers above answer normally, so the control-plane pages still
    // render real content behind an unhealthy dot.
    if (p === '/health')
      return json(res, 503, { status: 'stub: deliberately unhealthy' });
    return json(res, 404, {
      error: 'stub: no route for ' + req.method + ' ' + p,
    });
  });

  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
  const port = (server.address() as { port: number }).port;
  return { url: `http://127.0.0.1:${port}`, server };
}
