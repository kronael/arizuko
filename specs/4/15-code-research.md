---
status: draft
---

## <!-- source: arizuko specs/3/3-code-research.md, synced 2026-03-15 -->

## status: shipped

# Code Research Agent

A arizuko product configuration that turns a group into a codebase Q&A agent
with background research capabilities. Users ask questions about a mounted
codebase; the agent searches facts, researches deeply when needed, and
replies with evidence-backed findings.

Merged from `4/H-researcher` (background research subagent) and
`4/3-support` (code researcher product config). In production as
REDACTED Atlas since March 2026.

---

## 1. What It Is

A group configured with:

- Codebase mount (ro) at `/workspace/extra/<name>`
- SYSTEM.md replacing the Claude Code default system prompt
- SOUL.md persona (name, voice, domain)
- `facts/` knowledge base with YAML frontmatter
- `/facts` skill for research + verification
- Strict relevance rule for knowledge-first answers

The agent answers questions about the mounted codebase. When its knowledge
base doesn't fully answer a question, it automatically researches and
creates new facts.

---

## 2. Architecture

### Tier model

```
atlas/              tier 1 — world admin
  atlas/support     tier 2 — Q&A agent (code researcher)
  atlas/support/web tier 3 — web dashboard (future)
```

### Mount system

Instance `.env`:

```env
EXTRA_MOUNTS=/path/to/repo:codebase:ro
```

Gateway reads `EXTRA_MOUNTS` and appends volume mounts:

```
hostPath: /path/to/repo → containerPath: /workspace/extra/codebase (readonly)
```

### Knowledge base

`facts/` directory in the group folder:

```
facts/
  validator-bonds-overview.md
  REDACTED-staking-fees.md
  solana-runtime-cpi.md
```

Each fact file has YAML frontmatter:

```yaml
---
slug: validator-bonds-overview
topic: validator-bonds
verified_at: 2026-03-10T14:30:00Z
verification:
  status: verified
  confidence: high
  verified_count: 5
  rejected_count: 1
---
```

### /facts skill

Two-phase process:

1. **Research** — Opus agent explores codebase + web, writes factset XML
2. **Verify** — Sonnet agent tries to refute each finding

Results written to `facts/<slug>.md`. Agent answers from verified facts.

---

## 3. ElizaOS Prompt Patterns (Verbatim Reference)

All prompts below are **verbatim from the ElizaOS source** in
`refs/eliza-plugin-evangelist/`. Each includes the original, then
a note on how arizuko handles the same concern.

### 3a. Research Prompt

Source: `researchService.ts:69-190` (`buildResearchPrompt()`)

```
Research the following question. You have 40 MINUTES max - be focused, don't over-explore.

QUESTION: {{QUESTION}}

# Context: {FOCUS}

**Projects:**
- **{name}**: {description} (repos: {repos})

**Repositories** (GitHub: {org}):
- https://github.com/{org}/{repo}

**Websites:**
- {url} - {description}

**Documentation:**
- {url} - {description}

**Additional context:**
- {context}

# Workspace

- **{{CODEBASE_DIR}}**: Persistent storage for git clones
- **Current dir**: {{CODEBASE_ROOT}} (the bot's codebase)

# Known Repositories

The following repos should be cloned to {{CODEBASE_DIR}}:

- https://github.com/{org}/{repo}

**IMPORTANT**: If {{CODEBASE_DIR}} is empty or missing repos:
1. Use Bash tool to clone missing repos: `cd {{CODEBASE_DIR}} && git clone https://github.com/{org}/{repo}`
2. After cloning, search the repos to answer the question
3. Don't just report repos are missing - clone them first, then research

# Time constraint

You have 40 minutes. Clone missing repos FIRST, then research.
If you cannot find the answer, keep it short - don't over-explain failure.

# Web access

Public web access is allowed. Prefer authoritative sources, but you may use a wide
range of references when needed, including official docs, GitHub, DeepWiki,
technical blogs, and forum discussions.

# Git history

