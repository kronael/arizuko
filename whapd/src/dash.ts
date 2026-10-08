// dash.ts is whapd's operator dashboard at GET /dash/whapd/ (spec 6/31).
//
// The Go adapters get this page for free: chanlib/dash.go is one renderer
// mounted inside NewAdapterMux, so seven adapters share it with no per-adapter
// code. whapd cannot import chanlib, so it re-implements the contract here —
// and 6/31 says in as many words that this duplication is the cost the written
// contract exists to make payable. Keep this file in step with chanlib/dash.go;
// they are two implementations of one spec, not two designs.
//
// Two deltas from the Go renderer, both forced rather than chosen:
//
//   1. The capability strip is DECLARED here, not probed. Go's isStubVerb calls
//      the verb and watches for Unsupported; whapd's verbs are switch arms, not
//      values, so there is nothing to call. supportedVerbs is therefore a claim
//      and must be edited when a case is added — the Go side cannot drift this
//      way and this one can.
//   2. The theme is inlined. `theme.Page` is a Go package; the palette below is
//      the small subset this page uses.

import type * as http from 'node:http';
import { verifyProxyd, type RoutdVerifier } from './auth.js';

// STALE_SECONDS must equal server.ts's STALE_THRESHOLD_SECONDS, or the page and
// /health disagree about when the adapter went quiet — the exact drift the Go
// renderer avoids by reading one table.
const STALE_SECONDS = 5 * 60;

// supportedVerbs are the tools whapd's switch actually routes. Declared, not
// probed; see the header.
const supportedVerbs = [
  'send',
  'send-file',
  'send-voice',
  'typing',
  'like',
  'delete',
  'edit',
  'forward',
];

/** isOperator reads the proxyd-stamped scopes. `**` is the operator scope.
 *
 * This is HALF the gate. The header is only trustworthy behind proxyd's own
 * service bearer, which handleDash verifies — reading the scope alone admits
 * anyone who can reach the port (BUGS `L400`). */
export function isOperator(req: http.IncomingMessage): boolean {
  const hdr = req.headers['x-user-groups'];
  if (typeof hdr !== 'string' || hdr === '') return false;
  try {
    const groups = JSON.parse(hdr);
    return Array.isArray(groups) && groups.includes('**');
  } catch {
    // A malformed header is NO scopes, never all scopes.
    return false;
  }
}

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

/** adapterStatus is the three-state health the contract requires. */
export function adapterStatus(
  connected: boolean,
  lastInboundAt: number,
): string {
  if (!connected) return 'disconnected';
  const staleSec = Math.floor(Date.now() / 1000) - lastInboundAt;
  // `stale` is a live process whose platform link has gone quiet — the
  // "healthchecks green but the agent doesn't reply" case. It is not an outage
  // and must not read as one.
  if (lastInboundAt > 0 && staleSec > STALE_SECONDS) return 'stale';
  return 'ok';
}

/** DashSection is one per-adapter delta panel — the TS twin of
 * chanlib.DashSection (spec 6/32). Label/value rows, not HTML, for the same
 * reason: handing every adapter markup is how one grammar becomes ten layouts.
 */
export interface DashSection {
  title: string;
  rows: [string, string][];
}

function renderSection(sec: DashSection): string {
  const title = `<h2>${esc(sec.title)}</h2>`;
  if (sec.rows.length === 0) {
    // An empty section still says so: "not paired" is an answer, and omitting
    // the panel looks identical to the feature not existing.
    return `${title}<p class="dim">Nothing to show.</p>`;
  }
  const rows = sec.rows
    .map(
      ([k, v]) => `<tr><td>${esc(k)}</td><td><code>${esc(v)}</code></td></tr>`,
    )
    .join('');
  return `${title}<table><tbody>${rows}</tbody></table>`;
}

/** renderDash returns the page body for the given live state. */
export function renderDash(
  name: string,
  prefixes: string[],
  connected: boolean,
  lastInboundAt: number,
  sections: DashSection[] = [],
): string {
  const status = adapterStatus(connected, lastInboundAt);
  // A stale adapter WARNS: the process is up, so the operator's next step is
  // the platform link, not a restart.
  const tone =
    status === 'ok' ? '#3fb950' : status === 'stale' ? '#d4af37' : '#f85149';
  const inbound =
    lastInboundAt > 0 ? new Date(lastInboundAt * 1000).toISOString() : 'never';
  const caps = supportedVerbs.map((v) => `<span>${esc(v)}</span>`).join(' ');

  return `<!doctype html><meta charset="utf-8"><title>${esc(name)}</title>
<style>
body{background:#0a0e1a;color:#f0f0ea;font:14px/1.5 ui-monospace,monospace;margin:2rem}
a{color:#4a90e2}h1{color:#d4af37;margin:.2rem 0}
.dim{color:#a0a8b8}.tiles{display:flex;gap:1rem;margin:1rem 0}
.tile{background:#151b28;padding:1rem;border-radius:6px;min-width:9rem}
.n{font-size:1.4rem}
</style>
<div class="dim"><a href="/dash/services/">arizuko hub</a> &rsaquo; ${esc(name)}</div>
<h1>${esc(name)}</h1>
<div class="tiles">
  <div class="tile"><div class="n" style="color:${tone}">${esc(status)}</div><div class="dim">status</div></div>
  <div class="tile"><div class="n">${esc(inbound)}</div><div class="dim">last inbound</div></div>
</div>
<p>JID prefixes: <code>${esc(prefixes.join(', '))}</code></p>
<h2>Capabilities</h2>
<p class="dim">Declared, not probed — whapd's verbs are switch arms, so there is
nothing to call. The Go adapters probe theirs.</p>
<p>${caps}</p>
${sections.map(renderSection).join('\n')}`;
}

/**
 * handleDash serves the page when the request is for it, and reports whether it
 * did. Returns false so the caller falls through to its normal routing.
 *
 * `enforce` gates on the stamped operator scope. An adapter verifies no user
 * tokens, and proxyd's `user` route authenticates a person rather than an
 * operator, so without this the page is visible to anyone signed in. Deployed
 * means AUTHD_URL is set; unset is local dev, where nothing stamps headers.
 */
export async function handleDash(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  opts: {
    name: string;
    prefixes: string[];
    enforce: boolean;
    /** verifier proves the X-User-* headers are proxyd's own (BUGS `L400`). */
    verifier: RoutdVerifier;
    isConnected: () => boolean;
    lastInboundAt: () => number;
    /** sections are this adapter's deltas (6/32); omitted means none. */
    sections?: () => DashSection[];
  },
): Promise<boolean> {
  const path = `/dash/${opts.name}/`;
  if (
    req.method !== 'GET' ||
    (req.url !== path && req.url !== path.slice(0, -1))
  ) {
    return false;
  }
  if (
    opts.enforce &&
    !((await verifyProxyd(req, opts.verifier)) && isOperator(req))
  ) {
    res.writeHead(403, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end('<p>Operator access required.</p>');
    return true;
  }
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
  res.end(
    renderDash(
      opts.name,
      opts.prefixes,
      opts.isConnected(),
      opts.lastInboundAt(),
      opts.sections ? opts.sections() : [],
    ),
  );
  return true;
}
