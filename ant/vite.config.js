import { existsSync, statSync } from 'node:fs';
import { join } from 'node:path';

// Watch ONLY the served trees (pub/, priv/). vite cwd is /web; everything else
// there (node_modules — 214 dirs on krons — plus operator junk) is never served
// over HMR, so polling it is pure wasted CPU. Scoping the watcher took krons
// vited from ~21% CPU to idle while clean instances were already <1%.
const webRoot = process.cwd();
const skipSeg = new Set(['.git', 'node_modules', '.vite', '.cache']);
function unwatched(p) {
  if (p === webRoot) return false;
  const rel = p.startsWith(webRoot + '/') ? p.slice(webRoot.length + 1) : p;
  const segs = rel.split('/');
  // Only pub/ and priv/ are served; never descend VCS/dep dirs even inside them
  // (a krons agent published a 16k-file tree + a .git repo under pub/ — polling
  // it burned CPU).
  if (segs[0] !== 'pub' && segs[0] !== 'priv') return true;
  return segs.some((s) => skipSeg.has(s));
}

function pubFallback() {
  return {
    name: 'pub-fallback',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = req.url || '';
        const q = url.indexOf('?');
        const p = q >= 0 ? url.slice(0, q) : url;
        const qs = q >= 0 ? url.slice(q) : '';
        // A trailing-slash directory request must serve its index.html — vite's
        // MPA serving 404s nested dir requests (foo/index.html works, foo/ does
        // not). diskPrefix is where p lives under cwd; urlPrefix is prepended to
        // the rewritten request. Returns true once it rewrote req.url.
        const serveIndex = (diskPrefix, urlPrefix) => {
          if (!p.endsWith('/')) return false;
          if (!existsSync(join(process.cwd(), diskPrefix, p, 'index.html'))) return false;
          req.url = urlPrefix + p + 'index.html' + qs;
          return true;
        };
        if (p === '/' || p.startsWith('/pub/') || p.startsWith('/priv/') || p.startsWith('/@')) {
          serveIndex('', ''); // p already carries the /pub prefix; files at cwd+p
          return next();
        }
        const abs = join(process.cwd(), p);
        if (existsSync(abs)) return next();
        const pubAbs = join(process.cwd(), 'pub', p);
        if (!existsSync(pubAbs)) return next();
        if (statSync(pubAbs).isDirectory() && !p.endsWith('/')) {
          res.statusCode = 301;
          res.setHeader('Location', p + '/' + qs);
          res.end();
          return;
        }
        if (!serveIndex('pub', '/pub')) req.url = '/pub' + url;
        next();
      });
    },
  };
}

function trailingSlash() {
  return {
    name: 'trailing-slash',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = req.url || '';
        const q = url.indexOf('?');
        const p = q >= 0 ? url.slice(0, q) : url;
        const qs = q >= 0 ? url.slice(q) : '';
        if (p.endsWith('/') || p.includes('.') || p.startsWith('/@')) {
          return next();
        }
        const abs = join(process.cwd(), p);
        if (existsSync(abs) && statSync(abs).isDirectory()) {
          res.statusCode = 301;
          res.setHeader('Location', p + '/' + qs);
          res.end();
          return;
        }
        next();
      });
    },
  };
}

export default {
  appType: 'mpa',
  server: {
    allowedHosts: true,
    // Polling is required: agents in OTHER containers write web/pub via a bind
    // mount and inotify doesn't cross it. Scope it to served trees (unwatched)
    // and halve the rate so a junk-filled /web doesn't burn CPU.
    watch: {
      usePolling: true,
      interval: 2000,
      ignored: unwatched,
    },
    // Defense-in-depth: vited is internal-only (proxyd rewrites all paths to
    // /pub) but pin fs serving and never serve secrets even if reached directly.
    fs: { strict: true, deny: ['**/.env', '**/.env.*', '**/*.db', '**/.git/**'] },
    hmr: { clientPort: 443, protocol: 'wss' },
  },
  plugins: [pubFallback(), trailingSlash()],
};
