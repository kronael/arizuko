import { existsSync, statSync } from 'node:fs';
import { join } from 'node:path';

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
  server: { allowedHosts: true },
  plugins: [trailingSlash()],
};
