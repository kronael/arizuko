// pi-extension.ts — the pi renderer (spec 6/16). pi's hooks are in-process
// TypeScript and it loads an extension by explicit path (`-e`), which keeps
// working under `--no-extensions`. So this file imports the same binding table
// claude and codex render; the only translation is pi's field names.

import os from 'os';
import fs from 'fs';
import { createEditToolDefinition, createWriteToolDefinition } from '@earendil-works/pi-coding-agent';
import path from 'path';
import { PolicyContext, ToolEvent, Verdict, bindings, matches, runRow, runRows, runnableBy } from './policy.js';
import { GATEWAY_KEY_ENV, modelGateway } from './gateway.js';

// Pi renders denial, input rewrites, and result injection.
const PI_HONOURS: Array<Verdict['kind']> = ['deny', 'inject', 'rewrite'];

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
// its models; apiKey references an env var pi resolves at call time.
interface PiProviderConfig {
  baseUrl: string;
  apiKey?: string;
}

interface PiApi {
  registerProvider(name: string, config: PiProviderConfig): void;
  registerTool(tool: ReturnType<typeof createEditToolDefinition> | ReturnType<typeof createWriteToolDefinition>): void;
  on(event: 'tool_call', fn: (ev: PiToolCallEvent) => Promise<{ block: boolean; reason: string } | undefined>): void;
  on(event: 'tool_result', fn: (ev: PiToolResultEvent) => Promise<{ content: unknown[] } | undefined>): void;
}

// canonicalToolName maps pi's names onto the ones claude and codex share: pi
// says bash/write/edit, a pure case difference. It happens HERE because the
// policy bodies compare canonical names — skillguard reads `toolName === 'Write'`
// to find the fragment a write carries, so a lower-case name reached the guard
// with nothing to scan and passed.
export function canonicalToolName(name: string): string {
  return name.startsWith('mcp__') ? name : name ? name[0].toUpperCase() + name.slice(1) : name;
}

// Translate native paths and edit fragments for the shared policy rows.
export function canonicalInput(toolName: string, input: Record<string, unknown>): Record<string, unknown> {
  const abs = (p: unknown) => (typeof p === 'string' && p !== '' ? path.resolve(p) : '');
  if (toolName === 'Write' || toolName === 'Read') {
    const { path: p, ...rest } = input;
    return { ...rest, file_path: abs(p) };
  }
  if (toolName === 'Edit') {
    const { path: p, edits, ...rest } = input;
    return { ...rest, file_path: abs(p), edits: Array.isArray(edits) ? edits.map(({ oldText, newText }) => ({ old_string: oldText, new_string: newText })) : edits };
  }
  return input;
}

function toToolEvent(ev: PiToolCallEvent, event: ToolEvent['hook_event_name'], response?: unknown): ToolEvent {
  const toolName = ev.toolName === 'edit' ? 'MultiEdit' : canonicalToolName(ev.toolName);
  return {
    hook_event_name: event,
    tool_name: toolName,
    tool_input: canonicalInput(ev.toolName === 'edit' ? 'Edit' : toolName, ev.input ?? {}),
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
  const rows = runnableBy(bindings(ctx).filter((row) => {
    if (row.event !== 'Stop') return true;
    console.error(`[ant:pi] policy ${row.id} not rendered: delivery uses submission`);
    return false;
  }), PI_HONOURS, (m) => console.error(`[ant:pi] ${m}`));

  const guard = rows.find((row) => row.id === 'skill-guard')!;
  const writeFile = async (file_path: string, content: string) => {
    const verdict = await runRow(guard, {
      hook_event_name: 'PreToolUse', tool_name: 'Write',
      tool_input: { file_path, content }, tool_use_id: '', session_id: '',
    });
    if (verdict.kind === 'deny') throw new Error(verdict.reason);
    await fs.promises.writeFile(file_path, content, 'utf8');
  };
  pi.registerTool(createEditToolDefinition(process.cwd(), { operations: {
    readFile: fs.promises.readFile, writeFile,
    access: (p) => fs.promises.access(p, fs.constants.R_OK | fs.constants.W_OK),
  } }));
  pi.registerTool(createWriteToolDefinition(process.cwd(), { operations: {
    writeFile, mkdir: async (p) => { await fs.promises.mkdir(p, { recursive: true }); },
  } }));

  pi.on('tool_call', async (ev) => {
    const event = toToolEvent(ev, 'PreToolUse');
    let denial: string | undefined;
    let rewrite: Record<string, unknown> | undefined;
    for (const row of rows) {
      if (row.event !== 'PreToolUse' || !matches(row, event.tool_name)) continue;
      if (row.id === 'skill-guard' && (ev.toolName === 'edit' || ev.toolName === 'write')) continue;
      const verdict = await runRow(row, event);
      if (verdict.kind === 'deny') denial ??= verdict.reason;
      if (verdict.kind === 'rewrite') rewrite = verdict.input;
    }
    if (denial) return { block: true, reason: denial };
    if (ev.toolName === 'bash' && rewrite && typeof rewrite.command === 'string') ev.input.command = rewrite.command;
    return undefined;
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
  const auth = gw.key ? { apiKey: `$${GATEWAY_KEY_ENV}` } : {};
  pi.registerProvider('anthropic', { baseUrl: gw.url, ...auth });
  pi.registerProvider('openai', { baseUrl: `${gw.url}/v1`, ...auth });
}

// turnID is empty: pi has no TodoWrite, so nothing here needs the turn id.
export default function (pi: PiApi): void {
  registerWith(pi, { home: os.homedir(), turnID: '' });
  registerGateway(pi, process.env);
}
