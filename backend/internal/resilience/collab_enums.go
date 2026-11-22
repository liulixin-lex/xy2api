package resilience

// Collaboration enums are fixed wire values. Never cast upstream text to these
// types for logging or persistence; classifiers map it to a known constant.

type CollabRole string

const (
	CollabRoleRoot    CollabRole = "root"
	CollabRoleChild   CollabRole = "child"
	CollabRoleUnknown CollabRole = "unknown"
)

type CollabRequestKind string

const (
	CollabRequestKindNormal           CollabRequestKind = "normal"
	CollabRequestKindCompaction       CollabRequestKind = "compaction"
	CollabRequestKindReview           CollabRequestKind = "review"
	CollabRequestKindStructuredOutput CollabRequestKind = "structured_output"
)

type CollabRoute string

const (
	CollabRouteResponsesHttp  CollabRoute = "responses_http"
	CollabRoutePassthrough    CollabRoute = "passthrough"
	CollabRouteHttpToWs       CollabRoute = "http_to_ws"
	CollabRouteWs             CollabRoute = "ws"
	CollabRouteCompact        CollabRoute = "compact"
	CollabRouteChatBridge     CollabRoute = "chat_bridge"
	CollabRouteMessagesBridge CollabRoute = "messages_bridge"
	CollabRouteOther          CollabRoute = "other"
)

type CollabClient string

const (
	CollabClientCodexCli    CollabClient = "codex_cli"
	CollabClientCodexApp    CollabClient = "codex_app"
	CollabClientCodexVscode CollabClient = "codex_vscode"
	CollabClientOther       CollabClient = "other"
	CollabClientUnknown     CollabClient = "unknown"
)

type CollabVersionState string

const (
	CollabVersionStateParsed  CollabVersionState = "parsed"
	CollabVersionStateMissing CollabVersionState = "missing"
	CollabVersionStateInvalid CollabVersionState = "invalid"
)

type CollabFinalMode string

const (
	CollabFinalModePlaintext   CollabFinalMode = "plaintext"
	CollabFinalModeCiphertext  CollabFinalMode = "ciphertext"
	CollabFinalModePlaceholder CollabFinalMode = "placeholder"
	CollabFinalModeLocal       CollabFinalMode = "local"
	CollabFinalModeMixed       CollabFinalMode = "mixed"
	CollabFinalModeNone        CollabFinalMode = "none"
)

type CollabPlanOutcome string

const (
	CollabPlanOutcomeApplied     CollabPlanOutcome = "applied"
	CollabPlanOutcomeAbsent      CollabPlanOutcome = "absent"
	CollabPlanOutcomeConflict    CollabPlanOutcome = "conflict"
	CollabPlanOutcomeUnsupported CollabPlanOutcome = "unsupported"
	CollabPlanOutcomeInvalid     CollabPlanOutcome = "invalid"
)

type CollabPlanReason string

const (
	CollabPlanReasonNone             CollabPlanReason = "none"
	CollabPlanReasonNoNamespace      CollabPlanReason = "no_namespace"
	CollabPlanReasonInvalidJson      CollabPlanReason = "invalid_json"
	CollabPlanReasonDuplicateKey     CollabPlanReason = "duplicate_key"
	CollabPlanReasonStateful         CollabPlanReason = "stateful"
	CollabPlanReasonUnsupportedShape CollabPlanReason = "unsupported_shape"
	CollabPlanReasonAliasCollision   CollabPlanReason = "alias_collision"
	CollabPlanReasonNamespaceUpdate  CollabPlanReason = "namespace_update"
	CollabPlanReasonSchemaInvalid    CollabPlanReason = "schema_invalid"
	CollabPlanReasonToolDuplicate    CollabPlanReason = "tool_duplicate"
	CollabPlanReasonHistoryIdentity  CollabPlanReason = "history_identity"
	CollabPlanReasonControlText      CollabPlanReason = "control_text"
	CollabPlanReasonClientTooOld     CollabPlanReason = "client_too_old"
	CollabPlanReasonEditFailed       CollabPlanReason = "edit_failed"
)

type CollabEligibility string

