# Features

This file answers: what does arizuko do today, and which test proves it?
[README.md](README.md) names the project, [ARCHITECTURE.md](ARCHITECTURE.md)
shows where each piece lives, and [tests/README.md](tests/README.md) runs the
tests.

Each row names one behaviour and one test. A test cited as
`path::TestName/sub` is the subtest `sub` of `TestName` in that file. A
TypeScript test is cited by the name its file gives it. `untested — L<id>`
means that no test proves the behaviour, and the entry `L<id>` in
[BUGS.md](BUGS.md) names the gap.

## routd sends a message to the first route that matches

| Feature             | Behaviour today                                                                                                     | Evidence                                                                             |
| ------------------- | ------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Route table         | routd tries the route rows in order and sends the message to the first rule whose predicates all match.             | `router/router_test.go::TestResolveRouteTemplate`                                    |
| Route order         | The store sorts the rules by seq, then by id, before the router reads them.                                         | `store/routes_test.go::TestRouteTableReadsEqualSeqInIdOrder`                         |
| Platform match      | A rule with `platform=slack` sends slack chats to its folder before a later catch-all rule.                         | `tests/feat_routing_test.go::TestFeature_MessageRouting/prefix-precedence`           |
| Room match          | A rule with `room=42` sends chat `telegram:42` to its folder.                                                       | `tests/feat_routing_test.go::TestFeature_MessageRouting/room-match`                  |
| Predicate glob      | Every `key=glob` predicate must match, and `*` does not match across `/`.                                           | `router/router_test.go::TestRouteMatch_GlobFields/platform AND room match`           |
| Route miss          | routd advances the cursor without a turn when no rule matches, and queues onboarding when it is on.                 | `routd/loop_test.go::TestRouteMissDrops`                                             |
| Observe rule        | A `#observe` rule marks the messages observed, starts no turn, and advances the cursor.                             | `routd/parity_test.go::TestObserveMarksMessages`                                     |
| Engagement          | An engaged chat goes to its folder when no rule matches, and a thread falls back to the root topic.                 | `routd/parity_test.go::TestEngagementTopicRootNormalization`                         |
| Sticky group        | A bare `@folder` message pins the chat to that folder, answers "routing →" and starts no turn.                      | `routd/parity_test.go::TestStickyNavConsumed`                                        |
| Sticky topic        | A pinned topic replaces the topic of each message, so the chat runs in that topic.                                  | `routd/topic_fanout_test.go::TestDispatch_AStickyTopicStillTakesOneTurn`             |
| Reply chain         | A reply to a bot message goes to the folder in its `routed_to`, before the sticky group and the route table.        | `routd/parity_routing_test.go::TestResolveTarget_WithReplyTo`                        |
| Session per topic   | Each folder and topic pair keeps its own session, and `/new #topic` clears only that session.                       | `routd/parity_routing_test.go::TestCmdNew_WithTopic_ClearsTopicSession`              |
| One turn per thread | Two threads from one sender get one turn each, and each turn carries only the messages of its thread.               | `routd/topic_fanout_test.go::TestDispatch_TwoThreadsOneSenderGetOneTurnEach`         |
| Topic lineage       | The first turn on a new topic writes the lineage row and copies the session transcript of the parent.               | `routd/session_fork_test.go::TestEnsureTopicWithFork_CopiesParentSession`            |
| Topic fork          | `ForkTopic` records `parent_topic`, a new session id and a cursor, and refuses a duplicate without `force`.         | `store/sessions_test.go::TestForkTopic_WritesLineage`                                |
| `@child` prefix     | A leading `@child` writes the text without the prefix to the child folder and queues it.                            | `routd/parity_test.go::TestChildDelegation`                                          |
| Delegation target   | A folder that is not root delegates only to a direct child in its own world.                                        | `router/router_test.go::TestIsAuthorizedRoutingTarget/grandchild`                    |
| `#topic` prefix     | A leading `#topic` posts the text again under that topic and queues the chat.                                       | `routd/parity_test.go::TestTopicPrefix`                                              |
| Forwarding          | A delegated turn carries `forwarded_from`, and its reply goes back to the origin chat.                              | `routd/parity_test.go::TestDelegatedReplyReturnsToOrigin`                            |
| Inbound to reply    | An adapter POST to `/v1/messages` reaches the turn of the folder through runed, and one bot message is stored.      | `tests/feat_routing_test.go::TestFeature_MessageRouting/inbound-to-dispatch`         |
| Token JIDs          | routd sends a `web:` or `hook:` JID to the folder recorded at mint and does not read the route table.               | `routd/loop_test.go::TestSuffixedRouteTokenDispatchesToMintedFolder/web with suffix` |
| `web_routes`        | proxyd uses the longest matching `path_prefix` of the agent `/pub` routes, then redirects, denies or authenticates. | `proxyd/main_test.go::TestProxydPubWebRouteLongestPrefix`                            |

## runed runs one guest per turn under a cap

