# arizuko feature inventory

Complete feature list across the 14 product areas, with code-presence and
test-coverage status. Drives `tests/integration_features_test.go`.

Status legend:

- **exists**: yes (in code) / no
- **tested**: yes (covered by an automated test) / partial (some paths) /
  no (no automated coverage)
- **ref**: file or `file:section` the feature lives in / is documented at

New coverage added by `tests/integration_features_test.go` is marked
`TestFeature_*`. Pre-existing coverage cites the owning `*_test.go`.

---

## 1. Message routing

| Feature                                                       | exists | tested                                                                                                                         | ref                                               |
| ------------------------------------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------- |
| Inbound route table (seq-ordered predicate match, first wins) | yes    | yes — `TestFeature_MessageRouting/prefix-precedence`, `split_federation_test.go::TestSplitVsMonolithRoutingParity`             | `router/router.go:ResolveRouteTarget`, ROUTING.md |
| Prefix/platform routing (`platform=`)                         | yes    | yes — `TestFeature_MessageRouting/prefix-precedence`                                                                           | ROUTING.md                                        |
| Room routing (`room=`)                                        | yes    | yes — `TestFeature_MessageRouting/room-match`                                                                                  | ROUTING.md                                        |
| Route miss → no dispatch                                      | yes    | yes — `TestFeature_MessageRouting/no-match`                                                                                    | ROUTING.md                                        |
| Sticky group (`@folder` pin)                                  | yes    | yes — `TestFeature_MessageRouting/sticky-pin`                                                                                  | `store/groups.go:SetStickyGroup`                  |
| Sticky topic (`#topic` pin)                                   | yes    | partial — store round-trip only                                                                                                | `store/groups.go:SetStickyTopic`                  |
| Reply-chain routing (`routed_to`)                             | yes    | partial — `routd/thread_reply_test.go`                                                                                         | ROUTING.md                                        |
| Topic routing / `(folder,topic)` isolation                    | yes    | partial — `tests/topic_lineage_test.go`, `routd/session_fork_test.go`                                                          | ROUTING.md                                        |
| Topic forking / lineage (`parent_topic`)                      | yes    | yes — `tests/topic_lineage_test.go::TestFork_MCP_*` (skipped pending auth case), `store/sessions_test.go`                      | ROUTING.md                                        |
| Prefix delegation (`@child`)                                  | yes    | partial — `routd/parity_routing_test.go`                                                                                       | ROUTING.md                                        |
| Forwarding (delegation return-address)                        | yes    | partial — `routd/turns_social_test.go`                                                                                         | ROUTING.md, `routd/api/v1` ForwardedFrom          |
| Inbound→dispatch end-to-end (HTTP→loop→runed→reply)           | yes    | yes — `TestFeature_MessageRouting/inbound-to-dispatch`, `split_federation_test.go::TestSplitFederation_InboundToTurnRoundTrip` | ARCHITECTURE.md                                   |
| Web JID direct routing (`web:<folder>`)                       | yes    | yes — `TestFeature_WebChat/chat-token-binding`                                                                                 | ROUTING.md                                        |
| HTTP route table (`web_routes` longest-prefix)                | yes    | yes — `TestFeature_IntegrationPlumbing/web-routes-table`                                                                       | `routd/tokens.go:PutWebRoute`                     |

## 2. Agent dispatch

| Feature                                   | exists | tested                                                                                        | ref                                     |
| ----------------------------------------- | ------ | --------------------------------------------------------------------------------------------- | --------------------------------------- |
| Container spawn per turn (runed)          | yes    | yes — `TestFeature_AgentDispatch/spawn-persisted` (FakeRuntime), `routd/parity_spawn_test.go` | ARCHITECTURE.md                         |
| Spawn row persisted (runed.db)            | yes    | yes — `TestFeature_AgentDispatch/spawn-persisted`                                             | ARCHITECTURE.md                         |
| Session persistence / fork                | yes    | partial — `routd/session_test.go`, `routd/session_fork_test.go`                               | ARCHITECTURE.md                         |
| Context mode (group / isolated)           | yes    | yes — `TestFeature_AgentDispatch/context-mode`                                                | `core/types.go:Task.ContextMode`        |
| Per-group model override                  | yes    | yes — `TestFeature_AgentDispatch/model-override`                                              | `store/groups.go:SetGroupModel`         |
| Observe window (context sizing)           | yes    | yes — `TestFeature_AgentDispatch/observe-window`, `tests/mcp_helpers_test.go`                 | `store/groups.go:SetGroupObserveWindow` |
| Group queue serialization                 | yes    | partial — `routd/loop_test.go`                                                                | ARCHITECTURE.md                         |
| Steer (SIGUSR1 mid-run inject)            | yes    | partial — `routd/steer_commands_test.go`                                                      | ARCHITECTURE.md                         |
| Circuit breaker (3-fail open)             | yes    | partial — runed manager                                                                       | ARCHITECTURE.md                         |
| MaxConcurrent cap                         | yes    | no                                                                                            | ARCHITECTURE.md                         |
| Run TTL / kill                            | yes    | partial — `runed/*`                                                                           | ARCHITECTURE.md                         |
| Cost cap pre-check                        | yes    | partial — `routd/budget_test.go`                                                              | routd/README.md                         |
| Egress allowlist (crackbox network_rules) | yes    | partial — `crackbox/pkg/*`                                                                    | ARCHITECTURE.md                         |