const (
	CollabEligibilityEligible           CollabEligibility = "eligible"
	CollabEligibilityMasterOff          CollabEligibility = "master_off"
	CollabEligibilityFeatureOff         CollabEligibility = "feature_off"
	CollabEligibilityAccountOff         CollabEligibility = "account_off"
	CollabEligibilityModelNotListed     CollabEligibility = "model_not_listed"
	CollabEligibilityClientUnrecognized CollabEligibility = "client_unrecognized"
	CollabEligibilityDownstreamWs       CollabEligibility = "downstream_ws"
	CollabEligibilityUpstreamWs         CollabEligibility = "upstream_ws"
	CollabEligibilityPassthrough        CollabEligibility = "passthrough"
	CollabEligibilityCompact            CollabEligibility = "compact"
	CollabEligibilityBridge             CollabEligibility = "bridge"
	CollabEligibilityNotOpenai          CollabEligibility = "not_openai"
	CollabEligibilityFlatten            CollabEligibility = "flatten"
)

type CollabRestoreError string

const (
	CollabRestoreErrorNone               CollabRestoreError = "none"
	CollabRestoreErrorInvalidJson        CollabRestoreError = "invalid_json"
	CollabRestoreErrorAmbiguousFields    CollabRestoreError = "ambiguous_fields"
	CollabRestoreErrorInvalidItem        CollabRestoreError = "invalid_item"
	CollabRestoreErrorInvalidOutput      CollabRestoreError = "invalid_output"
	CollabRestoreErrorIdentityInvalid    CollabRestoreError = "identity_invalid"
	CollabRestoreErrorNamespaceAmbiguous CollabRestoreError = "namespace_ambiguous"
	CollabRestoreErrorIdentifierConflict CollabRestoreError = "identifier_conflict"
	CollabRestoreErrorIdentityChanged    CollabRestoreError = "identity_changed"
	CollabRestoreErrorEchoAmbiguous      CollabRestoreError = "echo_ambiguous"
	CollabRestoreErrorEditFailed         CollabRestoreError = "edit_failed"
)

type CollabMarkerReason string

const (
	CollabMarkerReasonNonemptyEvidence CollabMarkerReason = "nonempty_evidence"
	CollabMarkerReasonAbnormalType     CollabMarkerReason = "abnormal_type"
	CollabMarkerReasonAnonymous        CollabMarkerReason = "anonymous"
	CollabMarkerReasonExtraEncrypted   CollabMarkerReason = "extra_encrypted"
	CollabMarkerReasonUnsafeArguments  CollabMarkerReason = "unsafe_arguments"
	CollabMarkerReasonOpaqueMessage    CollabMarkerReason = "opaque_message"
	CollabMarkerReasonChangedArguments CollabMarkerReason = "changed_arguments"
	CollabMarkerReasonStrictVeto       CollabMarkerReason = "strict_veto"
)

type CollabAction string

const (
	CollabActionOriginal        CollabAction = "original"
	CollabActionSwitchOriginal  CollabAction = "switch_original"
	CollabActionProbeReasoning  CollabAction = "probe_reasoning"
	CollabActionNamedRepair     CollabAction = "named_repair"
	CollabActionDropUnprotected CollabAction = "drop_unprotected"
	CollabActionReplaceAll      CollabAction = "replace_all"
	CollabActionPendingRetry    CollabAction = "pending_retry"
	CollabActionFloorRepair     CollabAction = "floor_repair"
	CollabActionGenericFailover CollabAction = "generic_failover"
	CollabActionFieldRepair     CollabAction = "field_repair"
	CollabActionFcoRepair       CollabAction = "fco_repair"
	CollabActionOther           CollabAction = "other"
)

type CollabSendResult string

const (
	CollabSendResultAccepted          CollabSendResult = "accepted"
	CollabSendResultCipherRejected    CollabSendResult = "cipher_rejected"
	CollabSendResultUncertainRejected CollabSendResult = "uncertain_rejected"
	CollabSendResultUnrelatedRejected CollabSendResult = "unrelated_rejected"
	CollabSendResultTransientError    CollabSendResult = "transient_error"
	CollabSendResultCanceled          CollabSendResult = "canceled"
	CollabSendResultNotSent           CollabSendResult = "not_sent"
)