| Feature                  | Behaviour today                                                                                            | Evidence                                                                    |
| ------------------------ | ---------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Guest per turn           | routd posts each turn to runed, and runed records a spawn row and runs the turn.                           | `tests/feat_dispatch_test.go::TestFeature_AgentDispatch/spawn-persisted`    |
| Session persistence      | routd stores the session id of the topic before it calls runed, so a killed turn resumes the same session. | `routd/session_continuity_test.go::TestKilledTurnResumesItsOwnSession`      |
| Context mode             | An isolated scheduled task runs with no session, and a group task resumes the session of the folder.       | `routd/isolated_session_test.go::TestDispatchRun_IsolatedTaskGetsNoSession` |
| Model per group          | routd sends the model of the group, or the instance default when the group has none.                       | `routd/parity_test.go::TestDefaultModelFallback`                            |
| Observe window           | routd sizes the observed context from the first route override, else the config default.                   | `routd/parity_extra_test.go::TestObserveWindow_FirstRouteWins`              |
| Observe window per group | The group window replaces the config default, and the first route override replaces the group window.      | `routd/parity_extra_test.go::TestObserveWindow_GroupWindow`                 |
| Folder queue             | Two JIDs that map to one folder never run at the same time.                                                | `queue/queue_test.go::TestEnqueueSerializesByFolder`                        |
| Steer                    | runed gives a message for a busy folder to the live guest, and the guest wakes the agent.                  | `kernel/guest-init/main_test.go::TestSteerIsAckedWithTheRequestsID`         |
| No second spawn          | runed steers into a live run and does not start a second one.                                              | `runed/manager_test.go::TestSteerWhenLive`                                  |
| Circuit breaker in runed | Three failures in a row open the breaker of a folder, and the next inbound message closes it.              | `runed/manager_test.go::TestCircuitBreakerTripAndReset`                     |
| Circuit breaker in routd | The routd queue counts failures for each chat and opens at three.                                          | `queue/queue_test.go::TestCircuitBreaker`                                   |
| Concurrency cap          | runed admits at most the cap and answers busy to the others.                                               | `runed/queue_concurrency_test.go::TestConcurrencyCapNoOverAdmit`            |
| Run time limit           | runed turns the run time limit into the deadline that the runtime sees.                                    | `runed/manager_test.go::TestManagerAppliesRunTTLDeadline`                   |
| Kill                     | A kill marks the run killed, does not count toward the breaker, and frees the slot.                        | `runed/queue_concurrency_test.go::TestKillFreesSlotForNextRun`              |
| Spend cap                | routd refuses a turn before the spawn when the gateway reports the user or folder cap spent.               | `routd/budget_test.go::TestBudgetGate_UserCapDispatchParity`                |
| Model gateway            | runed puts `MODEL_GATEWAY_URL` and the key of the turn in the guest environment.                           | `runed/render_test.go::TestTurnEnvEmissions`                                |
| Key per turn             | routd makes one gateway key for each turn and revokes it after the turn.                                   | `routd/gateway_test.go::TestTurnMintsItsOwnKeyAndRevokesItAfter`            |
| Egress allowlist         | The egress proxy answers 403 for a host that is not on the allowlist, before it connects.                  | `egress/proxy_test.go::TestNonAllowlistedHostRefused`                       |
| Egress allow             | The egress proxy forwards HTTP to a host on the allowlist.                                                 | `egress/test/e2e_test.go::TestE2E_Case1_HTTPAllowed`                        |
| `network_rules`          | routd joins the rules of a folder and of its ancestors, excludes siblings, and sends the list to runed.    | `routd/network_test.go::TestResolveAllowlist`                               |
| Egress rule translation  | `*` turns on allow-all mode, and the gateway host joins the allowlist once.                                | `container/egress_test.go::TestEgressRulesSeparatesAllowAllFromTheNames`    |

## Each adapter turns platform events into messages and sends replies

