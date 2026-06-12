import { existsSync, statSync } from 'node:fs';
import { join } from 'node:path';

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
    // mount and inotify doesn't cross it. But polling the WHOLE cwd is the cost —
    // krons /web carries node_modules (214 dirs) so vited burned ~21% CPU idle
    // vs <1% on clean instances. Ignore non-served trees + halve the rate.
    watch: {
      usePolling: true,
      interval: 1000,
      ignored: ['**/node_modules/**', '**/.git/**', '**/.vite/**', '**/tmp/**'],
    },
    // Defense-in-depth: vited is internal-only (proxyd rewrites all paths to
    // /pub) but pin fs serving and never serve secrets even if reached directly.
    fs: { strict: true, deny: ['**/.env', '**/.env.*', '**/*.db', '**/.git/**'] },
    hmr: { clientPort: 443, protocol: 'wss' },
  },
  plugins: [pubFallback(), trailingSlash()],
};