type CollabReason string

const (
	CollabReasonNone                  CollabReason = "none"
	CollabReasonAgentDecrypt          CollabReason = "agent_decrypt"
	CollabReasonToolDecrypt           CollabReason = "tool_decrypt"
	CollabReasonReasoningDecrypt      CollabReason = "reasoning_decrypt"
	CollabReasonCompactionDecrypt     CollabReason = "compaction_decrypt"
	CollabReasonCallArgsDecrypt       CollabReason = "call_args_decrypt"
	CollabReasonRelayOwnership        CollabReason = "relay_ownership"
	CollabReasonRelayUnknownSuffix    CollabReason = "relay_unknown_suffix"
	CollabReasonAmbiguousWrapper      CollabReason = "ambiguous_wrapper"
	CollabReasonUnrelated             CollabReason = "unrelated"
	CollabReasonUnlocatable           CollabReason = "unlocatable"
	CollabReasonReviewRequest         CollabReason = "review_request"
	CollabReasonContinuationPointer   CollabReason = "continuation_pointer"
	CollabReasonPostOutput            CollabReason = "post_output"
	CollabReasonCallArgsUnreplaceable CollabReason = "call_args_unreplaceable"
	CollabReasonTreeLocalCap          CollabReason = "tree_local_cap"
	CollabReasonBudgetTime            CollabReason = "budget_time"
	CollabReasonBudgetSends           CollabReason = "budget_sends"
	CollabReasonNoAccount             CollabReason = "no_account"
	CollabReasonRelayCapacity         CollabReason = "relay_capacity"
	CollabReasonQueueTimeout          CollabReason = "queue_timeout"
	CollabReasonModerationBlock       CollabReason = "moderation_block"
	CollabReasonUnsupportedRoute      CollabReason = "unsupported_route"
	CollabReasonCanceled              CollabReason = "canceled"
	CollabReasonWriteFailed           CollabReason = "write_failed"
	CollabReasonOther                 CollabReason = "other"
)

type CollabPosition string

const (
	CollabPositionMessage  CollabPosition = "message"
	CollabPositionCallArgs CollabPosition = "call_args"
)

type CollabForm string

const (
	CollabFormPlaintext  CollabForm = "plaintext"
	CollabFormCiphertext CollabForm = "ciphertext"
)

type CollabMessageKind string

const (
	CollabMessageKindTask       CollabMessageKind = "task"
	CollabMessageKindMessage    CollabMessageKind = "message"
	CollabMessageKindToolOutput CollabMessageKind = "tool_output"
	CollabMessageKindUnknown    CollabMessageKind = "unknown"
)

type CollabItemClass string

const (
	CollabItemClassTerminated    CollabItemClass = "terminated"
	CollabItemClassProtected     CollabItemClass = "protected"
	CollabItemClassUnreadMessage CollabItemClass = "unread_message"
	CollabItemClassReplaceable   CollabItemClass = "replaceable"
)

type CollabDeliveryOutcome string

const (
	CollabDeliveryOutcomeAcceptedOriginal  CollabDeliveryOutcome = "accepted_original"
	CollabDeliveryOutcomePlaceholder       CollabDeliveryOutcome = "placeholder"
	CollabDeliveryOutcomePlaceholderReplay CollabDeliveryOutcome = "placeholder_replay"
	CollabDeliveryOutcomeNoticePending     CollabDeliveryOutcome = "notice_pending"
	CollabDeliveryOutcomeNoticeWrittenUser CollabDeliveryOutcome = "notice_written_user"
	CollabDeliveryOutcomeFailed            CollabDeliveryOutcome = "failed"
	CollabDeliveryOutcomeUndeliverable     CollabDeliveryOutcome = "undeliverable"
)

type CollabEventSource string

const (
	CollabEventSourceIssued           CollabEventSource = "issued"
	CollabEventSourceSeenParentNotice CollabEventSource = "seen_parent_notice"
	CollabEventSourceSeenOther        CollabEventSource = "seen_other"
)

type CollabHintKind string

