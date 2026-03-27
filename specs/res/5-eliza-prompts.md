---
status: draft
---

# ElizaOS Prompt Patterns — Reference

Extracted from eliza-atlas and eliza-plugin-evangelist. These are
the exact patterns that work in production for REDACTED Atlas.

## Character → System Prompt Assembly

ElizaOS `generateText()` (runtime.ts:2437-2461):

```
# About {name}
{bio items joined with space}

{system prompt verbatim}

Style:
- {style.all items}
- {style.chat items}
```

Kanipi `assembleCharacter()` (agent-runner/src/index.ts:66-106)
does the same but adds:

- Random topic: "{name} is currently thinking about {topic}."
- Random adjective: "{name} is {adjective}."
- Example conversations section (shuffled, max 5)

**Difference**: ElizaOS doesn't randomize or include topics/adjectives
in the system prompt — those go into the `{{providers}}` template
variable via composeState. Kanipi puts everything in one system prompt
block. Both approaches work. ElizaOS is more modular (providers inject
context per-message), kanipi is simpler (one-shot system prompt).

## Message Handler Template

ElizaOS uses `messageHandlerTemplate` (prompts.ts:35-100):

```xml
<task>Generate dialog and actions for the character {{agentName}}.</task>

<providers>
{{providers}}
</providers>

<instructions>
Write a thought and plan for {{agentName}} and decide what actions
to take. Also include the providers that {{agentName}} will use.

IMPORTANT ACTION ORDERING RULES:
- REPLY should come FIRST to acknowledge the user's request
- Follow-up actions execute after acknowledgment
- Use IGNORE when you should not respond at all

IMPORTANT RESEARCH_NEEDED USAGE:
- Use RESEARCH_NEEDED when knowledge is insufficient
- Knowledge confidence tiers from knowledgeContext provider:
  * High — answer directly
  * Medium — verify carefully; prefer RESEARCH_NEEDED for technical
  * Low / Very Low — use RESEARCH_NEEDED unless casual
- Never speculate — trigger research instead of guessing
</instructions>

<output>
<response>
  <thought>reasoning</thought>
  <actions>ACTION1,ACTION2</actions>
  <providers>PROVIDER1,PROVIDER2</providers>
  <text>response text</text>
</response>
</output>
```

**Kanipi equivalent**: None. Kanipi agents run Claude Code SDK directly —
no action/provider routing layer. The agent decides actions natively.
The `RESEARCH_NEEDED` trigger concept maps to a `/research` skill.

## Should-Respond Template

ElizaOS `shouldRespondTemplate` (prompts.ts:1-33):

```
If YOUR name ({agentName}) is directly mentioned → RESPOND
If someone uses a DIFFERENT name → IGNORE
If actively participating and message continues thread → RESPOND
If told to stop → STOP
Otherwise → IGNORE
```

**Kanipi equivalent**: `trigger_pattern` in registered_groups.
Gateway handles this at routing level — agent never sees messages
it shouldn't respond to. Simpler and more reliable.

## Knowledge Context Provider

Evangelist `knowledgeContextProvider` (providers/knowledgeContext.ts):

Injects relevant facts into `{{providers}}` per-message:

```xml
<knowledge_context query="{message text}">
  <tier name="High" count="2">
    <fact path="validator-bonds-overview" confidence="92%">
      header: Validator Bonds Overview
      topic: validator-bonds
      verification: verified (high)
      summary: Bond accounts store validator identity...
      read_full: facts/validator-bonds-overview.md
    </fact>
  </tier>
  <tier name="Medium" count="3">
    ...
  </tier>
  <search_tip>Use Read tool on facts/{path}.md for full content</search_tip>
</knowledge_context>
```

Tiers and limits:

- High (>80% similarity): max 3 facts
- Medium (40-80%): max 5
- Low (10-40%): max 5
- Very Low (<10%): max 5

Confidence formula: `max(0, (similarity - 0.70) * 3.33)`

**Kanipi equivalent**: Phase 2 gateway injection (specs/atlas/TODO.md).
Phase 1 uses agentic search (agent greps facts/ directly).

## Post Creation Template

ElizaOS `postCreationTemplate` (prompts.ts:102-146):

```
Write a post that is {{adjective}} about {{topic}} (without
mentioning {{topic}} directly), from the perspective of {{agentName}}.

Your response should be 1, 2, or 3 sentences.
No questions. Brief, concise statements only.
Total character count MUST be less than 280.
No emojis. Use \n\n between statements.
```

**Kanipi equivalent**: `tweet` skill (container/skills/tweet/SKILL.md).

## character.json Fields (ElizaOS)

```json
{
  "name": "Agent Name",
  "system": "System prompt text (injected verbatim)",
  "bio": ["Array of bio lines, joined with space"],
  "topics": ["used for post generation, random selection"],
  "adjectives": ["used for post generation, random selection"],
  "style": {
    "all": ["style rules for all contexts"],
    "chat": ["style rules for chat only"]
  },
  "messageExamples": [
    [
      { "name": "user", "content": { "text": "question" } },
      { "name": "Agent", "content": { "text": "answer" } }
    ]
  ],
  "templates": {
    "messageHandlerTemplate": "override default template",
    "shouldRespondTemplate": "override default template"
  }
}
```

**Kanipi character.json** supports all the same fields except
`templates` (kanipi uses SDK system prompt, not Handlebars templates).
The `{NAME}` placeholder is replaced at load time.

## What Kanipi Should Copy

1. **Knowledge context XML format** — the tier/confidence/fact
   structure works well for injection. Use same format in Phase 2.
2. **RESEARCH_NEEDED trigger** — the concept of detecting knowledge
   gaps and auto-triggering research. Implement as skill behavior.
3. **Fact formatting** — path, header, topic, verification, summary,
   read_full pattern. Agent knows how to dig deeper.
4. **Style section** — ElizaOS puts style as bullet list, same as
   kanipi. Keep this.

## What Kanipi Should NOT Copy

1. **Handlebars templates** — kanipi uses SDK system prompt, not
   template rendering. Simpler.
2. **Action/provider routing** — Claude Code handles this natively.
3. **Should-respond logic** — gateway routing handles this better.
4. **XML response format** — Claude Code agents return text directly.
