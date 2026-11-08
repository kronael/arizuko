---
name: zdroje-review
description: Review sources and legal quotations for Czech estate research.
---

# Source review

Collect primary documents and current registries before secondary sources. Record each source URL and date. Distinguish completed sales, offers, official rates, estimates and instructions. Verify the load-bearing figures against the original sources.

Read each legal provision in its current full context, including exceptions and transitional rules. For judgments, distinguish the court's decision and reasoning from the parties' claims. Check applicability and contrary evidence.

Run an adversarial source review with Codex using a self-contained brief:

```sh
codex exec --ephemeral -m gpt-6-astra -c 'model_reasoning_effort="high"' "Challenge the sources, figures and legal context in the supplied research. Identify evidence and impact for each gap."
```

Check CLI availability and authentication first. If the review cannot run, report that gap; do not substitute another provider. Repair each finding or state its effect and uncertainty. Follow `estate-site` for presentation.
