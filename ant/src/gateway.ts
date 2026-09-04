// gateway.ts — the one ant reader of the model-gateway declaration
// (core.Config.ModelGatewayURL/Key, emitted by container/runner.go as
// MODEL_GATEWAY_URL/KEY). A gateway holds the real model credential, meters
// and logs; the container presents only the virtual key. Each harness
// translates the pair into its own shape — env for claude, -c overrides for
// codex, registerProvider for pi — so this file names the env vars once and
// nothing else. Unset URL = no gateway = every harness unchanged.
//
// Zero imports on purpose: pi loads pi-extension.js in its own process.

export interface ModelGateway {
  url: string;
  key: string;
}

export const GATEWAY_KEY_ENV = 'MODEL_GATEWAY_KEY';

export function modelGateway(env: Record<string, string | undefined>): ModelGateway | undefined {
  const url = env.MODEL_GATEWAY_URL;
  if (!url) return undefined;
  // Trailing slashes off, so `${url}/v1` never yields `//v1`.
  return { url: url.replace(/\/+$/, ''), key: env[GATEWAY_KEY_ENV] ?? '' };
}