Repos in {{CODEBASE_DIR}} are kept up-to-date. Use git commands freely:
- `git log --oneline -20` for recent commits
- `git log --since="1 week ago" --oneline` for time-based history
- `git diff HEAD~5` for recent changes
- `git show <hash>` for specific commits
- `git log --all --grep="keyword"` for searching commit messages

# Output Format

<research>
  <factset>
    <slug>kebab-case-topic-slug</slug>
    <fact source="/path/file.rs:142">One verifiable finding with file:line
    reference inline. See path/to/file.rs:142</fact>
    <fact source="/path/other.rs:55">Another discrete finding</fact>
    <summary>1-2 sentence summary optimized for embedding search.
    Include function names, file paths, key concepts.</summary>
  </factset>
</research>

Rules:
- Group findings by topic. Each <factset> gets its own slug+summary.
- <slug>: kebab-case, 3-5 segments. Reuse existing slugs when topic matches.
- <fact source="...">: one verifiable claim per tag. Include file:line or URL.
  The source attribute helps the verifier locate evidence quickly.
  Also include the reference inline in the text itself.
- <summary>: at BOTTOM of each factset. You have all the facts in context
  now, so write a 1-2 sentence summary optimized for embedding search.
  Include function names, file paths, key concepts.
- Each fact should stand alone. Keep under 200 words. No fluff.
- Do NOT write a prose answer. Only facts and summaries.

Existing knowledge files (reuse slug if your findings relate):
{slugList}
```

**Kanipi equivalent**: The `/facts` skill runs Claude Code subagents with the
same two-phase approach (research + verify). The prompt is simpler because
arizuko agents have native tool access — no need for workspace setup
instructions. The factset XML output format is reused verbatim.

### 3b. Verification Prompt

Source: `researchService.ts:1023-1041` (`verifyFactset()`)

```
You are verifying research findings about: ${subject}

For each finding, try to REFUTE it using the codebase. If you cannot
refute it, it is VERIFIED. Fix inaccuracies you find.

<finding id="1" source="/path/file.rs:42">Finding text</finding>
<finding id="2" source="/path/other.rs:55">Another finding</finding>

Also verify/improve these metadata fields:
- Proposed slug: ${slug || '(generate one: kebab-case, 3-5 segments)'}
- Proposed summary: ${summary || '(generate one: 1-2 sentences for embedding search)'}
- Existing file slugs: ${slugList}

Output format:
<verification>
  <finding id="1" status="verified">Corrected text if needed</finding>
  <finding id="2" status="rejected" reason="...">Original text</finding>
  <slug>final-slug</slug>
  <summary>Final summary for embedding search</summary>
</verification>
```

**Kanipi equivalent**: Same prompt, same format. The `/facts` skill runs
verification as a Sonnet subagent against the mounted codebase.

### 3c. Knowledge Context XML

Source: `providers/knowledgeContext.ts`

```xml
<knowledge_context query="{message text}">
  <tier name="High" count="2">
    <fact path="validator-bonds-overview" confidence="92%">
      summary: Validator Bonds Overview
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

**Kanipi equivalent**: No embedding infrastructure. Agent greps `facts/`
summaries directly in `<think>` blocks. The strict relevance rule replaces
tiered confidence — fact either fully answers the question or doesn't.
Semantic search deferred to future work.

### 3d. Message Handler Template

Source: `prompts.ts:35-100` (`messageHandlerTemplate`)

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

**Kanipi equivalent**: None needed. Kanipi agents run Claude Code SDK
directly — no action/provider routing layer. The agent decides actions
natively. The `RESEARCH_NEEDED` concept maps to the `/facts` skill
invocation, triggered by SYSTEM.md instructions.

### 3e. Codebase Context Provider

Source: `providers/codebaseContext.ts`

**Greeting detection keywords** (verbatim):

```
hello, hi, hey, helo, help, start, /start, yo, sup,
morning, afternoon, evening, night,
good morning, good afternoon, good evening, good night,
greetings, howdy, hiya, heya, hoi, aloha,
wassup, what's up, whats up,
how are you, how do you do, how's it going,
👋, 👍
```

**Code question keywords** (verbatim):

```
where, find, search, show, how, what is, explain,
function, class, file, package, runtime, action, service,
provider, plugin, code, implement, define, located
```

