import { ALLOW, type ToolEvent, type Verdict } from './policy.js';
import { submitStatus, type SubmitStatusPayload } from './mcp.js';

export function todoStatus(
  turnID: string,
  source: 'claude' | 'codex',
  send: (p: SubmitStatusPayload) => Promise<void> = submitStatus,
): (ev: ToolEvent) => Promise<Verdict> {
  return async (snapshot) => {
    if (turnID) await send({ turn_id: turnID, source, snapshot });
    return ALLOW;
  };
}
