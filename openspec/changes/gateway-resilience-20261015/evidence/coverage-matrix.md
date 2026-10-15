# Spec scenario coverage

Every scenario of the eight specs, mapped to the automated tests that cover it and to the staging checks (`tools/gateway-resilience/staging`: `scenarios.py` IDs, `clients.py` real-client cases `C-*`, and the soak). Go tests run with `-tags=unit` unless noted. Staging results are recorded in the private staging report.

## upstream-failure-classification

| Scenario | Unit tests | Staging |
|---|---|---|
| Balance message wrapped in a 429 | `TestBalanceWrappedIn429`, `TestClassifySpecScenarios` | BAL-1 |
| Balance failure inside a stream | `TestApplyResilienceOutcomeStreamBalanceAndStall`, `TestClassifySpecScenarios` | — |
| Busy message inside a stream | `TestClassifySpecScenarios` | C-codex-busy-stream |
| Relay timeout page | `TestClassifySpecScenarios`, `TestRetryAfterFrom` | SW-1, BUD-1 |
| Request-scoped overload does not punish the account | `TestInServiceRetryOnlyForCapacityShedding`, `TestCategoryPolicyTable` | — |
| Client errors never switch accounts | `TestCategoryPolicyTable` | CTX-1 |
| User out of funds | `TestUserBalanceErrorsCarryCodes` | — |
| New relay wording | `TestAdminOverrideIsRespected`, `TestClassifySpecScenarios` | — |

## account-penalties-and-balance-recovery

| Scenario | Unit tests | Staging |
|---|---|---|
| Soft cooldown in a multi-account group | `TestResiliencePenaltyOfTiers`, `TestTransientCooldownOpenAIOnlyWithRetryAfter` | SW-1, C-codex-fail-first |
| Hard state is never used | `TestGatewayLastResortNeverUsesHardStates`, `TestResultHard` | — |
| Relay asks for 60 seconds | `TestTransientCooldownHonoursRetryAfter` | RL-1 |
| Claude relay runs out of credit | `TestStrongSignalExhausts`, `TestBalance403ExhaustsWithoutErrorState` | BAL-1 |
| Automatic recovery after top-up | `TestProbePositiveEntersRecovering`, `TestProbePositiveRecovers`, `TestRecoveringSuccessesReturnToOK` | BAL-1, soak (hourly) |
| Administrator confirms a top-up | `TestRecheckBalanceForcesProbe` | BAL-2 |
| Probe finds invalid credentials | `TestProbeAuthFailureFollowsAuthPath` | — |
| Two instances schedule the same probe | `TestProbeSingleFlight` | — |
| One 403 on an Anthropic relay | `TestUniform403CountedCooldown` | — |

## session-failure-handling

| Scenario | Unit tests | Staging |
|---|---|---|
| Replacement respects tiers | `TestGatewayLastResortPrefersHighestTierThenSoonest`, `TestAvoidedAccountExcludedButUsableAsLastResort` | — |
| One busy failure keeps the session | `TestSessionFailureStreakRebindsAndAvoids` | — |
| Second failure moves the session | `TestSessionFailureStreakRebindsAndAvoids` | — |
| Balance exhaustion moves the session at once | `TestGatewayHardBoundAccountRebinds` | — |
| Saturation does not rebind | Saturation never reaches the session failure recorder; existing spill behaviour: `TestShareSchedulingSpillsAndRebindsWhenBoundIsSaturated` | soak |
| Original scheduler during a cooldown | `TestGatewaySoftBoundAccountKeepsBinding` | — |
| Single-account Grok group with a transient error | `TestGatewayLastResortSingleAccountTransient`, `TestOpenAILastResortSingleAccount` | — |
| Hard state with no alternative | `TestGatewayLastResortNeverUsesHardStates` | — |
| Emergency off | `TestMasterSwitchDisablesEverything`, `TestResilienceOffKeepsLegacyPenalties`, `TestRetryHintsOffRestoresLegacy`, `TestTurnCapOffKeepsRetryAfter` | — |

## request-budget-and-retry

| Scenario | Unit tests | Staging |
|---|---|---|
| Four attempts at most | `TestBudgetFourAttemptsAtMost` | BUD-1 |
| Deadline reached before output | `TestBudgetDeadlineEndsBeforeNextAttempt` | FO-1 |
| Images are exempt from the deadline | `TestBudgetImagesHaveNoDeadline` | IMG-1 |
| Insufficient balance is not retried on the same account | `TestBudgetBalanceNotRetriedOnSameAccount` | — |
| Client leaves during a Gemini back-off | `TestGeminiBackoffStopsWhenClientLeaves` | — |
| Upstream asks for 120 s | `TestBudgetUpstreamRetryAfterBeyondCapSwitches` | — |
| Shared relay overload | `TestBudgetCapacityShedSwitchesOnceToAnotherHost` | — |

## pre-output-keepalive

