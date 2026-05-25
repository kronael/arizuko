// Per-tool-call logging: emits one [ant] [tool] {json} line to stderr per
// PreToolUse and PostToolUse event. Captured by container/runner.go which
// scans the agent container's stderr for [ant] prefix and forwards to slog.
//
// Hooks must never block or throw — swallow all errors. Secrets are dropped
// at the args_summary boundary: Bash.command is truncated to 200 chars,
// Edit/Write/Read use file_path only, content bodies are never logged.

import { HookCallback, PreToolUseHookInput, PostToolUseHookInput } from '@anthropic-ai/claude-agent-sdk';

const ARG_SUMMARY_MAX = 200;

// tool_use_id → epoch ms at PreToolUse, for PostToolUse duration calc.
const startTimes = new Map<string, number>();
// Cap the map to prevent unbounded growth if a tool_use_id never gets a Post event.
const START_TIMES_MAX = 1024;

export interface ToolLogLine {
  ant: 'tool';
  phase: 'pre' | 'post';
  turn_id: string;
  tool: string;
  args_summary: string;
  ts: string;
  duration_ms?: number;
  outcome?: 'ok' | 'error';
  error?: string;
}

export function summarizeArgs(toolName: string, toolInput: unknown): string {
  if (!toolInput || typeof toolInput !== 'object') return '';
  const ti = toolInput as Record<string, unknown>;
  switch (toolName) {
    case 'Bash': {
      const cmd = typeof ti.command === 'string' ? ti.command : '';
      return cmd.slice(0, ARG_SUMMARY_MAX);
    }
    case 'Read':
    case 'Write':
    case 'Edit':
    case 'NotebookEdit': {
      const fp = typeof ti.file_path === 'string' ? ti.file_path : '';
      return fp.slice(0, ARG_SUMMARY_MAX);
    }
    case 'Glob':
    case 'Grep': {
      const pat = typeof ti.pattern === 'string' ? ti.pattern : '';
      return pat.slice(0, ARG_SUMMARY_MAX);
    }
    case 'WebFetch':
    case 'WebSearch': {
      const q = typeof ti.url === 'string' ? ti.url
        : typeof ti.query === 'string' ? ti.query : '';
      return q.slice(0, ARG_SUMMARY_MAX);
    }
    case 'Task': {
      const desc = typeof ti.description === 'string' ? ti.description : '';
      return desc.slice(0, ARG_SUMMARY_MAX);
    }
    default:
      return '';
  }
}

export function buildToolLogLine(
  phase: 'pre' | 'post',
  toolName: string,
  toolInput: unknown,
  sessionId: string,
  toolUseId: string,
  now: number,
  toolResponse?: unknown,
): ToolLogLine {
  const out: ToolLogLine = {
    ant: 'tool',
    phase,
    turn_id: sessionId,
    tool: toolName,
    args_summary: summarizeArgs(toolName, toolInput),
    ts: new Date(now).toISOString(),
  };
  if (phase === 'post') {
    const start = startTimes.get(toolUseId);
    if (start !== undefined) {
      out.duration_ms = Math.max(0, now - start);
      startTimes.delete(toolUseId);
    }
    const { outcome, error } = classifyResponse(toolResponse);
    out.outcome = outcome;
    if (error) out.error = error;
  }
  return out;
}

function classifyResponse(resp: unknown): { outcome: 'ok' | 'error'; error?: string } {
  if (!resp || typeof resp !== 'object') return { outcome: 'ok' };
  const r = resp as Record<string, unknown>;
  if (r.is_error === true || r.isError === true) {
    const msg = typeof r.error === 'string' ? r.error
      : typeof r.message === 'string' ? r.message : 'error';
    return { outcome: 'error', error: msg.slice(0, ARG_SUMMARY_MAX) };
  }
  return { outcome: 'ok' };
}

export function recordStart(toolUseId: string, now: number): void {
  if (!toolUseId) return;
  if (startTimes.size >= START_TIMES_MAX) {
    // Drop the oldest entry (insertion order).
    const oldest = startTimes.keys().next().value;
    if (oldest !== undefined) startTimes.delete(oldest);
  }
  startTimes.set(toolUseId, now);
}

// For tests only.
export function _resetStartTimes(): void {
  startTimes.clear();
}

function emit(line: ToolLogLine): void {
  try {
    console.error(`[ant] [tool] ${JSON.stringify(line)}`);
  } catch {
    // Never let logging crash a tool call.
  }
}

export function createToolLogPreHook(): HookCallback {
  return async (input, _toolUseId, _context) => {
    try {
      const pi = input as PreToolUseHookInput;
      const now = Date.now();
      recordStart(pi.tool_use_id, now);
      emit(buildToolLogLine('pre', pi.tool_name, pi.tool_input, pi.session_id, pi.tool_use_id, now));
    } catch {
      // swallow
    }
    return {};
  };
}

export function createToolLogPostHook(): HookCallback {
  return async (input, _toolUseId, _context) => {
    try {
      const pi = input as PostToolUseHookInput;
      const now = Date.now();
      emit(buildToolLogLine('post', pi.tool_name, pi.tool_input, pi.session_id, pi.tool_use_id, now, pi.tool_response));
    } catch {
      // swallow
    }
    return {};
  };
}