const (
	CollabHintKindConsumer       CollabHintKind = "consumer"
	CollabHintKindIssuer         CollabHintKind = "issuer"
	CollabHintKindPlaintextRoute CollabHintKind = "plaintext_route"
)

type CollabCooldown string

const (
	CollabCooldownNone  CollabCooldown = "none"
	CollabCooldownLe10s CollabCooldown = "le_10s"
	CollabCooldownGt10s CollabCooldown = "gt_10s"
	CollabCooldownHard  CollabCooldown = "hard"
)

type CollabHintAttempt string

const (
	CollabHintAttemptNotAttempted CollabHintAttempt = "not_attempted"
	CollabHintAttemptSelected     CollabHintAttempt = "selected"
	CollabHintAttemptFellThrough  CollabHintAttempt = "fell_through"
)

type CollabExecution string

const (
	CollabExecutionNotStarted CollabExecution = "not_started"
	CollabExecutionPartial    CollabExecution = "partial"
	CollabExecutionUnknown    CollabExecution = "unknown"
)

type CollabEvidenceGrade string

const (
	CollabEvidenceGradeVerified  CollabEvidenceGrade = "verified"
	CollabEvidenceGradeClaimed   CollabEvidenceGrade = "claimed"
	CollabEvidenceGradeUnclaimed CollabEvidenceGrade = "unclaimed"
)

type CollabEvidenceSource string

const (
	CollabEvidenceSourceHttpError        CollabEvidenceSource = "http_error"
	CollabEvidenceSourceStreamError      CollabEvidenceSource = "stream_error"
	CollabEvidenceSourceAggregatedStream CollabEvidenceSource = "aggregated_stream"
	CollabEvidenceSourceFailedJson       CollabEvidenceSource = "failed_json"
	CollabEvidenceSourceOther            CollabEvidenceSource = "other"
)

type CollabFamily string

const (
	CollabFamilyNone           CollabFamily = "none"
	CollabFamilyAgentMessage   CollabFamily = "agent_message"
	CollabFamilyToolOutput     CollabFamily = "tool_output"
	CollabFamilyReasoning      CollabFamily = "reasoning"
	CollabFamilyCompaction     CollabFamily = "compaction"
	CollabFamilyCallArgs       CollabFamily = "call_args"
	CollabFamilyOwnership      CollabFamily = "ownership"
	CollabFamilyOtherEncrypted CollabFamily = "other_encrypted"
)

type CollabOwnershipClass string

const (
	CollabOwnershipClassNone           CollabOwnershipClass = "none"
	CollabOwnershipClassUnknownSuffix  CollabOwnershipClass = "unknown_suffix"
	CollabOwnershipClassContinuation   CollabOwnershipClass = "continuation"
	CollabOwnershipClassPending        CollabOwnershipClass = "pending"
	CollabOwnershipClassCapacity       CollabOwnershipClass = "capacity"
	CollabOwnershipClassSourceConflict CollabOwnershipClass = "source_conflict"
	CollabOwnershipClassRecognized     CollabOwnershipClass = "recognized"
)

type CollabScope string

const (
	CollabScopeFragment  CollabScope = "fragment"
	CollabScopeWholeItem CollabScope = "whole_item"
)

type CollabStage string

const (
	CollabStageOriginal   CollabStage = "original"
	CollabStageSwitch     CollabStage = "switch"
	CollabStageProbe      CollabStage = "probe"
	CollabStageNamed      CollabStage = "named"
	CollabStageDrop       CollabStage = "drop"
	CollabStageReplaceAll CollabStage = "replace_all"
	CollabStageFloor      CollabStage = "floor"
	CollabStageReplay     CollabStage = "replay"
	CollabStageLocal      CollabStage = "local"
)

type CollabEditKind string

const (
	CollabEditKindStripCipher    CollabEditKind = "strip_cipher"
	CollabEditKindPlaceholder    CollabEditKind = "placeholder"
	CollabEditKindDeleteItem     CollabEditKind = "delete_item"
	CollabEditKindProtocolRepair CollabEditKind = "protocol_repair"
)

type CollabSendStatus string

