# grants

Grant rule engine. Last-match-wins evaluation over action rules.

## Purpose

Rule format: `[!]action[(param=glob,...)]`. No match = deny.
`DeriveRules` builds per-spawn rule sets from store rows, tier, and
worldFolder. `NarrowRules` lets a child group inherit a narrower subset
from its parent. Rules are injected into `start.json` for the agent and
also filter the MCP tool manifest.

## Public API

- `Rule`, `ParamRule`, `ParseRule(r string) Rule`
- `CheckAction(rules []string, action string, params map[string]string) bool`
- `MatchingRules(rules []string, action string) []string`
- `NarrowRules(parent, child []string) []string`
- `DeriveRules(s *store.Store, folder string, tier int, worldFolder string) []string`

## Dependencies

- `store`

## Files

- `grants.go`

## Related docs

- `ARCHITECTURE.md` (Grants Engine)
- `specs/7/28-acl.md`