**First-contact intro template**:

```
## First Contact

This is a {first-time user | greeting message}. Introduce yourself:
- {botGreeting}
- You can research any GitHub repo or codebase on demand
- Your specialty is the {githubOrg} ecosystem (repos already cloned and indexed)
- You can search code, explain architecture, trace bugs
- Ask the user what they want to explore

Keep it short and energetic!
```

**Code question response rules**:

```
## Code Question Detected

For questions about external systems:
- Use RESEARCH_NEEDED action for deep code research
- Check knowledge facts first (provided by knowledgeContext)

## Response Rules
- NEVER claim code behavior without citing specific file:line evidence
- If you don't have evidence, say "Let me research that" and use RESEARCH_NEEDED
- Be honest about uncertainty
```

**Kanipi equivalent**: Greeting handling via `/hello` skill in
container/CLAUDE.md. Code question routing handled by SYSTEM.md
strict relevance rule — same concept, simpler mechanism.

### 3f. Research Needed Action

Source: `actions/researchNeeded.ts`

**`<research_question>` extraction pattern**:

```ts
// Structured field first
if (typeof llmResponse.research_question === 'string') ...

// XML tags in text
const m = text.match(/<research_question>\s*([\s\S]*?)\s*<\/research_question>/);
```

**Knowledge gap detection**:

- Similarity threshold: 0.7 (`HIGH_SIMILARITY_THRESHOLD`)
- If no facts service → trigger research
- If 0 facts loaded → trigger research
- If top similarity < 0.7 → trigger research
- Dedup cache: 30s TTL, validate() fires 7+ times per cycle

**Retry logic**: If `<research_question>` tag missing, re-prompt with full
conversation context (15 messages) asking only for the tag. Accept bare
text if 10-500 chars with no XML.

**Examples** (verbatim):

```
User: How do plugins work in ElizaOS?
Agent: On it! I'll research this properly - might take up to 10 minutes.
       <research_question>How does the plugin system register and initialize
       services in ElizaOS?</research_question>

User: Can you investigate how the database adapter works?
Agent: Let me dig into the database layer - I'll research this properly,
       might take up to 10 minutes.
       <research_question>How does the IDatabaseAdapter interface work and
       what query methods does it expose?</research_question>

User: what fees does REDACTED charge?
Agent: Good question - I'll research this properly, might take up to 10
       minutes.
       <research_question>What are the fee structures for REDACTED liquid
       staking and native staking products?</research_question>
```

**Kanipi equivalent**: The agent detects knowledge gaps in `<think>` blocks
via the strict relevance rule, then invokes `/facts` directly. No
XML extraction pattern needed — the agent runs the skill natively.

### 3g. Should-Respond Template

Source: `prompts.ts:1-33` (`shouldRespondTemplate`)

```
If YOUR name ({agentName}) is directly mentioned → RESPOND
If someone uses a DIFFERENT name → IGNORE
If actively participating and message continues thread → RESPOND
If told to stop → STOP
Otherwise → IGNORE
```

**Kanipi equivalent**: `trigger_pattern` in `groups` table.
Gateway handles this at routing level — agent never sees messages it
shouldn't respond to. Simpler and more reliable.

### 3h. Character.json Schema

Source: ElizaOS character definition

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

**Kanipi equivalent**: `character.json` supports same fields except
`templates` (arizuko uses SDK system prompt, not Handlebars). The `{NAME}`
placeholder is replaced at load time. Style section works identically.

### 3i. Allowed Tools

Source: `researchService.ts:47-67` (`ALLOWED_TOOLS`, `DISALLOWED_TOOLS`)

```
Allowed:
  Read, Glob, Grep, WebSearch, WebFetch,
  Bash(ls:*), Bash(find:*), Bash(tree:*), Bash(head:*), Bash(tail:*),
  Bash(wc:*), Bash(cat:*), Bash(file:*), Bash(stat:*), Bash(git:*),
  Bash(curl:*), Bash(wget:*)

Disallowed:
  Edit, NotebookEdit
```

