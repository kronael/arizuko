// pi-extension.ts — the pi renderer (spec 6/16). pi's hooks are in-process
// TypeScript and it loads an extension by explicit path (`-e`), which keeps
// working under `--no-extensions`. So this file imports the same binding table
// claude and codex render; the only translation is pi's field names.

import os from 'os';
import path from 'path';
import { PolicyContext, ToolEvent, Verdict, bindings, runRows, runnableBy } from './policy.js';
import { GATEWAY_KEY_ENV, modelGateway } from './gateway.js';

// PI_HONOURS: pi can refuse a call ({block, reason}) and can rewrite a tool
// RESULT, which is how an injection reaches the model. It has no rewrite of a
// tool's INPUT, so that row is refused rather than silently skipped.
const PI_HONOURS: Array<Verdict['kind']> = ['deny', 'inject'];

// The two pi events the table binds to, in pi's own shape (docs/extensions.md,
// pi 0.51.6).
interface PiToolCallEvent {
  toolName: string;
  toolCallId: string;
  input: Record<string, unknown>;
}

interface PiToolResultEvent extends PiToolCallEvent {
  content?: unknown;
  isError?: boolean;
}

// PiProviderConfig is the override shape of pi.registerProvider (pi 0.51.6
// docs/extensions.md): baseUrl alone re-points an existing provider and keeps
// its models; apiKey is the NAME of an env var pi resolves at call time.
interface PiProviderConfig {
  baseUrl: string;
  apiKey?: string;
}

interface PiApi {
  registerProvider(name: string, config: PiProviderConfig): void;
  on(event: 'tool_call', fn: (ev: PiToolCallEvent) => Promise<{ block: boolean; reason: string } | undefined>): void;
  on(event: 'tool_result', fn: (ev: PiToolResultEvent) => Promise<{ content: unknown[] } | undefined>): void;
}

// canonicalToolName maps pi's names onto the ones claude and codex share: pi
// says bash/write/edit, a pure case difference. It happens HERE because the
// policy bodies compare canonical names — skillguard reads `toolName === 'Write'`
// to find the fragment a write carries, so a lower-case name reached the guard
// with nothing to scan and passed.
export function canonicalToolName(name: string): string {
  return name ? name[0].toUpperCase() + name.slice(1) : name;
}

// canonicalInput maps pi's input keys onto the canonical ones. pi's write is
// {path, content} and its edit is {path, oldText, newText}, so a guard reading
// `file_path` got '' and ALLOWED every pi write — a gate that silently did
// nothing, on the tool the skill-guard exists for. pi also accepts paths
// relative to the session cwd, and the guard's path check needs them absolute.
export function canonicalInput(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  const abs = (p: unknown) => (typeof p === 'string' && p !== '' ? path.resolve(p) : '');
  if (toolName === 'Write' || toolName === 'Read') {
    const { path: p, ...rest } = input;
    return { ...rest, file_path: abs(p) };
  }
  if (toolName === 'Edit') {
    const { path: p, oldText, newText, ...rest } = input;
    return { ...rest, file_path: abs(p), old_string: oldText, new_string: newText };
  }
  return input;
}

function toToolEvent(ev: PiToolCallEvent, event: ToolEvent['hook_event_name'], response?: unknown): ToolEvent {
  const toolName = canonicalToolName(ev.toolName);
  return {
    hook_event_name: event,
    tool_name: toolName,
    tool_input: canonicalInput(toolName, ev.input ?? {}),
    tool_use_id: ev.toolCallId,
    // pi carries no session id on a tool event. The tool log keys on it only to
    // group lines, so an empty value costs grouping, never a refusal.
    session_id: '',
    tool_response: response,
  };
}

// registerWith takes the context explicitly. The ambient os.homedir() read
// happens in the default export and nowhere else: skill-rescan RENAMES files
// under <home>/.claude/skills.
export function registerWith(pi: PiApi, ctx: PolicyContext): void {
  const rows = runnableBy(bindings(ctx), PI_HONOURS, (m) => console.error(`[ant:pi] ${m}`));

  pi.on('tool_call', async (ev) => {
    const verdict = await runRows(rows, 'PreToolUse', toToolEvent(ev, 'PreToolUse'));
    return verdict.kind === 'deny' ? { block: true, reason: verdict.reason } : undefined;
  });

  pi.on('tool_result', async (ev) => {
    const verdict = await runRows(rows, 'PostToolUse', toToolEvent(ev, 'PostToolUse', ev.content));
    if (verdict.kind !== 'inject') return undefined;
    // pi's only channel back into the turn is the tool result itself. ev.content is
    // pi's (TextContent|ImageContent)[] and is kept verbatim — stringifying it
    // printed "[object Object]" in place of the real tool output.
    const existing = Array.isArray(ev.content) ? ev.content : [];
    return { content: [...existing, { type: 'text', text: verdict.context }] };
  });
}

// registerGateway is pi's translation of the gateway pair (gateway.ts). pi has
// no env-var base URL: each built-in provider carries its own, so the two whose
// wire formats a gateway speaks are re-pointed at it — anthropic sends the
// Anthropic messages format to the bare URL (pi-ai appends /v1/messages),
// openai sends the Responses format to `<url>/v1` (pi-ai appends /responses).
// Which one a turn uses follows from the model id, as today. The key is a
// fallback: pi consults it only when no real credential for that provider is
// present, which is exactly the gateway deployment. Read from pi 0.51.6's
// model-registry.js, not measured against a live gateway.
export function registerGateway(pi: Pick<PiApi, 'registerProvider'>, env: Record<string, string | undefined>): void {
  const gw = modelGateway(env);
  if (!gw) return;
  const auth = gw.key ? { apiKey: GATEWAY_KEY_ENV } : {};
  pi.registerProvider('anthropic', { baseUrl: gw.url, ...auth });
  pi.registerProvider('openai', { baseUrl: `${gw.url}/v1`, ...auth });
}

// turnID is empty: pi has no TodoWrite, so nothing here needs the turn id.
export default function (pi: PiApi): void {
  registerWith(pi, { home: os.homedir(), turnID: '' });
  registerGateway(pi, process.env);
}
