interface Resp {
  ok: boolean;
  token?: string;
  error?: string;
}

export class RouterClient {
  private token = '';

  constructor(
    private url: string,
    private secret: string,
  ) {}

  async register(name: string, listenURL: string): Promise<void> {
    const r = await this.post(
      '/v1/channels/register',
      {
        name,
        url: listenURL,
        jid_prefixes: ['whatsapp:'],
        capabilities: {
          send_text: true,
          send_file: true,
          typing: true,
          fwd: true,
          edit: true,
          like: true,
          delete: true,
        },
      },
      this.secret,
    );
    if (!r.ok) throw new Error(`register: ${r.error}`);
    this.token = r.token!;
  }

  async deregister(): Promise<void> {
    await this.post('/v1/channels/deregister', null, this.token).catch(
      () => {},
    );
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
    const r = await this.post('/v1/messages', msg, this.token);
    if (!r.ok) throw new Error(`deliver: ${r.error}`);
  }

  private async post(path: string, body: unknown, auth: string): Promise<Resp> {
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
