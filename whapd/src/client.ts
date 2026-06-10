interface Resp {
  ok: boolean;
  token?: string;
  error?: string;
}

// Bearer returns the service:<adapter> ES256 token to present on routd calls
// (HMAC retire step 6). Async because the token source refreshes before expiry.
// Returns '' in local dev (no AUTHD_URL) → routd's no-JWKS gate is open.
export type Bearer = () => Promise<string>;

export class RouterClient {
  constructor(
    private url: string,
    private bearer: Bearer,
  ) {}

  async register(name: string, listenURL: string): Promise<void> {
    const r = await this.post('/v1/channels/register', {
      name,
      url: listenURL,
      jid_prefixes: ['whatsapp:'],
      capabilities: {
        send_text: true,
        send_file: true,
        send_voice: true,
        typing: true,
        fwd: true,
        edit: true,
        like: true,
        delete: true,
      },
    });
    if (!r.ok) throw new Error(`register: ${r.error}`);
  }

  async deregister(): Promise<void> {
    await this.post('/v1/channels/deregister', null).catch(() => {});
  }

  async sendMessage(msg: {
    id: string;
    chat_jid: string;
    sender: string;
    sender_name: string;
    content: string;
    timestamp: number;
    reply_to?: string;
    reply_to_text?: string;
    reply_to_sender?: string;
    verb?: string;
    reaction?: string;
    is_group?: boolean;
  }): Promise<void> {
    const r = await this.post('/v1/messages', msg);
    if (!r.ok) throw new Error(`deliver: ${r.error}`);
  }

  private async post(path: string, body: unknown): Promise<Resp> {
    const auth = await this.bearer();
    const r = await fetch(this.url + path, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(auth ? { Authorization: `Bearer ${auth}` } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!r.ok) throw new Error(`router ${path}: status ${r.status}`);
    return r.json() as Promise<Resp>;
  }
}