| Scenario | Unit tests | Staging |
|---|---|---|
| Internal client is never marked | `TestResilienceEdgeMarkerTrustsOnlyProxies` | KA-2, CF-2 |
| Spoofed marker is ignored | `TestResilienceEdgeMarkerTrustsOnlyProxies` | CF-5 |
| Fast failure keeps its status code | `TestPreOutputKeepaliveUncommittedKeepsStatusCode` | ERR-1, BUD-1 |
| Slow upstream through Cloudflare | `TestArmPreOutputKeepaliveConditions` | KA-1, KA-3, KA-6, CF-1, CF-4, C-codex-slow-first-cdn |
| Official OAuth account | `TestPreOutputKeepaliveStopsUncommittedForOAuth` | — |
| Gemini is excluded | `TestArmPreOutputKeepaliveConditions` | GEM-1 |
| Failover after heartbeats | `TestHeartbeatThenFailoverKeepsSwitching` | — |
| Slow relay is abandoned in time | `TestFirstOutputTimeoutTiers`, `TestFirstOutputTimeoutMeasuredFromAttemptStart` | FO-1 |
| Last candidate waits for the budget | `TestSoleCandidateIsLastCandidate`, `TestOpenAILastResortSingleAccount` | C-codex-slow-first-cdn, C-openai-py-slow-first-cdn |
| Keep-alive before response.created | `TestRelayKeepaliveFramesAreDropped` | KA-7, C-openai-py-fail-first |
| Keep-alive does not disarm failover | `TestRelayKeepaliveFramesAreDropped` | KA-7 |
| Stall detected within the threshold | `TestStallDetectedWithinFiveSecondsAfterOutput`, `TestStallCheckPeriodAndThreshold` | ST-1 |
| Anthropic error after a ping | `TestPreOutputKeepaliveAnthropicPingsAndFailoverSize`, `TestTerminalFrameAnthropicHasEventLine` | ANT-1 |
| Codex sees the failure promptly | `TestPreOutputKeepaliveRewritesCommittedErrors`, `TestCommittedFailureCarriesRetryHint` | KA-4 |

## client-disconnect-and-billing

| Scenario | Unit tests | Staging |
|---|---|---|
| Heartbeats only | `TestDeliveryWriterExcludesHeartbeats`, `TestUndeliveredRequestIsNotBilled` | DC-1 |
| CDN cuts a slow request | `TestUndeliveredDisconnectCancelsUpstream` | DC-1 |
| XY-image reconciliation stays correct | — | XY-image lab (tasks 12.8) |
| Long generation abandoned midway | `TestPartialDisconnectDrainsUpstream` | DC-4 |
| Key keeps disconnecting before output | `TestUndeliveredAbuseBillsUpstreamCost`, `TestResolveDisconnectAbuseGuard` | DC-2 |
| Disconnect during header wait | `TestUpstreamContextOnDisconnect` | DC-3 |

## error-semantics-and-storm-protection

| Scenario | Unit tests | Staging |
|---|---|---|
| Pricing restriction keeps 503 | `TestConfigErrorRetryHeaders` | ERR-2 |
| Codex backs off instead of hammering | `TestNoAccountResponseCarriesRetryAfter`, `TestExhaustedBadGatewayCapsUpstreamRetryAfter`, `TestEdgeRequestsGet503InsteadOf502` | ERR-1, CF-3 |
| Burst of retries | `TestNegativeCacheBurstOfRetries` | — |
| Retry storm on a single-account group | `TestFastFailRetryStormOnSingleAccountGroup`, `TestFastFailIgnoresCappedTurns` | ERR-4 |
| Codex stops after five requests | `TestTurnCapEndsCodexTurn`, `TestTurnCapCountsConsecutiveFailures` | ERR-3, C-codex-all-fail |
| In-stream failures end the turn | `TestTurnCapNoAccountAndCommittedStream` | C-codex-busy-stream |
| Recovery is not delayed | `TestTurnCapResetOnlyOnRealSuccess` | — |
| Image account out of credit | `TestHideUpstreamBalanceOnExhaustedFailover` | — |
| Concurrent requests with a small balance | `TestUserBalanceErrorsCarryCodes`, `TestReservationErrorDistinguishesHeldBalance` | RES-1 |
| Cached Codex conversation | `TestCacheAwareReservationEstimate`, `TestOutputP99CapsReservationOutput` | — |
| Re-submitted batch | `TestAsyncImageV2AdmissionLimit`, `TestPromptAuditGatePrecedesAccountBillingAndUpstreamSideEffects` (admission is the only call allowed before the audit) | — |

## availability-fallback-group

| Scenario | Unit tests | Staging |
|---|---|---|
| Cycle rejected | `TestValidateAvailabilityFallbackGroup` | — |
| Primary has only soft-penalised accounts | `TestAvailabilityFallbackOnlySoftPenalties`, `TestAvailabilityFallbackMiddlewareMovesKey` | FB-1, FB-2 |
| Failover exhaustion does not cross groups | The fallback is chosen once, before the handler | FB-2 |
| User sees fallback pricing | `TestUsageRecordsOriginGroup`, `TestCollectUsageLogIDsIncludesOriginGroups` | FB-1, FB-2 |
| Primary recovers | `TestAvailabilityFallbackReturnAndFlapping` | — |
| Flapping primary | `TestAvailabilityFallbackReturnAndFlapping` | — |
