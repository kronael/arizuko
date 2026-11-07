import { ALLOW, type ToolEvent, type Verdict } from './policy.js';
import { submitStatus } from './mcp.js';
import { nudgeProgress } from './spool.js';

export function todoStatus(
  turnID: string,
  source: 'claude' | 'codex',
  send: typeof submitStatus = submitStatus,
): (ev: ToolEvent) => Promise<Verdict> {
  return async (snapshot) => {
    if (turnID && (await send({ turn_id: turnID, source, snapshot })).remind) nudgeProgress();
    return ALLOW;
  };
}
