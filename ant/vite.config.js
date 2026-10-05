import { existsSync, statSync } from 'node:fs';
import { join } from 'node:path';

// Watch ONLY the served trees (pub/, priv/). vite cwd is /web; everything else
// there (node_modules — 214 dirs on krons — plus operator junk) is never served
// over HMR, so watching it is pure waste.
const webRoot = process.cwd();
const skipSeg = new Set(['.git', 'node_modules', '.vite', '.cache']);
function unwatched(p) {
  if (p === webRoot) return false;
  const rel = p.startsWith(webRoot + '/') ? p.slice(webRoot.length + 1) : p;
  const segs = rel.split('/');
  // Only pub/ and priv/ are served; never descend VCS/dep dirs even inside them
  // (a krons agent published a 16k-file tree + a .git repo under pub/ — every
  // directory costs an inotify watch).
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
        // Non-pub/priv path: ALWAYS rewrite under pub/ so vite resolves it to
        // /web/pub/<p> and 404s if absent — never serving a /web ROOT file
        // (vite.config, *.ts source, research dumps, node_modules). A bare
        // `next()` here would let vite's static handler serve the root file, so
        // every branch must leave req.url prefixed with /pub. Internal files
        // stay unreachable even if vited is hit directly (bypassing proxyd).
        const pubAbs = join(process.cwd(), 'pub', p);
        if (existsSync(pubAbs) && statSync(pubAbs).isDirectory() && !p.endsWith('/')) {
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
  // Vite's dep-optimization cache MUST live OUTSIDE the bind-mounted /web root.
  // Its default (`<root>/node_modules/.vite` or `<root>/.vite`) sat inside /web,
  // whose host uid ownership drifts to root across restarts/rebuilds — so vite
  // (USER node) hit `EACCES: unlink .../.vite/deps/_metadata.json` when clearing
  // it on a config change → crash-loop → /pub 502 (marinade + sloth, 2026-07-18,
  // twice). A container-local cache is container-owned + ephemeral, so the whole
  // failure class is gone by construction. (BUGS M4.)
  cacheDir: '/home/node/.vite-cache',
  server: {
    allowedHosts: true,
    // inotify sees a write from another container or from the host, because
    // the bind mount shares the host inode, so the watcher costs nothing until
    // a file changes. Polling krons' 48k files every 2 s held vited at 26% CPU
    // with no traffic (BUGS L391).
    watch: { ignored: unwatched },
    // Defense-in-depth: vited is internal-only (proxyd rewrites all paths to
    // /pub) but pin fs serving and never serve secrets even if reached directly.
    fs: { strict: true, deny: ['**/.env', '**/.env.*', '**/*.db', '**/.git/**'] },
    hmr: { clientPort: 443, protocol: 'wss' },
  },
  plugins: [pubFallback(), trailingSlash()],
};
