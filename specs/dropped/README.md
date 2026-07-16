# dropped specs

Specs explicitly rejected. Kept for historical context.

| Spec                                                   | Reason                                                                                                                                                                                                                                                             |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| [3-agent-teams.md](3-agent-teams.md)                   | Superseded by Agent SDK subagent model; orphan + mount issues                                                                                                                                                                                                      |
| [13-b-ant-standalone.md](13-b-ant-standalone.md)       | Go standalone-ant rewrite. The standalone ant already exists in TS (`ant/ant` launcher + `arizuko-ant` image); this specced an abandoned Go reimpl. Superseded by `6/1` (scoped reimplement-in-Go) + `6/16` (ant = interface/template, runner = crackbox/dockbox). |
| [13-c-ant-mcp-runtime.md](13-c-ant-mcp-runtime.md)     | Go MCP runtime for the standalone-ant rewrite. Dropped with `13-b`.                                                                                                                                                                                                |
| [13-d-ant-image-cutover.md](13-d-ant-image-cutover.md) | `ant:latest` ENTRYPOINT swap to the Go binary. Dropped with `13-b` — no Go binary to cut over to.                                                                                                                                                                  |