## 3. Channel adapters

Capability surface tested generically via `testutils.FakeChannel`
(`TestFeature_ChannelAdapters/capability-surface`): send, reply, file,
voice, like, delete, post, typing, prefix-Owns. Per-adapter wire encoding is
covered in each adapter's own `*_test.go`.

| Adapter           | exists | tested                 | ref             |
| ----------------- | ------ | ---------------------- | --------------- |
| teled (Telegram)  | yes    | yes — 8 test files     | teled/README.md |
| discd (Discord)   | yes    | yes — 7 test files     | discd/          |
| slakd (Slack)     | yes    | yes — 7 test files     | slakd/          |
| mastd (Mastodon)  | yes    | yes — 5 test files     | mastd/          |
| bskyd (Bluesky)   | yes    | yes — 5 test files     | bskyd/          |
| reditd (Reddit)   | yes    | yes — 5 test files     | reditd/         |
| emaid (Email)     | yes    | yes — 6 test files     | emaid/          |
| linkd (LinkedIn)  | yes    | partial — 3 test files | linkd/          |
| whapd (WhatsApp)  | yes    | **no — 0 test files**  | whapd/          |
| twitd (Twitter/X) | yes    | **no — 0 test files**  | twitd/          |

### Capability matrix (per adapter, from README + code)

| verb         | discd | slakd | mastd | bskyd | reditd | teled | emaid | linkd | whapd | twitd |
| ------------ | ----- | ----- | ----- | ----- | ------ | ----- | ----- | ----- | ----- | ----- |
| send / reply | ✓     | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     |
| send_file    | ✓     | ✓     |       |       |        | ✓     |       |       | ✓     | ✓     |
| send_voice   | ✓     |       |       |       |        | ✓     |       |       | ✓     |       |
| post         | ✓     | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |
| like         | ✓     | ✓     | ✓     | ✓     | ✓      | ✓     |       |       | ✓     | ✓     |
| delete       | ✓     | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |
| forward      |       |       |       |       |        | ✓     |       |       | ✓     |       |
| quote        |       |       |       | ✓     |        |       |       |       |       | ✓     |
| repost       |       |       | ✓     | ✓     |        |       |       |       |       | ✓     |
| dislike      |       | ✓     |       |       | ✓      |       |       |       |       |       |
| edit         | ✓     | ✓     | ✓     |       |        | ✓     |       |       | ✓     |       |
| pin / unpin  | ✓     | ✓     |       |       |        | ✓     |       |       |       |       |

## 4. Auth

| Feature                                             | exists | tested                                                                         | ref                     |
| --------------------------------------------------- | ------ | ------------------------------------------------------------------------------ | ----------------------- |
| Inbound service-token authn (ES256 JWKS verify)     | yes    | yes — `TestFeature_Auth/inbound-valid-token`, `split_federation_test.go`       | authd/README.md         |
| Invalid/missing/wrong-scope rejection (401/403)     | yes    | yes — `TestFeature_Auth/inbound-invalid-token`                                 | `routd/server.go:authz` |
| Folder-bound reply (flip-blocker containment)       | yes    | yes — `TestFeature_Auth/folder-bound-reply`, `routd/authz_containment_test.go` | specs/5/32              |
| ES256 signing / JWKS publish                        | yes    | yes — `auth/es256_test.go`, `routd/channels_es256_test.go`                     | authd/README.md         |
| Service token minting                               | yes    | yes — `auth/service_test.go`                                                   | authd/README.md         |
| Scope resolution (full grant set, not folder claim) | yes    | yes — `split_federation_test.go::TestSplitFederation_StampResolvesFullGrants`  | specs/5/32              |
| OAuth login (Google/GitHub/Discord/Telegram)        | yes    | no — authd is `package main`, OAuth front-end not in test harness              | authd/README.md         |
| Refresh token family / reuse detection              | yes    | partial — authd unit                                                           | authd/README.md         |
| Proxyd transit verification (`X-User-Sub` trust)    | yes    | partial — `dashd/auth_bearer_test.go`                                          | webd/README.md          |
| Login rate limiting                                 | yes    | no                                                                             | ARCHITECTURE.md         |

