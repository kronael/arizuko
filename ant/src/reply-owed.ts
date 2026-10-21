import { checkTurn } from './mcp.js';
import { ALLOW, type ToolEvent } from './policy.js';

export function replyOwed(turnID: string, check = checkTurn) {
  return async function (event: ToolEvent) {
    if (!turnID || event.stop_hook_active) return ALLOW;
    const { nudge } = await check(turnID);
    return nudge ? { kind: 'deny' as const, reason: nudge } : ALLOW;
  };
}
