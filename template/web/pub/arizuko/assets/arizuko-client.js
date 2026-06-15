/**
 * arizuko-client — vanilla-JS SDK for the slink REST surface.
 *
 * Usage:
 *   <script src="/assets/arizuko-client.js"></script>
 *   <script>
 *     const slink = await Arizuko.connect("<token>");
 *     const turn  = await slink.send("hello");
 *     const close = slink.stream(turn.turn_id, {
 *       onMessage: m => console.log("msg", m),
 *       onStatus:  s => console.log("status", s),
 *       onDone:    e => console.log("done", e),
 *     });
 *   </script>
 *
 * Protocol: specs/1/W-slink.md (round handle)
 * Hosting:  specs/1/Z2-slink-sdk.md (SDK)
 *
 * No build step. No dependencies. Browser-only. ES2017 baseline.
 *
 * @typedef {Object} Message
 * @property {string} id
 * @property {string} content
 * @property {string} created_at
 * @property {string=} sender
 *
 * @typedef {Object} TurnHandle
 * @property {Message} user
 * @property {string}  turn_id
 * @property {string}  status
 *
 * @typedef {Object} Frame
 * @property {string} id
 * @property {string} content
 * @property {string} created_at
 * @property {("message"|"status")} kind
 *
 * @typedef {Object} Snapshot
 * @property {string}  turn_id
 * @property {string}  status
 * @property {Frame[]} frames
 * @property {string=} last_frame_id
 *
 * @typedef {Object} StatusEnvelope
 * @property {string}  turn_id
 * @property {string}  status
 * @property {number=} frames_count
 * @property {string=} last_frame_id
 *
 * @typedef {Object} StreamHandlers
 * @property {(f:Frame)=>void=} onMessage
 * @property {(f:Frame)=>void=} onStatus
 * @property {(e:{turn_id:string,status:string,error?:string})=>void=} onDone
 * @property {(err:Error|Event)=>void=} onError
 */
(function (global) {
  'use strict';

  const DEFAULT_BASE = '';

  function joinURL(base, path) {
    if (!base) return path;
    if (base.endsWith('/')) base = base.slice(0, -1);
    return base + path;
  }

  async function fetchJSON(url, init) {
    const r = await fetch(url, init);
    if (!r.ok) {
      const body = await r.text().catch(() => '');
      throw new Error(
        'arizuko: ' +
          r.status +
          ' ' +
          r.statusText +
          (body ? ' — ' + body : ''),
      );
    }
    return r.json();
  }

  /**
   * Slink — bound to a single token. Construct via Arizuko.connect().
   */
  class Slink {
    constructor(config, baseURL) {
      this.token = config.token;
      this.folder = config.folder;
      this.name = config.name;
      this.endpoints = config.endpoints || {};
      this._base = baseURL || DEFAULT_BASE;
      this._config = config;
    }

    _url(path) {
      return joinURL(this._base, path);
    }

    /**
     * Post a fresh user message to the slink. Returns the turn handle.
     * @param {string} content
     * @param {{topic?: string}=} opts
     * @returns {Promise<TurnHandle>}
     */
    send(content, opts) {
      opts = opts || {};
      const body = { content };
      if (opts.topic) body.topic = opts.topic;
      return fetchJSON(this._url('/slink/' + encodeURIComponent(this.token)), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json',
        },
        body: JSON.stringify(body),
      });
    }

    /**
     * Cheap status poll for a round.
     * @param {string} turnId
     * @returns {Promise<StatusEnvelope>}
     */
    status(turnId) {
      return fetchJSON(
        this._url(
          '/slink/' +
            encodeURIComponent(this.token) +
            '/' +
            encodeURIComponent(turnId) +
            '/status',
        ),
      );
    }

    /**
     * Snapshot a round's frames. Use opts.after to page forward.
     * @param {string} turnId
     * @param {{after?: string}=} opts
     * @returns {Promise<Snapshot>}
     */
    snapshot(turnId, opts) {
      const q =
        opts && opts.after ? '?after=' + encodeURIComponent(opts.after) : '';
      return fetchJSON(
        this._url(
          '/slink/' +
            encodeURIComponent(this.token) +
            '/' +
            encodeURIComponent(turnId) +
            q,
        ),
      );
    }

    /**
     * Open the SSE stream for a round. Returns a close fn.
     * The stream auto-closes on `round_done`; the close fn is for early abort.
     * @param {string} turnId
     * @param {StreamHandlers} handlers
     * @returns {() => void}
     */
    stream(turnId, handlers) {
      handlers = handlers || {};
      const url = this._url(
        '/slink/' +
          encodeURIComponent(this.token) +
          '/' +
          encodeURIComponent(turnId) +
          '/sse',
      );
      const es = new EventSource(url);
      let closed = false;
      const close = () => {
        if (closed) return;
        closed = true;
        es.close();
      };
      es.addEventListener('message', (e) => {
        if (!handlers.onMessage) return;
        try {
          handlers.onMessage(JSON.parse(e.data));
        } catch (err) {
          handlers.onError && handlers.onError(err);
        }
      });
      es.addEventListener('status', (e) => {
        if (!handlers.onStatus) return;
        try {
          handlers.onStatus(JSON.parse(e.data));
        } catch (err) {
          handlers.onError && handlers.onError(err);
        }
      });
      es.addEventListener('round_done', (e) => {
        let env = {};
        try {
          env = JSON.parse(e.data);
        } catch (_) {}
        handlers.onDone && handlers.onDone(env);
        close();
      });
      es.onerror = (err) => {
        if (closed) return;
        handlers.onError && handlers.onError(err);
      };
      return close;
    }
  }

  const Arizuko = {
    /**
     * Connect to a slink token. Fetches /config and returns a Slink.
     * @param {string} token
     * @param {{baseURL?: string}=} opts
     * @returns {Promise<Slink>}
     */
    async connect(token, opts) {
      opts = opts || {};
      const base = opts.baseURL || DEFAULT_BASE;
      const cfg = await fetchJSON(
        joinURL(base, '/slink/' + encodeURIComponent(token) + '/config'),
      );
      return new Slink(cfg, base);
    },
    Slink,
    version: '1',
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = Arizuko;
  global.Arizuko = Arizuko;
})(typeof window !== 'undefined' ? window : globalThis);