## 5. Grants / ACL

| Feature                                 | exists | tested                                                                | ref                              |
| --------------------------------------- | ------ | --------------------------------------------------------------------- | -------------------------------- |
| ACL CRUD (add/resolve/remove)           | yes    | yes — `auth/acl_test.go`, `routd/acl_endpoint_test.go`                | `store/acl.go`                   |
| Allow/deny effect (deny wins)           | yes    | yes — `TestAuthorize_DenyWinsSameTriple`                              | `auth/authorize.go`              |
| Param glob matching                     | yes    | yes — `TestAuthorize_ParamGlob`                                       | `auth/acl.go:matchPattern`       |
| No fallback (missing grant denies loud) | yes    | yes — `TestAuthorizeWith_NoTierFallback`, `TestAuthorize_EmptyDenies` | `auth/authorize.go`              |
| Action lattice (`*` ⊃ admin ⊃ interact) | yes    | yes — `TestAuthorize_ActionLattice*` (3 cases)                        | `auth/authorize.go:actionCovers` |
| Delegation bounded by subset-of-held    | yes    | yes — `TestDelegate`, `auth/delegate_test.go`                         | `auth/delegate.go`               |
| REST acl:write scope gate               | yes    | yes — `routd/acl_endpoint_test.go`                                    | `routd/acl_resource.go`          |
| Scope-widening prevention               | yes    | yes — no-fallback + delegate subset tests                             | specs/5/33                       |
| Role membership / cycle prevention      | yes    | partial — `auth/acl_test.go`                                          | specs/5/32                       |
| Operator (`**`) resolution              | yes    | partial — `auth/authorize_test.go`                                    | ARCHITECTURE.md                  |
| ACL overlay / per-spawn cache           | yes    | partial — `routd/acl_overlay_test.go`                                 | specs/5/32                       |

## 6. Scheduled tasks

| Feature                                         | exists | tested                                                                   | ref                                              |
| ----------------------------------------------- | ------ | ------------------------------------------------------------------------ | ------------------------------------------------ |
| Task create                                     | yes    | yes — `TestFeature_Tasks/create`, `routd/tasks_test.go`                  | `store/tasks.go:CreateTask`                      |
| Cron / interval scheduling                      | yes    | partial — `timed/timed_test.go` (computeNextRun is `package main`)       | timed/README.md                                  |
| Pause / resume / cancel state machine           | yes    | yes — `TestFeature_Tasks/pause-resume-cancel`                            | `store/tasks.go:SetTaskStatus`                   |
| next_run set on reschedule (dead-task guard)    | yes    | yes — `TestFeature_Tasks/reschedule-sets-future-next-run`                | `store/tasks.go:RescheduleTask`, .diary/20260618 |
| Firing recovery (crash safety)                  | yes    | yes — `TestFeature_Tasks/firing-recovery`                                | `store/tasks.go:RecoverFiringTasks`              |
| Due-query predicate (`datetime(next_run)<=now`) | yes    | yes — `TestFeature_Tasks/*` via `dueCount`, `tests/microservice_test.go` | timed/main.go                                    |
| timed → routd cron-fire contract                | yes    | yes — `tests/microservice_test.go::TestCronFiresMessage`                 | timed/README.md                                  |
| Run log                                         | yes    | partial — `store/inspect.go`                                             | ARCHITECTURE.md                                  |
| Manual trigger (run_now)                        | yes    | no                                                                       | SCREENS.md                                       |

## 7. Webhooks / route tokens