The verbs each adapter supports are in [EXTENDING.md § Verb support matrix](EXTENDING.md#verb-support-matrix).

| Feature                | Behaviour today                                                                                               | Evidence                                                                      |
| ---------------------- | ------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| teled inbound          | teled turns a Telegram update into a message with a group JID and a `telegram:user/<id>` sender.              | `teled/integration_test.go::TestBotHandle_RoutesInbound`                      |
| teled outbound         | teled sends text to the chat id in the JID.                                                                   | `teled/integration_test.go::TestBotSend_Simple`                               |
| discd inbound          | discd turns a gateway message into a message and drops its own mention.                                       | `discd/integration_test.go::TestOnMessage_Inbound`                            |
| discd outbound         | discd posts the content to the channel and returns the message id.                                            | `discd/integration_test.go::TestSend_Success`                                 |
| slakd inbound          | slakd turns a signed Events API mention into a message with verb `mention`.                                   | `slakd/integration_test.go::TestInbound_ChannelMention`                       |
| slakd outbound         | slakd posts to the channel.                                                                                   | `slakd/integration_test.go::TestOutbound_Send`                                |
| bskyd inbound          | bskyd reads unread notifications, delivers the oldest first, and marks them seen.                             | `bskyd/xrpc_test.go::TestFetchNotifications_Dispatches`                       |
| bskyd outbound         | bskyd creates an `app.bsky.feed.post` record with the text.                                                   | `bskyd/xrpc_test.go::TestSend`                                                |
| reditd cursor          | reditd stores a cursor and advances it after each poll.                                                       | `reditd/client_test.go::TestCursorAdvance`                                    |
| reditd inbound content | reditd turns an inbox item into a message.                                                                    |                                                                               |
| reditd outbound        | reditd submits a post.                                                                                        | `reditd/bothandler_test.go::TestBotHandler_Send`                              |
| emaid inbound          | emaid turns an IMAP message into a message on `email:thread/<id>`, with verb `untrusted` unless DMARC passes. | `emaid/server_test.go::TestFetchMsgToInbound`                                 |
| emaid outbound         | emaid sends SMTP mail with `In-Reply-To` and `References` from the thread root.                               | `emaid/bothandler_test.go::TestBotHandler_Send_Success`                       |
| linkd seen             | linkd marks each comment on its own posts seen once and skips its own comments.                               | `linkd/linkd_test.go::TestDeliverComment`                                     |
| linkd inbound content  | linkd turns a comment into a message.                                                                         |                                                                               |
| linkd outbound         | linkd posts a comment on a post URN.                                                                          | `linkd/linkd_test.go::TestPostCommentOutbound`                                |
| whapd inbound          | whapd turns a WhatsApp message into a `whatsapp:` message with the sender and the group flag.                 | `whapd/src/inbound.test.ts::builds DM text payload with whatsapp: prefix`     |
| whapd outbound         | `POST /send` sends text through the WhatsApp socket.                                                          | `whapd/src/server.test.ts::sends when connected`                              |
| twitd inbound          | twitd reads mentions, delivers the oldest first, and advances the cursor to the newest.                       | `twitd/src/poll.test.ts::delivers oldest-first and advances cursor to newest` |
| twitd outbound         | `POST /send` with `reply_to` posts a reply to that tweet.                                                     | `twitd/src/server.test.ts::calls sendTweet with replyTo id`                   |

## authd signs every token and each daemon verifies it

| Feature             | Behaviour today                                                                                      | Evidence                                                                      |
| ------------------- | ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| Service token check | routd verifies the ES256 bearer against the authd keys and accepts a `messages:write` adapter token. | `tests/feat_auth_test.go::TestFeature_Auth/inbound-valid-token`               |
| Rejection           | routd answers 401 for a bad bearer and 403 for a wrong scope, and stores nothing.                    | `tests/feat_auth_test.go::TestFeature_Auth/inbound-invalid-token`             |
| Folder-bound reply  | An agent replies only to a chat routed to its folder or a descendant.                                | `ipc/authorize_jid_test.go::TestAuthorizeJID_MentionOnlySubfolder`            |
| ES256 signing       | authd signs tokens with its ES256 key, and a verifier accepts them by `kid`.                         | `auth/es256_test.go::TestSignVerifyRoundTrip`                                 |
| Key publication     | authd serves `/v1/keys`, and `FetchKeys` verifies a token against it.                                | `authd/authd_test.go::TestKeysEndpointServesJWKS`                             |
| Service token       | authd exchanges a daemon secret for a `service:<daemon>` token with the scopes of that daemon.       | `authd/authd_test.go::TestServiceTokenEndpoint`                               |
| Scope resolution    | A token with a narrow folder claim carries the full resolved grant set.                              | `tests/split_federation_test.go::TestSplitFederation_StampResolvesFullGrants` |
| OAuth login         | authd handles the Google callback, creates the user, and issues an ES256 token and a refresh token.  | `authd/oauth_test.go::TestOAuthDispatchMintsES256AndCreatesUser`              |
| Telegram login      | `POST /auth/telegram` signs a user in when `TELEGRAM_TOKEN` is set.                                  | `authd/oauth_login_test.go::TestTelegramLoginSignedWidgetSignsIn`             |
| Google allowlist    | The Google callback admits only a verified address that matches `GoogleAllowedEmails`.               | `authd/oauth_login_test.go::TestGoogleCallbackAppliesAllowedEmails`           |
| GitHub allowlist    | The GitHub callback admits only a member of `GitHubAllowedOrg`.                                      | `authd/oauth_login_test.go::TestGitHubCallbackAppliesAllowedOrg`              |
| Refresh token reuse | authd rotates the refresh token on each use, and a second use of a spent token revokes the family.   | `authd/authd_test.go::TestRefreshReuseRevokesFamily`                          |
| proxyd transit      | A backend trusts `X-User-*` only with a valid `service:proxyd` bearer.                               | `auth/transit_test.go::TestProxydTransitRejectsEveryOtherValidToken`          |

## An acl row is the only source of a permission

| Feature          | Behaviour today                                                                                    | Evidence                                                                      |
| ---------------- | -------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| ACL rows         | The store adds, reads and removes acl rows, and a repeated add changes nothing.                    | `store/acl_test.go::TestACL_CRUD`                                             |
| Deny             | A deny row wins over an allow row for the same action and scope.                                   | `auth/authorize_test.go::TestAuthorize_DenyWinsSameTriple`                    |
| Param glob       | The `params` glob of a row must match the call, and a missing param denies.                        | `auth/authorize_test.go::TestAuthorize_ParamGlob`                             |
| Scope glob       | `**` covers descendants, and a row does not match outside its subtree.                             | `auth/authorize_test.go::TestAuthorize_WildcardScope`                         |
| No fallback      | A principal with no matching row is denied.                                                        | `auth/authorize_test.go::TestAuthorizeWith_NoTierFallback`                    |
| Action lattice   | A held `*` covers every action.                                                                    | `auth/authorize_test.go::TestAuthorize_ActionLatticeStarImpliesAll`           |
| Delegation       | A granter gives only rows that it holds with the grant option.                                     | `auth/delegate_test.go::TestDelegate/no grant option → cannot delegate`       |
| No wider scope   | A granter that holds `acme/*` cannot delegate `acme/**`.                                           | `auth/delegate_test.go::TestDelegate/cannot broaden the scope glob (F3)`      |
| REST `acl:write` | `POST /v1/acl` answers 403 without the `acl:write` scope.                                          | `routd/acl_endpoint_test.go::TestACLEndpointRequiresScope`                    |
| REST containment | A folder-scoped caller grants only inside its own subtree and never `**`.                          | `routd/acl_endpoint_test.go::TestACLRESTContainmentDenied`                    |
| Role chain       | Grants flow through a chain of three roles.                                                        | `auth/authorize_test.go::TestAuthorize_RoleMembershipThreeDeep`               |
| Role cycle       | The store refuses the second of two membership edges that close a ring.                            | `store/cycle_guard_test.go::TestTwoEdgesClosingARingAreSerialisedByTheMarker` |
| Operator         | A member of `role:operator` holds `*` on `**`.                                                     | `auth/authorize_test.go::TestAuthorize_RoleMembershipOneDeep`                 |
| Overlay          | Operator rows add to the `role:member` floor: an allow adds a tool and a deny blocks a floor verb. | `routd/acl_overlay_test.go::TestOperatorOverlay`                              |

## timed fires each due task once

| Feature               | Behaviour today                                                                                       | Evidence                                                                             |
| --------------------- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Task create           | `schedule_task` stores a cron, interval or one-time task with its `next_run`, and refuses a bad cron. | `routd/scheduled_tasks_resource_test.go::TestScheduledTasksMCP_ScheduleParsesCron`   |
| Next run              | `NextRun` gives the next match of a cron, or now plus N ms for an interval.                           | `core/cron_test.go::TestNextRunBasic`                                                |
| Pause, resume, cancel | The agent tools change the status of a task, and cancel deletes it.                                   | `routd/scheduled_tasks_resource_test.go::TestScheduledTasksMCP_PauseResumeCancelOwn` |
| Reschedule            | timed writes the next run of a cron task, and marks a one-time task `completed`.                      | `timed/split_test.go::TestFireSplitOneShotCompletes`                                 |
| Recovery              | At start, the store sets tasks left in `firing` back to active.                                       | `store/tasks_test.go::TestRecoverFiringTasks`                                        |
| Due claim             | `DueTasks` claims each due task once and marks it `firing`.                                           | `store/tasks_test.go::TestDueTasks_ClaimsOnce`                                       |
| Fire                  | A tick claims a due task, queues its prompt, logs the run and reschedules.                            | `timed/split_test.go::TestFireSplitDrivesRoutd`                                      |
| Run log               | routd lists the runs of a task, newest first.                                                         | `store/task_run_logs_test.go::TestThePerTaskHistoryNamesOneTaskAndHonoursItsLimit`   |
| Run now               | `PATCH /v1/tasks/{id}` with `next_run` sets the run time, and the task fires on the next tick.        | `routd/tasks_test.go::TestTaskPatchEndpoint`                                         |

## A route token maps a URL to one folder

| Feature                  | Behaviour today                                                                               | Evidence                                                                                |
| ------------------------ | --------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Issue and resolve        | The store keeps only the sha256 of a token, and a lookup maps the token to its JID and owner. | `tests/feat_tokens_test.go::TestFeature_RouteTokens/issue-and-resolve`                  |
| Revoke                   | A revoke deletes every delivery token for the JID under that owner.                           | `tests/feat_tokens_test.go::TestFeature_RouteTokens/revoke`                             |
| List                     | The list shows the tokens of the owner without the raw value.                                 | `tests/feat_tokens_test.go::TestFeature_RouteTokens/list-and-bulk-revoke`               |
| HTTP create              | `POST /v1/route_tokens/chat` answers 201 with `routes:write` and 403 without it.              | `tests/feat_tokens_test.go::TestFeature_RouteTokens/http-create-requires-scope`         |
| Hook label               | A `source_label` outside `^[\w-]+$` gets 400.                                                 | `tests/feat_tokens_test.go::TestFeature_RouteTokens/hook-label-validation`              |
| Hook JID                 | A hook token becomes `hook:<folder>/<label>` with an optional suffix.                         | `routd/loop_test.go::TestSuffixedRouteTokenDispatchesToMintedFolder/hook with suffix`   |
| REST owner containment   | The shared REST gate answers 403 for an `owner_folder` outside the folder of the caller.      | `routd/gdrive_files_resource_test.go::TestGDriveFiles_RESTOwnerOutsideTokenFolderIs403` |
| Agent revoke containment | An agent revoke of another folder's token deletes nothing.                                    | `routd/route_tokens_resource_test.go::TestRouteTokensMCP_RevokeCrossFolderDenied`       |
| Mint containment         | The mint refuses a `target_folder` outside the grant scope of the caller.                     | `routd/route_tokens_resource_test.go::TestRouteTokensMCP_MintTargetContainment`         |
| Hook revoke by path      | `DELETE /v1/route_tokens/hook:demo/github` revokes a token whose JID contains a slash.        | `tests/feat_tokens_test.go::TestFeature_RouteTokens/http-revoke-slash-bearing-hook-jid` |
| Pairing token            | A pairing token never resolves as a delivery token.                                           | `routd/tokens_test.go::TestRouteTokenResolve_RejectsPairingKind`                        |

## onbod admits a person by invite or gate

| Feature          | Behaviour today                                                                                     | Evidence                                                                       |
| ---------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| Invite create    | The store keeps only the hash of an invite token, and the list shows the invites of the issuer.     | `store/invites_test.go::TestInviteCRUD`                                        |
| Invite use limit | An invite refuses use when `used_count` reaches `max_uses`.                                         | `store/invites_test.go::TestConsumeInviteExhausted`                            |
| Invite revoke    | A revoked invite does not redeem.                                                                   | `tests/feat_onboarding_test.go::TestFeature_Onboarding/invite-revoke`          |
| Invite expiry    | An invite whose `expires_at` is past does not redeem.                                               | `store/invites_test.go::TestConsumeInviteExpired`                              |
| Subgroup invite  | An invite whose target ends in `/` creates the new folder under its parent.                         | `onbod/invite_parent_test.go::TestRedeemedSubgroupInviteCreatesUnderItsParent` |
| Gate match       | onbod queues a paired chat when a gate matches the identity.                                        | `onbod/main_test.go::TestAdmitJIDWithGatesQueues`                              |
| No gates         | With no gates, onbod approves the chat.                                                             | `onbod/main_test.go::TestAdmitJIDApprovesAndWritesNoEdge`                      |
| No matching gate | With gates and no match, onbod refuses the chat once.                                               | `onbod/main_test.go::TestAdmitJIDWithGatesNoMatchIsRefusedTerminally`          |
| Queue drain      | onbod approves queued chats oldest first, up to the limit of each gate.                             | `onbod/main_test.go::TestAdmitFromQueue`                                       |
| Daily limit      | onbod admits no more than `limit_per_day` in one day.                                               | `onbod/main_test.go::TestAdmitFromQueueRespectsDaily`                          |
| Pairing          | onbod admits a chat once, only when a `pairing` edge exists.                                        | `onbod/main_test.go::TestObservePairingsAdmitsAPairedJID`                      |
| Group create     | onbod sets up the folder, then writes the group row, the admin grant and the username in one batch. | `onbod/main_test.go::TestCreateWorldValidUsername`                             |
| Second JID       | A paired chat of a person who administers one world goes to that world without a question.          | `onbod/main_test.go::TestSingleWorldRoutesWithoutAsking`                       |

## dashd shows and changes the instance in the browser

| Feature           | Behaviour today                                                                   | Evidence                                                                            |
| ----------------- | --------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Portal            | The portal shows group, errored chat, failed task and held approval counts.       | `dashd/coverage_test.go::TestPortalFailedTasksDot`                                  |
| Services          | The services page probes the `/health` of each daemon and shows the result.       | `dashd/services_test.go::TestServicesOperator`                                      |
| Status            | The status page shows the group count, errored chats and the active sessions.     | `dashd/answering_section_test.go::TestStatus_AnsweringSectionCountsExpiredTurns`    |
| Activity          | The activity page lists the last 50 messages with a link to older ones.           | `dashd/coverage_test.go::TestActivityPagination`                                    |
| Tasks             | The tasks page shows the owner, cron, status and next run of each task.           | `dashd/coverage_test.go::TestTasksPartialRows`                                      |
| Task create       | An admin creates a task through routd.                                            | `dashd/admin_integration_test.go::TestDash_TaskCreate`                              |
| Task status       | An admin pauses, resumes or cancels a task.                                       | `dashd/admin_integration_test.go::TestDash_TaskPauseResume`                         |
| Groups            | The groups page lists the groups that the caller can see, with their routes.      | `dashd/coverage_test.go::TestGroupsWithRoutes`                                      |
| Group create      | An admin creates a folder and the creator's `admin` grant in one transaction.     | `dashd/group_create_test.go::TestGroupCreatePage_CommitsTheAdminGrantWithTheFolder` |
| Group delete      | An admin removes the group row and its directory.                                 | `dashd/admin_integration_test.go::TestDash_GroupDelete`                             |
| Routes            | An admin creates and deletes a route.                                             | `dashd/admin_integration_test.go::TestDash_RouteCreateAndDelete`                    |
| Unrouted chats    | An operator claims an unrouted chat, and dashd writes a `chat_jid=` route.        | `dashd/unrouted_test.go::TestRoutes_ClaimWritesTheRoute`                            |
| Grants add        | An admin adds a grant row for a folder.                                           | `dashd/admin_integration_test.go::TestDash_GrantAdd`                                |
| Grants revoke     | An admin removes a grant row.                                                     | `dashd/admin_integration_test.go::TestDash_GrantRevoke`                             |
| Invites           | The invites page shows the link once, at create, and later only the ref.          | `dashd/invites_test.go::TestHandleInviteCreateAndList`                              |
| Invite revoke     | The revoke form uses the ref, and the list is then empty.                         | `dashd/invites_test.go::TestHandleInviteRevokeAndGone`                              |
| WhatsApp pairing  | An admin starts pairing through whapd, sees the code and leaves an audit row.     | `dashd/channels_test.go::TestDash_WhatsappPairStart_AdminProxiesAndAudits`          |
| Memory read       | The memory page shows `MEMORY.md` of the selected group.                          | `dashd/coverage_test.go::TestHandleMemorySelectedGroup`                             |
| Memory write      | A PUT writes the memory file after an admin check.                                | `dashd/main_test.go::TestMemoryWrite`                                               |
| Memory delete     | A DELETE removes the memory file.                                                 | `dashd/main_test.go::TestMemoryDelete`                                              |
| Profile           | The profile page shows the sub and a link button for each other provider.         | `dashd/profile_test.go::TestProfile_HidesOwnProviderOffersRest`                     |
| Personal secrets  | The secrets page lists keys without values and creates, changes and deletes them. | `dashd/me_secrets_test.go::TestMeSecrets_CreateAndList`                             |
| Secrets encrypted | dashd stores a personal secret as `v2:` ciphertext.                               | `dashd/me_secrets_byoa_test.go::TestMeSecrets_SealsAtRest`                          |
| routd page        | `/dash/routd/` shows groups, routes, errored chats and the dead-letter queue.     | `routd/dash_test.go::TestOverviewCountsRoutdsOwnState`                              |
| runed page        | `/dash/runed/` shows live and recent runs and a kill control for live runs only.  | `runed/dash_test.go::TestOnlyLiveRunsOfferKill`                                     |
| Audit             | The audit page merges the rows of routd, runed, authd and onbod, newest first.    | `dashd/audit_page_test.go::TestAuditFederatesAllFourSources`                        |
| Usage             | The usage page shows messages, tokens and cents for each group.                   | `dashd/usage_page_test.go::TestUsageWithCost`                                       |
| Folder isolation  | A folder-scoped caller sees only its subtree and gets 403 for a sibling.          | `dashd/isolation_test.go::TestIso_PerFolderGET_AliceForbiddenOnSales`               |
| Approvals         | The approvals page lists held calls and recent verdicts.                          | `dashd/approvals_page_test.go::TestApprovalsPage_RendersHeldQueue`                  |
| Engagement        | The engagement page lists the live engagement windows.                            | `dashd/engagement_page_test.go::TestEngagementPage_RendersLiveWindows`              |
| Proactive         | The proactive page shows the mode of each group and marks a bad configuration.    | `dashd/proactive_page_test.go::TestProactivePageRendersModes`                       |
| Packages          | The packages page lists installed packages with their source and revision.        | `dashd/packages_page_test.go::TestPackagesPageRows`                                 |

## An agent acts only through MCP tools that its grants show

| Feature              | Behaviour today                                                                               | Evidence                                                                            |
| -------------------- | --------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `set_observe_window` | The tool stores the caps of the group and keeps the cap that the call does not set.           | `tests/topic_lineage_test.go::TestSetObserveWindow_MCP_PersistsAndPreservesOmitted` |
| `fork_topic`         | The tool forks a child topic, and refuses a duplicate without `force`.                        | `tests/topic_lineage_test.go::TestFork_MCP_InsertsLineageAndForceOverwrites`        |
| `reply`              | The tool sends into the thread of the turn and returns the message id.                        | `ipc/ipc_test.go::TestServeMCP_Reply_ThreadsViaActiveTopic`                         |
| `send`               | The tool sends a new message. After a failed delivery, the message stays pending for a retry. | `routd/outbound_deadletter_test.go::TestMCPSendFailureLeavesPendingRowForSweep`     |
| `send_file`          | The tool sends a file from the workspace with a caption into the thread.                      | `ipc/ipc_test.go::TestServeMCP_SendFile_ThreadsViaActiveTopic`                      |
| `send_voice`         | The tool makes speech from text and sends it as a voice message.                              | `routd/voice_test.go::TestSendVoice_SynthesizesAndDelivers`                         |
| `like`               | The tool calls the adapter with the target message id.                                        | `tests/integration_mcp_test.go::TestMCP_SocialActions/like`                         |
| Social audit         | Each social verb writes an audit row with its result.                                         | `ipc/social_audit_test.go::TestSocialVerbIsAudited`                                 |
| `quote`, `repost`    | The tools record the platform ids as outbound messages.                                       | `ipc/ipc_test.go::TestServeMCP_FeedVerbs_RecordOutbound`                            |
| `post`               | The tool sends content and refuses a media path outside the group directory.                  | `ipc/untested_tools_test.go::TestPostSendsContentAndResolvesMediaInsideTheGroup`    |
| Task tools           | The task tools write to the routd store.                                                      | `routd/dark_tools_test.go::TestTaskWriteTools_WriteOwnDB`                           |
| `reset_session`      | The tool clears the session of the named folder.                                              | `ipc/untested_tools_test.go::TestResetSessionClearsTheNamedFolder`                  |
| `inspect_messages`   | The tool returns the recent messages of a chat.                                               | `tests/integration_mcp_test.go::TestMCP_MessageInspection/inspect_messages`         |
| `inspect_routing`    | The tool lists only the routes of the caller, and the whole table for root.                   | `routd/mcp_test.go::TestScopeRoutes`                                                |
| `inspect_session`    | The tool asks a resolver for the session.                                                     | `routd/dark_tools_test.go::TestInspectSession_Federated`                            |
| `inspect_identity`   | The tool resolves a sub to its identity through authd.                                        | `routd/dark_tools_test.go::TestInspectIdentity_ViaAuthd`                            |
| `find_messages`      | The tool drops hits from chats that are not routed to the folder of the caller.               | `ipc/find_messages_test.go::TestServeMCP_FindMessages_ACLDropsForeignRows`          |
| Search rank          | The search index orders hits by BM25 score.                                                   | `routd/msgsearch/msgsearch_test.go::TestRankingOrder`                               |
| ACL tools            | The tools add, list and remove acl rows in the subtree of the caller.                         | `routd/acl_resource_test.go::TestACLMCP_AddListRemove`                              |
| `invite_create`      | The agent creates an invite through onbod and gets the token once.                            | `tests/integration_mcp_test.go::TestMCP_InviteTools/invite_create`                  |
| `observe_group`      | The tool adds a watcher from the folder of the caller to a source folder.                     | `tests/integration_mcp_test.go::TestMCP_GroupObservation/observe_group`             |
| Tool list            | `tools/list` shows a tool only to a folder that holds `mcp:<tool>`.                           | `routd/routes_resource_test.go::TestRoutesMCP_Visibility`                           |
| Call deny            | An operator deny row on `mcp:reply` makes the call fail.                                      | `routd/acl_overlay_test.go::TestServeTurnMCP_OperatorDenyBlocksTool`                |

## Each turn gets the memory of its folder

| Feature             | Behaviour today                                                                      | Evidence                                                                 |
| ------------------- | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------ |
| Group watchers      | A watch merges the deliveries of the watched folder into the window of the observer. | `routd/prompt_test.go::TestObservedSince_WatchedSources`                 |
| Persona summary     | routd puts the `summary` of `PERSONA.md` into a `<persona>` block.                   | `routd/parity_prompt_test.go::TestPersonaBlock_WithSummary`              |
| System prompt order | ant puts `SYSTEM.md` first and the persona after it.                                 | `ant/src/index.test.ts::persona follows systemMd in the rendered prompt` |
| Diary               | The turn gets the summaries of the 14 newest diary entries.                          | `diary/diary_test.go::TestReadWithSummary`                               |
| Episodes            | The turn gets up to three newest episodes of each type.                              | `container/episodes_test.go::TestReadRecentEpisodes_WithFiles`           |
| Pre-compaction hook | The PreCompact hook tells the agent to run `/diary`.                                 | untested — L509                                                          |
| User tag            | The turn carries a `<user id>` tag for each sender other than `system`.              | `router/router_test.go::TestUserContextXml`                              |
| User file           | The tag gains `name` and `memory` attributes when `users/<id>.md` exists.            | untested — L509                                                          |
| Skill seeding       | Seeding writes missing skill files and does not overwrite edits.                     | `container/container_test.go::TestSeedSkills_PreservesOursOnRerun`       |
| `/solve` prefix     | runed starts every prompt with the `/solve` instruction.                             | `runed/render_test.go::TestPreparePayloadWithoutLayers`                  |
| Migration check     | routd queues `/migrate` for each group with an old skill version.                    | `routd/migrate_test.go::TestCheckMigrationVersion`                       |

## webd serves the chat widget and the webhook URL

| Feature           | Behaviour today                                                                                           | Evidence                                                            |
| ----------------- | --------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| Chat widget       | `GET /chat/<token>/` serves the widget for a known token and 404 for an unknown one.                      | `webd/route_token_test.go::TestHandleChatTokenRoot`                 |
| Slink             | `/slink/<token>/...` answers 308 to `/chat/<token>/...` and keeps the method.                             | `webd/slink_shim_test.go::TestSlinkShimPreservesPOSTAndCarriesCORS` |
| Chat list         | webd lists the folders and topics of the caller, newest first.                                            | `webd/me_test.go::TestMeXChats_Partial`                             |
| Turn stream       | The stream of a turn uses the chat JID as its key and closes on the matching `round_done`.                | `webd/turn_test.go::TestTurnSSE_JidSuffixToken`                     |
| `round_done`      | `POST /v1/round_done` publishes the event on the chat JID and topic.                                      | `webd/api_test.go::TestHandleRoundDone`                             |
| Webhook           | `POST /hook/<token>` sends the body to routd as a message and answers 204.                                | `webd/route_token_test.go::TestE2EHookTokenPost`                    |
| Web presence      | `get_web_presence` gives the host, `/pub` path and `/priv` base of a folder in the subtree of the caller. | `ipc/web_presence_test.go::TestServeMCP_WebPresence_Derived`        |
| Web presence REST | `GET /v1/web_presence` gives the same answer and 403 across folders.                                      | `routd/web_presence_test.go::TestRESTWebPresence`                   |

## The core daemons report health, and a change writes an audit row

| Feature                    | Behaviour today                                                            | Evidence                                                                      |
| -------------------------- | -------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| routd health               | `/health` answers 200 without a token.                                     | `tests/feat_health_test.go::TestFeature_HealthEndpoints/routd-health-no-auth` |
| runed health               | `/health` answers 200 before the auth gate.                                | `tests/feat_health_test.go::TestFeature_HealthEndpoints/runed-health`         |
| proxyd health              | `/health` answers 200.                                                     | `proxyd/integration_test.go::TestHealthEndpoint`                              |
| dashd health               | `/health` answers `{"ok":true}`.                                           | `dashd/main_test.go::TestDashHealth`                                          |
| webd health                | `/health` reports the adapter data.                                        | `webd/api_test.go::TestHandleHealth`                                          |
| authd, onbod, timed health | Each daemon serves `/health`.                                              | untested — L510                                                               |
| Audit row                  | `audit.Emit` writes a row with category, action, actor and instance.       | `audit/log_test.go::TestEmit_Inserts`                                         |
| Audit with mutation        | The audit row commits with its change, and a refused change leaves no row. | `audit/audit_log_test.go::TestTheAuditRowIsOneCommitWithItsMutation`          |
| Audit redaction            | Sensitive param keys appear as `<redacted:...>`.                           | `audit/log_test.go::TestRedactParams_HidesSensitiveKeys`                      |
| Metrics                    | With `METRICS_ENABLED`, `/metrics` shows the `arizuko_*` families.         | `obs/metrics_test.go::TestMetricsHandler_EnabledExposesFamilies`              |
| OTLP                       | With `OTEL_EXPORTER_OTLP_ENDPOINT`, log records also go to the exporter.   | `obs/obs_test.go::TestSetup_WithEnv_FanOut`                                   |
| Turn key record            | routd records the hash of the gateway key of each turn.                    | `routd/gateway_test.go::TestTurnRecordsTheKeyThatMeteredIt`                   |
| Spend read                 | `GET /v1/cost` adds the gateway usage of the key of a turn.                | `routd/gateway_test.go::TestKeySpendSumsTheModelsAndRoundsTheTotalOnce`       |
| Spend containment          | A caller outside the folder of the turn gets 403.                          | `routd/authz_containment_test.go::TestRESTCostRead`                           |
| Turn result                | routd stores the session id and cost of a turn and drops a second report.  | `routd/contract_test.go::TestResultRecordsOutcome`                            |

## The daemons connect through fixed contracts

| Feature                | Behaviour today                                                                                       | Evidence                                                               |
| ---------------------- | ----------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| Ingress key            | `POST /v1/messages` makes the id from `X-Idempotency-Key` and refuses an id and a key together.       | `routd/fixes_test.go::TestIngressIdempotencyKey`                       |
| Redelivery             | A second delivery of one message id adds no second row.                                               | `store/store_test.go::TestPutMessage_RedeliveryIsStillANoOp`           |
| Web route tools        | `set_web_route`, `del_web_route` and `list_web_routes` change the rows of the folder of the caller.   | `routd/web_routes_resource_test.go::TestWebRoutesMCP_CreateListDelete` |
| Web route fall-through | proxyd sends a `/pub` request with no matching row to vited.                                          | `proxyd/main_test.go::TestProxydPubWebRouteNoMatchProxies`             |
| proxyd routes          | proxyd uses the longest matching path prefix.                                                         | `proxyd/routes_test.go::TestMatchRoute_LongestPrefix_Wins`             |
| WebDAV guard           | proxyd blocks writes to `.env`, `*.pem`, `.git` and `logs/` under `/dav/`.                            | `proxyd/main_test.go::TestProxydDavBlocksSensitiveWrites`              |
| MCP connectors         | routd registers the tools of a remote MCP server for a folder and removes the secret from the result. | `ipc/connector_test.go::TestConnector_EndToEndThroughMCPSocket`        |
| Transcription          | routd sends a voice attachment to whisper and puts the text in the message.                           | `routd/voice_test.go::TestEnrich_TranscribesVoiceAttachment`           |
| ttsd                   | ttsd forwards `POST /v1/audio/speech` to the backend.                                                 | `ttsd/main_test.go::TestProxyForwardsSpeech`                           |
| Compose                | `Generate` writes the core daemons without a docker socket and with a `/health` check.                | `compose/compose_test.go::TestGenerateBaseDaemons`                     |
