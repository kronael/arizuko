# XML vs JSON for LLM Prompts and Outputs

Research document. Question: do LLMs (especially Claude) parse and reason about
XML better than JSON, and when is each format better?

---

## 1. Empirical benchmarks: comprehension accuracy

The clearest head-to-head study tested GPT-5 Nano, Llama 3.2 3B, and Gemini
2.5 Flash Lite on 1,000 questions per format over deeply nested (6-7 levels)
synthetic config data, calibrated to put models in the 40-60% accuracy range
so format differences show up.

| Model            | YAML  | Markdown | JSON  | XML   |
| ---------------- | ----- | -------- | ----- | ----- |
| GPT-5 Nano       | 62.1% | 54.3%    | 50.3% | 44.4% |
| Gemini 2.5 Flash | 51.9% | 48.2%    | 43.1% | 33.8% |
| Llama 3.2 3B     | ~50%  | ~50%     | ~50%  | ~50%  |

Source: [Which Nested Data Format Do LLMs Understand Best?](https://www.improvingagents.com/blog/best-nested-data-format/)

Key result: **XML performed worst for comprehension of nested data** in two of
three models. YAML won on accuracy; Markdown won on token economy.

Token cost of the same data:

- Markdown: baseline
- YAML: ~10% more than Markdown
- JSON: ~34-38% more than Markdown
- XML: ~80% more than Markdown (most expensive)

The Llama result (no format sensitivity) likely reflects a smaller model
that couldn't stress-test format differences at this nesting depth.

---

## 2. Format restrictions degrade reasoning

"Let Me Speak Freely?" (Tam et al., 2024) found that locking a model into a
structured format during generation harms reasoning tasks significantly:

| Condition               | GSM8K (GPT-3.5-Turbo) |
| ----------------------- | --------------------- |
| Natural language (free) | 75.99%                |
| JSON-mode               | 49.25% (-26.7 pts)    |
| XML format              | 45.06% (-30.9 pts)    |

Classification tasks showed the opposite trend — JSON-mode helped (e.g.
DDXPlus: 41.59% text → 60.36% JSON for Gemini 1.5 Flash).

The performance drop is not from parsing failures. Even when the model
produced syntactically valid output, reasoning accuracy dropped. The
hypothesis: constrained decoding distorts the probability distribution. High-
probability tokens are masked when they violate the grammar; remaining tokens
are renormalized. The model can no longer place probability mass on the
reasoning tokens it needs.

Source: [Let Me Speak Freely? (arXiv 2408.02442)](https://arxiv.org/html/2408.02442v1)

The recommended mitigation: let the model reason freely, then convert to
structured format afterward (a second pass or post-processing step).

---

## 3. Anthropic's own guidance

### XML for prompt structure (inputs)

Anthropic explicitly recommends XML tags to structure _prompts to Claude_,
not just outputs from Claude. The official prompt engineering docs state:

> "XML tags help Claude parse complex prompts unambiguously, especially when
> your prompt mixes instructions, context, examples, and variable inputs."

Recommended pattern:

```xml
<documents>
  <document index="1">
    <source>annual_report.pdf</source>
    <document_content>{{CONTENT}}</document_content>
  </document>
</documents>
```

Claude was trained with XML tags in training data. This is confirmed as
intentional: XML serves as a prompt-organizing mechanism. There are no
special "magic" tag names — the tags are flexible as long as they are
consistent.

Source: [Use XML tags to structure your prompts](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/use-xml-tags)
Source: [Claude 4.x prompting best practices](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-4-best-practices)

### JSON for structured outputs (machine-readable responses)

When the application needs guaranteed-valid, schema-conformant output for
downstream processing, Anthropic's structured outputs API is JSON-only:

```python
response = client.messages.parse(
    model="claude-opus-4-6",
    output_format=MySchema,  # pydantic or json schema
    messages=[...]
)
```

Strict tool use (`strict: True`) also guarantees JSON-shaped tool call
parameters. Anthropic's docs position this as superior to prompt engineering
for schema compliance: no parse errors, correct types, no retries.

Source: [Structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)

### The functional split

Anthropic's guidance implies a clean split:

- **XML**: organize prompts, delimit content sections, inject context
- **JSON**: machine-readable outputs, tool call parameters, schema-validated
  responses for downstream systems

---

## 4. Why XML works better for prompt structure

Claude was trained on large amounts of HTML/XML-tagged text (web crawl,
documentation, code). XML tags appeared in its training data as semantic
structure — headings, sections, code blocks, docstrings. The model learned
to treat tag boundaries as structural signals for reasoning.

JSON, by contrast, appears mainly in training as _data_, not as
_instructions_. A JSON blob containing instructions is unusual to the model;
XML wrapping instructions is familiar.

This also explains why XML works better for _input_ structure but not
necessarily for _output_ generation:

- Input XML: model reads it as structural context — familiar
- Output XML: model must produce balanced open/close tags — error-prone
  because there is no grammar enforcement

---

## 5. Streaming parseability

This is where XML has a practical advantage over JSON for streaming output:

**JSON streaming problem**: JSON is not valid until the closing brace/bracket.
A streaming client receiving partial JSON has an incomplete document. You must
buffer everything or use a partial-JSON parser (incremental hack).

**XML streaming**: XML is inherently incremental. Each `<tag>content</tag>`
element is self-contained. A streaming parser can emit partial results as each
element closes. The xmllm library (GitHub: padolsey/xmllm) exploits this:
it uses a lenient streaming HTML parser to extract data as XML elements arrive.

Projects using streaming structured output often prefer XML for this reason
alone — each field surfaces as it's generated rather than when the full object
closes.

Sources:

- [xmllm — Simple structured data from any LLM](https://github.com/padolsey/xmllm)
- [Streaming AI responses and the incomplete JSON problem](https://www.aha.io/engineering/articles/streaming-ai-responses-incomplete-json)
- [llm-xml-parser](https://github.com/ocherry341/llm-xml-parser)

---

## 6. Schema enforcement comparison

| Property                         | JSON                        | XML                          |
| -------------------------------- | --------------------------- | ---------------------------- |
| Grammar-constrained decoding     | Mature (Outlines, vLLM FSM) | Less tooling                 |
| Strict API guarantee (Anthropic) | Yes (structured outputs)    | No                           |
| Streaming partial reads          | Hard (buffering required)   | Natural (element-by-element) |
| Validity without enforcement     | Unreliable                  | Unreliable                   |
| Schema standard                  | JSON Schema                 | XSD, RelaxNG (complex)       |
| Verbosity overhead               | High (~34% vs Markdown)     | Very high (~80% vs Markdown) |
| Nesting clarity                  | Visual brackets             | Explicit open/close tags     |

Grammar-constrained decoding (FSM / finite state machines) is better
developed for JSON than XML. vLLM, Outlines, and lm-format-enforcer all
target JSON schemas natively. This is a significant tooling gap.

Source: [vLLM structured decoding intro](https://blog.vllm.ai/2025/01/14/struct-decode-intro.html)
Source: [Fast JSON Decoding (LMSYS)](https://lmsys.org/blog/2024-02-05-compressed-fsm/)

---

## 7. Agent inter-agent communication formats

Modern agent protocols (2024-2025) standardized on JSON, not XML:

- **MCP (Model Context Protocol)** — Anthropic, Nov 2024 — uses JSON-RPC 2.0
  for all gateway-to-tool and tool-to-tool messages.
- **A2A (Agent-to-Agent Protocol)** — Google — JSON-based agent cards and
  task lifecycle messages.
- **ACP (Agent Communication Protocol)** — IBM Research — JSON structured
  messages for intention, task parameters, context.

XML is absent from all current inter-agent protocol standards. JSON's compact
size and universal parse support in every language ecosystem won.

Source: [Survey of Agent Interoperability Protocols (arXiv 2505.02279)](https://arxiv.org/html/2505.02279v1)
Source: [MCP specification](https://modelcontextprotocol.io/specification/2025-11-25)

---

## 8. Community findings and prompt engineering practice (2024-2026)

**Keytail (content generation workflows)**: Recommends JSON for LLM-to-CMS
pipelines. Reasoning: simpler syntax, less error-prone output generation,
no tag-balancing mistakes.
Source: [Are LLMs Better at JSON or XML?](https://www.keytail.ai/blog/are-llms-better-at-json-or-xml-keytail-s-take)

**Michael Hannecke (LLM pipeline analysis)**: YAML outperforms both JSON and
XML for data representation in prompts. XML has the worst token cost/accuracy
ratio. Recommends YAML for config-like data fed to LLMs.
Source: [Beyond JSON: Picking the Right Format for LLM Pipelines](https://medium.com/@michael.hannecke/beyond-json-picking-the-right-format-for-llm-pipelines-b65f15f77f7d)

**General consensus across practitioners**: Two-phase approach:

1. Let model reason in natural language (no format constraint)
2. Convert output to structured format (JSON schema, post-processing)

This recovers near-baseline reasoning performance while still producing
structured output. The "Let Me Speak Freely?" paper validated this approach
empirically.

---

## 9. Summary: when to use each format

### Use XML when:

- Structuring prompt inputs to Claude (sections, context injection, examples)
- Streaming output where partial reads matter (each element is usable as
  it arrives)
- Delimiting reasoning sections in chain-of-thought prompts
  (`<thinking>`, `<answer>`)
- Mixing instructions with variable content inside a single prompt block

### Use JSON when:

- Structured machine-readable output for downstream code
- Tool call parameters (use `strict: True` with Anthropic API)
- Agent-to-agent protocol messages (MCP, A2A, ACP all use JSON-RPC)
- State files tracked across multi-context sessions (agentic workflows)
- Any case where grammar-constrained decoding tooling is needed

### Use YAML when:

- Feeding config-like or deeply nested data _into_ a prompt (best
  accuracy/token ratio in benchmarks)
- Human-readable configuration that the model must interpret

### Avoid XML when:

- Expecting guaranteed-valid output (no enforcement exists)
- The application needs schema validation (use JSON Schema + structured
  outputs instead)
- Building inter-agent protocols (all current standards are JSON)
- Token budget is a constraint (XML is the most expensive format)

---

## 10. Synthesis for kanipi / agent contexts

For kanipi's use cases specifically:

**IPC messages between gateway and containers**: JSON (already correct — aligns
with MCP, simpler to parse in TypeScript).

**System prompts and context injection**: XML tags for structure (matches
Anthropic training; improves Claude's section parsing).

**Agent skill files / instructions**: XML-tagged sections (`<instructions>`,
`<context>`, `<examples>`) are better prompt structure than JSON blobs.

**Streaming output to UI**: If per-field streaming matters, XML output is
easier to parse incrementally. If full-document delivery is fine, JSON is
cleaner.

**Tool results and structured data back to Claude**: YAML may outperform
JSON for deeply nested config/state data fed into context. Worth testing if
context token budget is a concern.

**Memory/facts files**: Plain text or YAML outperform JSON for LLM
comprehension; JSON better for machine reads. If the file is read by both
Claude and TypeScript code, JSON with a simple schema wins on practicality.

---

## Sources

- [Which Nested Data Format Do LLMs Understand Best? JSON vs YAML vs XML vs Markdown](https://www.improvingagents.com/blog/best-nested-data-format/)
- [Let Me Speak Freely? A Study on Format Restrictions on LLM Performance (arXiv 2408.02442)](https://arxiv.org/html/2408.02442v1)
- [Are LLMs Better at JSON or XML? — Keytail](https://www.keytail.ai/blog/are-llms-better-at-json-or-xml-keytail-s-take)
- [Beyond JSON: Picking the Right Format for LLM Pipelines — Medium](https://medium.com/@michael.hannecke/beyond-json-picking-the-right-format-for-llm-pipelines-b65f15f77f7d)
- [Use XML tags to structure your prompts — Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/use-xml-tags)
- [Claude 4.x prompting best practices — Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-4-best-practices)
- [Structured outputs — Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)
- [Streaming AI responses and the incomplete JSON problem — Aha!](https://www.aha.io/engineering/articles/streaming-ai-responses-incomplete-json)
- [xmllm — Simple structured data from any LLM (GitHub)](https://github.com/padolsey/xmllm)
- [llm-xml-parser (GitHub)](https://github.com/ocherry341/llm-xml-parser)
- [Fast JSON Decoding with Compressed FSM — LMSYS](https://lmsys.org/blog/2024-02-05-compressed-fsm/)
- [vLLM Structured Decoding Introduction](https://blog.vllm.ai/2025/01/14/struct-decode-intro.html)
- [Survey of Agent Interoperability Protocols (arXiv 2505.02279)](https://arxiv.org/html/2505.02279v1)
- [MCP Specification 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25)
- [Generating Structured Outputs from LLMs: Benchmark and Studies (arXiv 2501.10868)](https://arxiv.org/html/2501.10868v1)