| Feature                                    | exists | tested                                                | ref                                    |
| ------------------------------------------ | ------ | ----------------------------------------------------- | -------------------------------------- |
| Token issue + resolve (raw→hash→jid)       | yes    | yes — `TestFeature_RouteTokens/issue-and-resolve`     | `routd/tokens.go`                      |
| Token revoke                               | yes    | yes — `TestFeature_RouteTokens/revoke`                | `routd/tokens.go:RevokeRouteTokens`    |
| HTTP chat-token create (routes:write gate) | yes    | yes — `TestFeature_RouteTokens/http-create-chat`      | `routd/tokens_http.go:handleTokenChat` |
| Hook label validation (`[\w-]+`)           | yes    | yes — `TestFeature_RouteTokens/hook-label-validation` | `routd/tokens_http.go:handleTokenHook` |
| JID encoding (web:/hook: + suffix)         | yes    | yes — chat + hook create tests                        | `routd/tokens_http.go`                 |
| owner_folder subtree containment           | yes    | partial — handler checks `ownsFolder`                 | `routd/tokens_http.go`                 |
| Token list                                 | yes    | partial — `routd/tokens.go:ListRouteTokens`           | routd                                  |

## 8. Onboarding

| Feature                             | exists | tested                                                 | ref                                     |
| ----------------------------------- | ------ | ------------------------------------------------------ | --------------------------------------- |
| Invite create + list                | yes    | yes — `TestFeature_Onboarding/invite-create-list`      | `store/invites.go:CreateInvite`         |
| Invite consume / single-use exhaust | yes    | yes — `TestFeature_Onboarding/invite-consume-exhausts` | `store/invites.go:ConsumeInviteNoGrant` |
| Invite revoke                       | yes    | yes — `TestFeature_Onboarding/invite-revoke`           | `store/invites.go:RevokeInvite`         |
| Invite expiry enforcement           | yes    | yes — `TestFeature_Onboarding/invite-expiry`           | `store/invites.go`                      |
| Admission queue / approval loop     | yes    | partial — onbod                                        | onbod/README.md                         |
| Admission gates (per-day limits)    | yes    | no                                                     | onbod/README.md                         |
| Group auto-create (SetupGroup)      | yes    | partial — container                                    | onbod/README.md                         |
| Second-JID auto-link                | yes    | no                                                     | onbod/README.md                         |

## 9. dashd (operator dashboard)

dashd page handlers are `package main`, covered by `dashd/*_test.go`
(handler-level httptest with `X-User-Sub`). The admin write plane (the routd
REST that dashd writes through in the split) is covered by
`TestFeature_DashdAdminPlane`.

| Page / action                   | exists            | tested                                                                                                                             | ref        |
| ------------------------------- | ----------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| Portal (`/dash/`)               | yes               | yes — `dashd/coverage_test.go`, `dashd/main_test.go`                                                                               | SCREENS.md |
| Services health grid            | yes               | yes — `dashd/services_test.go`                                                                                                     | SCREENS.md |
| Status                          | yes               | partial — `dashd/coverage_test.go`                                                                                                 | SCREENS.md |
| Activity feed                   | yes               | partial — `dashd/coverage_test.go`                                                                                                 | SCREENS.md |
| Tasks CRUD                      | yes               | yes — `dashd/coverage_test.go`                                                                                                     | SCREENS.md |
| Groups list / CRUD              | yes               | yes — `dashd/admin_integration_test.go::TestDash_GroupDelete`                                                                      | SCREENS.md |
| Routes CRUD                     | yes — REST + page | yes — `TestFeature_DashdAdminPlane/routes-crud`, `dashd/admin_integration_test.go::TestDash_RouteCreateAndDelete` (+ DenyNonAdmin) | SCREENS.md |
| Grants CRUD                     | yes — REST + page | yes — `TestFeature_DashdAdminPlane/grants-crud`, `dashd/authz_test.go`                                                             | SCREENS.md |
| Invites                         | yes               | yes — `dashd/invites_test.go`                                                                                                      | SCREENS.md |
| Channels (whapd pair)           | yes               | yes — `dashd/channels_test.go`                                                                                                     | SCREENS.md |
| Memory read/write               | yes               | partial — `dashd/coverage_test.go`                                                                                                 | SCREENS.md |
| Profile                         | yes               | yes — `dashd/profile_test.go`                                                                                                      | SCREENS.md |
| Per-user secrets                | yes               | yes — `dashd/me_secrets_test.go`, `dashd/me_secrets_routd_test.go`                                                                 | SCREENS.md |
| routd control plane drill-down  | yes               | yes — `dashd/routd_page_test.go`                                                                                                   | SCREENS.md |
| runed control plane drill-down  | yes               | yes — `dashd/runed_page_test.go`                                                                                                   | SCREENS.md |
| Audit log page                  | yes               | yes — `dashd/audit_page_test.go`                                                                                                   | SCREENS.md |
| Usage analytics page            | yes               | yes — `dashd/usage_page_test.go`                                                                                                   | SCREENS.md |
| Chat portal / session list      | yes               | yes — `dashd/chat_test.go`                                                                                                         | SCREENS.md |
| Folder isolation (scope filter) | yes               | yes — `dashd/isolation_test.go`                                                                                                    | SCREENS.md |

