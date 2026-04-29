// Make Node's built-in fetch (undici) honor HTTPS_PROXY / HTTP_PROXY env vars.
// Loaded via NODE_OPTIONS=--require when egress isolation is on. No-op
// otherwise.
const proxy = process.env.HTTPS_PROXY || process.env.https_proxy
  || process.env.HTTP_PROXY || process.env.http_proxy;
if (proxy) {
  try {
    const { setGlobalDispatcher, ProxyAgent } = require('undici');
    setGlobalDispatcher(new ProxyAgent(proxy));
  } catch (e) {
    // undici not present (older Node, or pre-installed elsewhere) — fail
    // open: leave default fetch behavior. Real CLIs (curl, npm) honor env
    // vars natively.
  }
}