**Kanipi equivalent**: Skill convention in SYSTEM.md — "do not modify
codebase files". Gateway doesn't enforce tool restrictions; the agent
follows instructions. Same read-only principle, simpler mechanism.

### 3j. Research Delivery XML

Source: `providers/researchDelivery.ts`

```xml
<research_delivery internal="true" mode="success">
<instruction>
This is an internal research delivery event, not a new user request.
Use the original question and findings below to continue the same thread naturally.
Do not trigger RESEARCH_NEEDED again for this delivery event.
</instruction>
<request id="{requestId}" original_message_id="{originalMessageId}">
  <entity>{entityName}</entity>
  <question>{question}</question>
  <fact_count>{factCount}</fact_count>
</request>
<facts>
  <fact index="1">{verified fact text}</fact>
  <fact index="2">{verified fact text}</fact>
</facts>
</research_delivery>
```

Error mode includes `<delivery_error>` tag inside `<request>`.

**Kanipi equivalent**: Not needed. The agent runs `/facts` synchronously
within its container session — no async delivery. For long research,
`<status>` blocks provide interim updates. Results are immediately
available in the same conversation context.

---

## 4. SYSTEM.md for Research Agents

Reference system prompt for code research groups. Replaces the Claude Code
default for user-facing research agents. Ships as a world template in
`container/worlds/code-researcher/` when worlds are implemented.

```markdown
# {AGENT_NAME}

You are a codebase research assistant. You answer questions about the
codebases mounted in your workspace using evidence from your knowledge
base and direct code exploration.

Read SOUL.md on session start for your persona and voice.

## Knowledge-First Rule

Before every answer, scan `facts/` headers in `<think>`:

A fact is relevant ONLY if it answers the question 100% correctly with
only trivial application needed. No interpretation, no inference, no
"probably matches". If you have any doubt, the fact is NOT relevant.

Decision tree:

- Fact fully answers + fresh (verified_at < 14 days) → answer from it
- Fact fully answers but stale → run `/facts` to refresh, then answer
- No fact fully answers → run `/facts` to research and create, then answer

Always deliberate in `<think>` before answering:

1. List candidate facts found by scanning headers
2. For each candidate, explain:
   - What does this fact say?
   - Does it directly answer the user's question?
   - What gap remains — what's missing, uncertain, or requires inference?
3. Verdict: use the fact, refresh it (`/facts`), or research from scratch

If you skip this reasoning or jump to an answer without evaluating facts
first, you will give wrong answers. The deliberation is mandatory.

Never guess. Never speculate. Never claim code behavior without citing
file:line evidence from facts or direct exploration.

## Research

When no fact is relevant, run `/facts` to research the question:

1. Emit `<status>researching...</status>` so the user knows
2. Run `/facts` with the specific question
3. Wait for results, then answer from the new facts

For follow-up questions on the same topic, check if existing facts
already cover it before re-researching.

## Evidence Standard

- Always cite file:line when referencing code
- Quote relevant code snippets when they clarify the answer
- If you cannot find evidence, say so honestly
- Never fabricate file paths, function names, or line numbers

## Conversation Style

- Keep answers focused and technical
- Lead with the answer, then supporting evidence
- Use code blocks for file paths, function names, code snippets
- For long research, emit `<status>` blocks to keep the user informed

## What You Do NOT Do

- Do not run builds, tests, or modify any files
- Do not execute arbitrary commands on the codebase
- Do not access developer tools (git status, npm, etc.) in responses
- Do not expose system internals to users
```

---

## 5. Howto: Building a Code Research Agent

### Option A: World template (future)

```bash
arizuko create <name> --world code-researcher
# Pre-configured: SYSTEM.md, SOUL.md template, /facts skill, mount config
```

### Option B: Manual setup

**Step 1: Create instance + groups**

```bash
arizuko create myresearch
arizuko group add myresearch atlas --tier 1
arizuko group add myresearch atlas/support --tier 2
```

**Step 2: Configure codebase mount**

Edit `/srv/data/arizuko_myresearch/.env`:

```env
EXTRA_MOUNTS=/path/to/target/repo:codebase:ro
TELEGRAM_BOT_TOKEN=...
```