## 10. MCP / agent tools

Tools dispatched over the real MCP unix socket. Generic socket path covered by
`TestFeature_MCPTools` + `tests/topic_lineage_test.go`; verb-level coverage in
`routd/mcp_test.go`, `routd/turns_social_test.go`, `routd/voice_test.go`.

| Tool                                                   | exists | tested                                                                       | ref            |
| ------------------------------------------------------ | ------ | ---------------------------------------------------------------------------- | -------------- |
| set_observe_window                                     | yes    | yes — `TestFeature_MCPTools/set-observe-window`, `tests/mcp_helpers_test.go` | `ipc/ipc.go`   |
| set_group_open                                         | yes    | yes — `tests/mcp_helpers_test.go` (tier + cross-folder)                      | `ipc/ipc.go`   |
| fork_topic                                             | yes    | yes — `tests/topic_lineage_test.go` (skip-gated by known bug)                | `ipc/ipc.go`   |
| reply / send / send_file                               | yes    | partial — `routd/turns_social_test.go`, `routd/deliver_test.go`              | `ipc/ipc.go`   |
| send_voice                                             | yes    | partial — `routd/voice_test.go`                                              | `ipc/ipc.go`   |
| like / delete / edit / post / forward / quote / repost | yes    | partial — `routd/turns_social_test.go`                                       | `ipc/ipc.go`   |
| schedule_task                                          | yes    | partial — `routd/tasks_test.go`                                              | `ipc/ipc.go`   |
| reset_session                                          | yes    | partial — `routd/session_test.go`                                            | `ipc/ipc.go`   |
| inspect\_\* / find_messages (FTS5)                     | yes    | partial — `routd/mcp_test.go`                                                | `ipc/ipc.go`   |
| add_acl / remove_acl / list_acl                        | yes    | partial — `routd/acl_endpoint_test.go`                                       | `ipc/ipc.go`   |
| invite_create (MCP)                                    | yes    | partial — `routd/invite_mcp_test.go`                                         | `ipc/ipc.go`   |
| dark/hidden tools gating                               | yes    | partial — `routd/dark_tools_test.go`                                         | `routd/mcp.go` |

## 11. Memory / skills

| Feature                                 | exists | tested                                               | ref                               |
| --------------------------------------- | ------ | ---------------------------------------------------- | --------------------------------- |
| Group open flag (engagement gate)       | yes    | yes — `TestFeature_MemoryGroupState/group-open-flag` | `store/groups.go:SetGroupOpen`    |
| Group watchers (cross-folder ambient)   | yes    | yes — `TestFeature_MemoryGroupState/group-watchers`  | `store/groups.go:AddGroupWatcher` |
| MEMORY.md / PERSONA.md / CLAUDE.md      | yes    | partial — `ant/pkg/agent/loader_test.go`             | EXTENDING.md                      |
| Diary (episodes, nudge, compression)    | yes    | partial — diary pkg                                  | ARCHITECTURE.md                   |
| Facts / users directories               | yes    | no                                                   | EXTENDING.md                      |
| Skill seeding / migration (3-way merge) | yes    | partial — `ant/skills/self/`                         | EXTENDING.md                      |
| Skill discovery / dispatch              | yes    | no (agent-runtime)                                   | EXTENDING.md                      |
| Migration version broadcast hook        | yes    | partial — `routd/migrate_test.go`                    | CLAUDE.md                         |

## 12. Web / chat portal

| Feature                             | exists | tested                                         | ref               |
| ----------------------------------- | ------ | ---------------------------------------------- | ----------------- |
| Chat token → web JID binding        | yes    | yes — `TestFeature_WebChat/chat-token-binding` | `routd/tokens.go` |
| Slink widget                        | yes    | partial — `webd/` E2E (`make test-e2e`)        | EXTENDING.md      |
| Chat sessions list / continue links | yes    | yes — `dashd/chat_test.go`                     | SCREENS.md        |
| round_done SSE (keyed by chat JID)  | yes    | partial — `routd/web_presence_test.go`         | webd/README.md    |
| Webhook ingest (`/hook/<token>`)    | yes    | partial — token tests                          | webd/README.md    |
| Web presence (vhost per folder)     | yes    | partial — `routd/web_presence_test.go`         | ARCHITECTURE.md   |