const (
	CollabSendStatusReserved  CollabSendStatus = "reserved"
	CollabSendStatusConfirmed CollabSendStatus = "confirmed"
	CollabSendStatusReleased  CollabSendStatus = "released"
	CollabSendStatusSettled   CollabSendStatus = "settled"
)

type CollabNextKind string

const (
	CollabNextKindSelectAccount CollabNextKind = "select_account"
	CollabNextKindLegacy        CollabNextKind = "legacy"
	CollabNextKindSend          CollabNextKind = "send"
	CollabNextKindLocal         CollabNextKind = "local"
	CollabNextKindDeliver       CollabNextKind = "deliver"
	CollabNextKindFloor         CollabNextKind = "floor"
	CollabNextKindUndeliverable CollabNextKind = "undeliverable"
)

type CollabIssuedStatus string

const (
	CollabIssuedStatusPending   CollabIssuedStatus = "pending"
	CollabIssuedStatusConfirmed CollabIssuedStatus = "confirmed"
	CollabIssuedStatusVoid      CollabIssuedStatus = "void"
)

type CollabPlaceholder string

const (
	CollabPlaceholderUnreadTask    CollabPlaceholder = "unread_task"
	CollabPlaceholderProcessedTask CollabPlaceholder = "processed_task"
	CollabPlaceholderMessage       CollabPlaceholder = "message"
	CollabPlaceholderToolOutput    CollabPlaceholder = "tool_output"
	CollabPlaceholderCompaction    CollabPlaceholder = "compaction"
	CollabPlaceholderLegacy        CollabPlaceholder = "legacy"
)

type CollabTreeHintStatus string

const (
	CollabTreeHintStatusProvisional CollabTreeHintStatus = "provisional"
	CollabTreeHintStatusConfirmed   CollabTreeHintStatus = "confirmed"
)

type CollabLeaseRelease string

const (
	CollabLeaseReleaseConsumed   CollabLeaseRelease = "consumed"
	CollabLeaseReleaseTerminated CollabLeaseRelease = "terminated"
)

type CollabDeliveryPhase string

const (
	CollabDeliveryPhaseReceived     CollabDeliveryPhase = "received"
	CollabDeliveryPhaseAboutToWrite CollabDeliveryPhase = "about_to_write"
	CollabDeliveryPhaseWritten      CollabDeliveryPhase = "written"
	CollabDeliveryPhaseCompleted    CollabDeliveryPhase = "completed"
)

type CollabDigestDomain string

const (
	CollabDigestDomainRejection  CollabDigestDomain = "rejection"
	CollabDigestDomainBody       CollabDigestDomain = "body"
	CollabDigestDomainThread     CollabDigestDomain = "thread"
	CollabDigestDomainTree       CollabDigestDomain = "tree"
	CollabDigestDomainParent     CollabDigestDomain = "parent"
	CollabDigestDomainDelivery   CollabDigestDomain = "delivery"
	CollabDigestDomainItem       CollabDigestDomain = "item"
	CollabDigestDomainCiphertext CollabDigestDomain = "ciphertext"
	CollabDigestDomainArguments  CollabDigestDomain = "arguments"
	CollabDigestDomainCall       CollabDigestDomain = "call"
	CollabDigestDomainAccount    CollabDigestDomain = "account"
	CollabDigestDomainSite       CollabDigestDomain = "site"
	CollabDigestDomainEvent      CollabDigestDomain = "event"
	CollabDigestDomainScope      CollabDigestDomain = "scope"
)

type CollabTransformationReason string

const (
	CollabTransformationReasonReasoningRemoved  CollabTransformationReason = "reasoning_removed"
	CollabTransformationReasonCompactionRemoved CollabTransformationReason = "compaction_removed"
	CollabTransformationReasonEncryptedRemoved  CollabTransformationReason = "encrypted_removed"
	CollabTransformationReasonOther             CollabTransformationReason = "other"
)

// Existing confirmed-rejection learning retains its current pre-send timing.
// Only newly learned replacement state waits for upstream acceptance.
type CollabLearningTiming string

const (
	CollabLearningConfirmedRejection  CollabLearningTiming = "confirmed_rejection_before_send"
	CollabLearningReplacementAccepted CollabLearningTiming = "replacement_after_accept"
)
