import { expect, test } from 'bun:test';
import fs from 'node:fs';
import path from 'node:path';

const SKILLS = path.join(import.meta.dir, '..', 'skills');

function markdownFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...markdownFiles(p));
    else if (entry.name.endsWith('.md')) out.push(p);
  }
  return out;
}

// A CAPTURED tool result gets parsed, and there are two silent ways to get it
// wrong. Plain `tools-call` prints a human banner and EXITS 0, so a `||` guard
// never fires and the parser is handed prose. `--json` prints the MCP
// CallToolResult envelope, and ipc's toolJSON answers NewToolResultText, so the
// payload is a JSON STRING in .content[0].text with no structuredContent. Both
// end the same way: jq reads null, the release broadcast decides there is no
// #announce route, and exits 0 having announced nothing (BUGS L332).
//
// The rule is therefore about the CAPTURE, not about a pipe on the same line —
// the original defect put the jq two lines below the assignment.
test('a captured tool result passes --json and opens the envelope', () => {
  const offenders: string[] = [];
  for (const file of markdownFiles(SKILLS)) {
    fs.readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        if (!/\$\((?:[^)]*\s)?mcpc\b[^)]*tools-call/.test(line)) return;
        const opensEnvelope =
          line.includes('.content[0].text') || line.includes('structuredContent');
        if (line.includes('--json') && opensEnvelope) return;
        offenders.push(`${path.relative(SKILLS, file)}:${i + 1}: ${line.trim()}`);
      });
  }
  expect(offenders).toEqual([]);
});
