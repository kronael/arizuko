---
status: deferred
---

# Evangelist

Community-engagement agent. Polls external sources (reddit first),
scores relevance, drafts responses, routes to human via dashd for
approval before posting.

Contract: per-instance group `evangelist/` with product facts + persona.
`timed` cron polls sources → agent writes drafts to
`evangelist_drafts` table → `/dash/evangelist/` review queue → approved
drafts posted back via adapter.

Rationale: broad pattern (scrape → score → draft → review → post)
generalizes across platforms. Reddit first.

Unblockers: reddit feed adapter, draft schema + dashboard, posting
path, approval flow. Authoring product
([../6/5-authoring-product.md](../6/5-authoring-product.md)) and HITL
firewall ([../6/4-hitl-firewall.md](../6/4-hitl-firewall.md)) together
may subsume this.