**Step 3: Write SYSTEM.md**

Copy the reference SYSTEM.md (section 4 above) into the group folder:

```bash
cp system-template.md /srv/data/arizuko_myresearch/groups/atlas/support/SYSTEM.md
```

Edit `{AGENT_NAME}` to match your agent's name.

**Step 4: Write SOUL.md persona**

```markdown
# Atlas

You are Atlas, REDACTED's codebase research assistant. You specialize
in the REDACTED DeFi protocol on Solana — staking, validator bonds,
governance, and the Rust/TypeScript codebase.

Your voice is technical, precise, and helpful. You cite code when you
can and say "I don't know" when you can't.
```

**Step 5: Seed facts/**

Create initial fact files from existing documentation:

```bash
mkdir -p /srv/data/arizuko_myresearch/groups/atlas/support/facts
# Copy or create initial .md files with YAML frontmatter
```

**Step 6: Configure channels**

Set `TELEGRAM_BOT_TOKEN`, `DISCORD_BOT_TOKEN`, etc. in `.env`.
Configure `trigger_pattern` if needed.

**Step 7: Test**

```bash
sudo systemctl start arizuko_myresearch
# Send a question via configured channel
# Verify: agent searches facts/, runs /facts if needed, cites evidence
```

### Example: REDACTED Atlas (production)

```
Instance: REDACTED
World: atlas
Groups: atlas (tier 1), atlas/support (tier 2)

Mounts:
  /srv/data/REDACTED-repos:codebase:ro   # REDACTED source repos

SOUL.md: REDACTED-specific persona
SYSTEM.md: Research-focused (no developer output)
facts/: 40+ verified facts about REDACTED codebase
Skills: /facts (research+verify), /hello (greeting)

Channel: Telegram
Trigger: responds to all messages (requires_trigger=0)
```

---

## 6. What Kanipi Replaces from ElizaOS

| ElizaOS component                     | Kanipi equivalent                       | Needed? |
| ------------------------------------- | --------------------------------------- | ------- |
| ClaudeCodeService (subprocess mgmt)   | container-runner already does this      | No      |
| ResearchService queue + poll loop     | agent handles sequentially in container | No      |
| CodebaseService (file search helpers) | agent uses Read/Grep/Glob natively      | No      |
| FactsService (fact extraction)        | agent's own reasoning + /facts skill    | No      |
| KnowledgeDiscoveryService             | deferred (semantic search)              | No      |
| HelpSessionManager                    | arizuko groups scope sessions           | No      |
| researchNeededAction (classification) | agent decides inline via strict rule    | No      |
| Auth bridge (OAuth token mgmt)        | gateway runs outside container          | No      |
| ALLOWED_TOOLS restriction             | SYSTEM.md convention (no write tools)   | No      |
| Interim delivery via handleMessage    | `<status>` blocks + `send_reply` IPC    | No      |
| knowledgeContextProvider (embeddings) | grep + header scanning (for now)        | No      |
| shouldRespondTemplate                 | gateway trigger_pattern routing         | No      |
| messageHandlerTemplate                | Claude Code SDK native                  | No      |
| character.json → system prompt        | SOUL.md + SYSTEM.md                     | No      |

---

## 7. Open Questions

From the original H-researcher spec:

- **Timeout**: 40 min was too long for UX. Current `/facts` skill has
  no hard timeout — relies on container session timeout (~10 min default).
  May need skill-level timeout config.

- **Dedup**: How to prevent duplicate research on the same topic?
  Current approach: agent checks existing facts in `<think>` before
  researching. Could add slug-based dedup in the skill.

- **Cron research**: Scheduled background research (e.g., daily scan
  for codebase changes). Possible via `schedule_task` MCP tool +
  `/facts` skill. Not yet implemented.

- **Semantic search**: Embedding-based fact retrieval would replace
  grep + header scanning. Deferred — noted as future work. The strict
  relevance rule compensates by forcing research when grep matches
  are uncertain.

- **Multi-codebase**: Current mount system supports one `EXTRA_MOUNTS`
  value. Multiple codebases need comma-separated mounts or repeated
  config entries.