## 13. Observability

| Feature                                     | exists | tested                                                | ref               |
| ------------------------------------------- | ------ | ----------------------------------------------------- | ----------------- |
| routd /health                               | yes    | yes — `TestFeature_HealthEndpoints/routd-health`      | `routd/server.go` |
| runed /health                               | yes    | yes — `TestFeature_HealthEndpoints/runed-health`      | `runed/server.go` |
| authd / dashd / timed / webd / davd /health | yes    | partial — own `*_test.go` (package main) + smoke      | each daemon       |
| Live multi-daemon /health (smoke)           | yes    | yes — `TestSmoke_DaemonHealth` (`-tags smoke`)        | Makefile smoke    |
| Audit log table + emission                  | yes    | partial — `audit/audit_test.go`, `audit/log_test.go`  | specs/5/I         |
| /metrics (Prometheus)                       | yes    | partial — obs pkg                                     | runed/README.md   |
| OTLP export                                 | yes    | partial — obs pkg                                     | specs/5/O         |
| Cost logging                                | yes    | partial — `store/cost_log.go`, `routd/budget_test.go` | ARCHITECTURE.md   |
| Turn context / results                      | yes    | partial — `split_federation_test.go` (PutTurnContext) | ARCHITECTURE.md   |

## 14. Integration plumbing

| Feature                                     | exists | tested                                                                                                     | ref                           |
| ------------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------- | ----------------------------- |
| Idempotency ledger (exactly-once)           | yes    | yes — `TestFeature_IntegrationPlumbing/idempotency-ledger`, `split_federation_test.go` (X-Idempotency-Key) | `routd/tokens.go:IdemClaim`   |
| web_routes URL-tree access table            | yes    | yes — `TestFeature_IntegrationPlumbing/web-routes-table`                                                   | `routd/tokens.go:PutWebRoute` |
| proxyd routing rules                        | yes    | partial — `routd/network_test.go`, compose tests                                                           | ARCHITECTURE.md               |
| davd (WebDAV per-group)                     | yes    | no                                                                                                         | ARCHITECTURE.md               |
| vited dev server                            | yes    | no                                                                                                         | ARCHITECTURE.md               |
| crackbox egress (network_rules, DNS, proxy) | yes    | partial — `crackbox/pkg/*`, `crackbox/test/egress_e2e_test.go`                                             | ARCHITECTURE.md               |
| MCP connectors (third-party servers)        | yes    | partial — `routd/connectors_test.go`                                                                       | EXTENDING.md                  |
| Whisper transcription                       | yes    | no                                                                                                         | ARCHITECTURE.md               |
| TTS (ttsd)                                  | yes    | partial — `routd/voice_test.go`                                                                            | routd/README.md               |
| Oracle skill (codex fallback)               | yes    | no (agent-runtime)                                                                                         | EXTENDING.md                  |
| Compose generation                          | yes    | partial — `compose/*` tests                                                                                | ARCHITECTURE.md               |

---

## Coverage gaps (completely untested features)

These have NO automated test anywhere in the repo:

1. **whapd adapter** — 0 test files. No send/reply/pair coverage.
2. **twitd adapter** — 0 test files. No verb coverage.
3. **MaxConcurrent container cap** — spawn-boundary cap unverified.
4. **OAuth login front-ends** (Google/GitHub/Discord/Telegram) — authd is
   `package main`; the OAuth callback + allowed-email/org gating untested.
5. **Login rate limiting** — 5/IP/15min unverified.
6. **Admission gates** (per-day limits) and **second-JID auto-link** — onbod
   business logic untested.
7. **Task run_now manual trigger** — dashd action, no test.
8. **Facts / users memory directories** — agent-runtime read paths untested.
9. **Skill discovery / dispatch** and **oracle skill** — agent-runtime only.
10. **davd (WebDAV)** and **vited dev server** — no integration test.
11. **Whisper transcription** — no test (external sidecar).

Partial-only areas worth deepening: reply-chain routing, prefix delegation,
forwarding, circuit breaker, OTLP/metrics export, round_done SSE end-to-end.
