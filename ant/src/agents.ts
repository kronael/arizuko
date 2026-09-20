// agents.ts declares the subagent types the shipped skills dispatch to. The
// skills were vendored from the host toolkit; the definitions beside them were
// not, so every `subagent_type` in ant/skills named a type the container did
// not have and the SDK answered "Agent type 'sonnet' not found". The dispatch
// then ran at the parent's model and effort instead of the cheaper one the
// skill promised (BUGS L77).
//
// Two shapes, and the difference is deliberate:
//
//   - A TIER agent is nothing but a model and an effort. Its whole purpose is
//     the pin: effort is INHERITED when it is not set, so `model: "haiku"`
//     alone still runs at the parent's effort, which is what the skills warn
//     about.
//   - A ROLE agent's instructions are its skill, which already ships in
//     ant/skills. It names that skill rather than restating it — a copy here
//     would be a second declaration of the same text and the two would drift.

import type { AgentDefinition } from '@anthropic-ai/claude-agent-sdk';

// tier declares a model/effort pin. The model strings are the ones the host
// definitions use, unchanged.
function tier(model: string, effort: AgentDefinition['effort'], description: string): AgentDefinition {
  return {
    description,
    model,
    effort,
    prompt: 'Carry out the delegated task and report what you did, with the evidence for it.',
  };
}

// role binds an agent type to the skill that holds its instructions.
function role(skill: string, description: string, tools?: string[]): AgentDefinition {
  return {
    description,
    skills: [skill],
    tools,
    prompt: `Follow the \`${skill}\` skill, which carries your instructions, and report what you did with the evidence for it.`,
  };
}

export const AGENTS: Record<string, AgentDefinition> = {
  fable: tier('fable', 'xhigh',
    'Fable 5 at xhigh effort — for hardest reasoning, architecture, and long-horizon agentic work.'),
  opus: tier('claude-opus-5', 'xhigh',
    'Opus 5 at xhigh effort — complex reasoning, design decisions, and multi-file architecture work.'),
  sonnet: tier('claude-sonnet-5', 'high',
    'Sonnet 5 at high effort — thinking-enabled workhorse for coding, exploration, pre-review, flagging, and template-heavy tasks.'),
  haiku: tier('haiku', 'low',
    'Haiku — fast, cheap model for mechanical tasks (renaming, single-file edits, simple lookups).'),

  distill: role('distill', 'Extract essence through recursive summarization using 5/3 approach',
    ['Read', 'Glob', 'Grep', 'Write', 'Edit', 'TodoWrite', 'Task', 'WebSearch', 'WebFetch']),
  improve: role('improve', 'Improve, enhance, fix, cleanup, refactor, optimize, polish code quality.',
    ['Read', 'Write', 'Edit', 'Glob', 'Grep', 'Bash', 'WebSearch', 'WebFetch']),
  learn: role('learn', 'Learn, extract patterns, analyze history, create or update skills, evaluate sessions for memory-worthy content.',
    ['Read', 'Write', 'Edit', 'Glob', 'Grep', 'Bash']),
  readme: role('readme', 'Update readme, docs, documentation, architecture files.',
    ['Read', 'Write', 'Edit', 'Glob', 'Grep', 'Bash', 'WebSearch', 'WebFetch']),
  refine: role('refine', 'Refine, finalize, finish, complete, wrap up, review code, ship it.',
    ['Read', 'Write', 'Edit', 'Glob', 'Grep', 'Bash', 'WebSearch', 'WebFetch']),
  visual: role('visual', 'Visual, UI, styling, CSS, layout, design, render improvements.',
    ['Read', 'Write', 'Edit', 'Glob', 'Grep', 'Bash', 'Task', 'WebSearch', 'WebFetch']),
};
