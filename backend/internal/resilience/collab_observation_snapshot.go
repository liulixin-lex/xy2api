package resilience

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/mod/semver"
)

// CloneCollabObservation bounds work before copying or encoding. It only accepts
// the typed document: arbitrary marshalers and upstream JSON never run here.
// Invalid enums and identifiers are removed rather than persisted as text.
func CloneCollabObservation(in *CollabObservation) (out *CollabObservation) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	if in == nil {
		return nil
	}
	v := *in
	v.Counts = collabCountsBeforeTrim(in)
	v.V = CollabObservationVersion
	v.Sends = slices.Clone(in.Sends[:min(len(in.Sends), CollabObservationMaxSends)])
	v.SendOverflow = collabAdd(v.SendOverflow, len(in.Sends)-len(v.Sends))
	v.SendTotal = max(v.SendTotal, collabAdd(len(in.Sends), in.SendOverflow))
	v.Deliveries = slices.Clone(in.Deliveries[:min(len(in.Deliveries), CollabObservationMaxDeliveries)])
	v.DeliveriesOverflow = collabAdd(v.DeliveriesOverflow, len(in.Deliveries)-len(v.Deliveries))
	v.DeliveriesTotal = max(v.DeliveriesTotal, collabAdd(len(in.Deliveries), in.DeliveriesOverflow))
	v.Events = slices.Clone(in.Events[:min(len(in.Events), CollabObservationMaxEvents)])
	v.Hints = slices.Clone(in.Hints[:min(len(in.Hints), CollabObservationMaxHints)])
	v.EventsTotal = max(v.EventsTotal, collabAdd(len(in.Events), in.EventsOverflow))
	v.EventsOverflow = collabAdd(v.EventsOverflow, len(in.Events)-len(v.Events))
	v.HintsTotal = max(v.HintsTotal, collabAdd(len(in.Hints), in.HintsOverflow))
	v.HintsOverflow = collabAdd(v.HintsOverflow, len(in.Hints)-len(v.Hints))
	v.Truncated = v.Truncated || len(v.Sends) != len(in.Sends) || len(v.Deliveries) != len(in.Deliveries) || len(v.Events) != len(in.Events) || len(v.Hints) != len(in.Hints)
	for i := range v.Sends {
		s := &v.Sends[i]
		s.CallCount = collabCopyCount(s.CallCount)
		s.Marker = cloneCollabMarkerCounts(s.Marker)
		if len(s.MarkerReasons) > 8 {
			v.Truncated = true
		}
		s.MarkerReasons = slices.Clone(s.MarkerReasons[:min(len(s.MarkerReasons), 8)])
	}
	if in.InputTransformations != nil {
		v.InputTransformations = &CollabInputTransformations{Counts: make(map[CollabTransformationReason]int, 4)}
		for _, reason := range []CollabTransformationReason{CollabTransformationReasonReasoningRemoved, CollabTransformationReasonCompactionRemoved, CollabTransformationReasonEncryptedRemoved, CollabTransformationReasonOther} {
			if count, ok := in.InputTransformations.Counts[reason]; ok {
				v.InputTransformations.Counts[reason] = max(count, 0)
			}
		}
		if len(in.InputTransformations.Counts) != len(v.InputTransformations.Counts) {
			v.Truncated = true
		}
	}
	collabSanitizeValue(reflect.ValueOf(&v).Elem(), &v.Truncated)
	return &v
}

func collabCopyCount(p *int) *int {
	if p == nil {
		return nil
	}
	n := max(*p, 0)
	return &n
}

// EncodeCollabObservation returns absence on failure. Counts survive truncation;
// detail dropped by the byte cap is explicitly excluded from reconciliation.
func EncodeCollabObservation(in *CollabObservation) json.RawMessage {
	return encodeExpandedCollabObservation(in)
}

// Keep expansion independent from the persistence entry point so a later sparse
// writer cannot silently change the admin API's existing representation.
func encodeExpandedCollabObservation(in *CollabObservation) (raw json.RawMessage) {
	defer func() {
		if recover() != nil {
			raw = nil
		}
	}()
	v := CloneCollabObservation(in)
	if v == nil {
		return nil
	}
	for {
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		if len(b) <= CollabObservationMaxBytes {
			return b
		}
		v.Truncated = true
		switch {
		case len(v.Deliveries) > 0:
			n := (len(v.Deliveries) + 1) / 2
			v.Deliveries = v.Deliveries[:len(v.Deliveries)-n]
			v.DeliveriesOverflow += n
		case len(v.Sends) > 0:
			n := (len(v.Sends) + 1) / 2
			v.Sends = v.Sends[:len(v.Sends)-n]
			v.SendOverflow += n
		case len(v.Events) > 0:
			n := (len(v.Events) + 1) / 2
			v.Events = v.Events[:len(v.Events)-n]
			v.EventsOverflow += n
		case len(v.Hints) > 0:
			n := (len(v.Hints) + 1) / 2
			v.Hints = v.Hints[:len(v.Hints)-n]
			v.HintsOverflow += n
		default:
			return nil
		}
	}
}

// NormalizeCollabObservationJSON isolates optional data from billing intents and
// SQL. Sparse v3 uses the same typed fields as v2: omitted values take their Go
// defaults, while nullable counts must retain an explicit zero when observed.
// Both versions expand to v2; persistence still writes v2 in this release.
// Invalid, oversized or unsupported documents become SQL NULL, not errors.
func NormalizeCollabObservationJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 || len(raw) > 4*CollabObservationMaxBytes || !collabPostgresJSON(raw) {
		return nil
	}
	var v CollabObservation
	if json.Unmarshal(raw, &v) != nil || (v.V != CollabObservationVersion && v.V != collabObservationSparseVersion) {
		return nil
	}
	return encodeExpandedCollabObservation(&v)
}

// PostgreSQL rejects NUL, invalid UTF-8 and unpaired Unicode surrogates which
// encoding/json may accept or replace. Validate unknown fields too. The depth
// bound exceeds this schema but prevents deeply nested input reaching decoding.
func collabPostgresJSON(raw []byte) bool {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return false
	}
	depth := 0
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			switch c {
			case '"':
				inString = true
			case '{', '[':
				depth++
				if depth > 32 {
					return false
				}
			case '}', ']':
				depth--
			}
			continue
		}
		if c == '"' {
			inString = false
			continue
		}
		if c != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		code, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if code == 0 || code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func collabSanitizeValue(v reflect.Value, truncated *bool) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			collabSanitizeValue(v.Field(i), truncated)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			collabSanitizeValue(v.Index(i), truncated)
		}
	case reflect.String:
		s := v.String()
		if s == "" {
			return
		}
		valid := false
		switch v.Type() {
		case reflect.TypeFor[CollabDigest]():
			valid = len(s) == 64
			if valid {
				for _, c := range s {
					if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
						valid = false
						break
					}
				}
			}
		case reflect.TypeFor[string]():
			valid = validCollabVersion(s)
		default:
			known := collabObservationEnumValues[v.Type()]
			index := slices.Index(known, s)
			valid = index >= 0
			if valid {
				v.SetString(known[index])
			}
		}
		// Detach bounded identifiers from any larger request-backed string.
		if valid && (v.Type() == reflect.TypeFor[CollabDigest]() || v.Type() == reflect.TypeFor[string]()) {
			v.SetString(strings.Clone(s))
		}
		if !valid {
			v.SetString("")
			*truncated = true
		}
	}
}

func validCollabVersion(s string) bool {
	if len(s) == 0 || len(s) > 64 || strings.HasPrefix(s, "v") {
		return false
	}
	base, _, _ := strings.Cut(s, "-")
	base, _, _ = strings.Cut(base, "+")
	return strings.Count(base, ".") == 2 && semver.IsValid("v"+s)
}

// ParseCollabClient observes only the leading originator/version token. It
// never substitutes a gateway version or persists the rest of User-Agent.
func ParseCollabClient(ua string) (CollabClient, CollabVersionState, string) {
	if ua == "" {
		return CollabClientUnknown, CollabVersionStateMissing, ""
	}
	end := strings.IndexAny(ua, " \t\r\n")
	if end < 0 {
		end = len(ua)
	}
	if end > 128 {
		return CollabClientUnknown, CollabVersionStateInvalid, ""
	}
	name, version, ok := strings.Cut(ua[:end], "/")
	client := CollabClientOther
	switch name {
	case "codex_cli_rs", "codex_cli":
		client = CollabClientCodexCli
	case "codex_app":
		client = CollabClientCodexApp
	case "codex_vscode":
		client = CollabClientCodexVscode
	}
	if !ok || !validCollabVersion(version) {
		return client, CollabVersionStateInvalid, ""
	}
	return client, CollabVersionStateParsed, version
}

func CollabClientVersionAtLeast(version, minimum string) bool {
	return validCollabVersion(version) && validCollabVersion(minimum) && semver.Compare("v"+version, "v"+minimum) >= 0
}

var collabObservationEnumValues = map[reflect.Type][]string{
	reflect.TypeFor[CollabRole]():                 {string(CollabRoleRoot), string(CollabRoleChild), string(CollabRoleUnknown)},
	reflect.TypeFor[CollabRequestKind]():          {string(CollabRequestKindNormal), string(CollabRequestKindCompaction), string(CollabRequestKindReview), string(CollabRequestKindStructuredOutput)},
	reflect.TypeFor[CollabRoute]():                {string(CollabRouteResponsesHttp), string(CollabRoutePassthrough), string(CollabRouteHttpToWs), string(CollabRouteWs), string(CollabRouteCompact), string(CollabRouteChatBridge), string(CollabRouteMessagesBridge), string(CollabRouteOther)},
	reflect.TypeFor[CollabClient]():               {string(CollabClientCodexCli), string(CollabClientCodexApp), string(CollabClientCodexVscode), string(CollabClientOther), string(CollabClientUnknown)},
	reflect.TypeFor[CollabVersionState]():         {string(CollabVersionStateParsed), string(CollabVersionStateMissing), string(CollabVersionStateInvalid)},
	reflect.TypeFor[CollabFinalMode]():            {string(CollabFinalModePlaintext), string(CollabFinalModeCiphertext), string(CollabFinalModePlaceholder), string(CollabFinalModeLocal), string(CollabFinalModeMixed), string(CollabFinalModeNone)},
	reflect.TypeFor[CollabPlanOutcome]():          {string(CollabPlanOutcomeApplied), string(CollabPlanOutcomeAbsent), string(CollabPlanOutcomeConflict), string(CollabPlanOutcomeUnsupported), string(CollabPlanOutcomeInvalid)},
	reflect.TypeFor[CollabPlanReason]():           {string(CollabPlanReasonNone), string(CollabPlanReasonNoNamespace), string(CollabPlanReasonInvalidJson), string(CollabPlanReasonDuplicateKey), string(CollabPlanReasonStateful), string(CollabPlanReasonUnsupportedShape), string(CollabPlanReasonAliasCollision), string(CollabPlanReasonNamespaceUpdate), string(CollabPlanReasonSchemaInvalid), string(CollabPlanReasonToolDuplicate), string(CollabPlanReasonHistoryIdentity), string(CollabPlanReasonControlText), string(CollabPlanReasonClientTooOld), string(CollabPlanReasonEditFailed)},
	reflect.TypeFor[CollabEligibility]():          {string(CollabEligibilityEligible), string(CollabEligibilityMasterOff), string(CollabEligibilityFeatureOff), string(CollabEligibilityAccountOff), string(CollabEligibilityModelNotListed), string(CollabEligibilityClientUnrecognized), string(CollabEligibilityDownstreamWs), string(CollabEligibilityUpstreamWs), string(CollabEligibilityPassthrough), string(CollabEligibilityCompact), string(CollabEligibilityBridge), string(CollabEligibilityNotOpenai), string(CollabEligibilityFlatten)},
	reflect.TypeFor[CollabRestoreError]():         {string(CollabRestoreErrorNone), string(CollabRestoreErrorInvalidJson), string(CollabRestoreErrorAmbiguousFields), string(CollabRestoreErrorInvalidItem), string(CollabRestoreErrorInvalidOutput), string(CollabRestoreErrorIdentityInvalid), string(CollabRestoreErrorNamespaceAmbiguous), string(CollabRestoreErrorIdentifierConflict), string(CollabRestoreErrorIdentityChanged), string(CollabRestoreErrorEchoAmbiguous), string(CollabRestoreErrorEditFailed)},
	reflect.TypeFor[CollabMarkerReason]():         {string(CollabMarkerReasonNonemptyEvidence), string(CollabMarkerReasonAbnormalType), string(CollabMarkerReasonAnonymous), string(CollabMarkerReasonExtraEncrypted), string(CollabMarkerReasonUnsafeArguments), string(CollabMarkerReasonOpaqueMessage), string(CollabMarkerReasonChangedArguments), string(CollabMarkerReasonStrictVeto)},
	reflect.TypeFor[CollabAction]():               {string(CollabActionOriginal), string(CollabActionSwitchOriginal), string(CollabActionProbeReasoning), string(CollabActionNamedRepair), string(CollabActionDropUnprotected), string(CollabActionReplaceAll), string(CollabActionPendingRetry), string(CollabActionFloorRepair), string(CollabActionGenericFailover), string(CollabActionFieldRepair), string(CollabActionFcoRepair), string(CollabActionOther)},
	reflect.TypeFor[CollabSendResult]():           {string(CollabSendResultAccepted), string(CollabSendResultCipherRejected), string(CollabSendResultUncertainRejected), string(CollabSendResultUnrelatedRejected), string(CollabSendResultTransientError), string(CollabSendResultCanceled), string(CollabSendResultNotSent)},
	reflect.TypeFor[CollabReason]():               {string(CollabReasonNone), string(CollabReasonAgentDecrypt), string(CollabReasonToolDecrypt), string(CollabReasonReasoningDecrypt), string(CollabReasonCompactionDecrypt), string(CollabReasonCallArgsDecrypt), string(CollabReasonRelayOwnership), string(CollabReasonRelayUnknownSuffix), string(CollabReasonAmbiguousWrapper), string(CollabReasonUnrelated), string(CollabReasonUnlocatable), string(CollabReasonReviewRequest), string(CollabReasonContinuationPointer), string(CollabReasonPostOutput), string(CollabReasonCallArgsUnreplaceable), string(CollabReasonTreeLocalCap), string(CollabReasonBudgetTime), string(CollabReasonBudgetSends), string(CollabReasonNoAccount), string(CollabReasonRelayCapacity), string(CollabReasonQueueTimeout), string(CollabReasonModerationBlock), string(CollabReasonUnsupportedRoute), string(CollabReasonCanceled), string(CollabReasonWriteFailed), string(CollabReasonOther)},
	reflect.TypeFor[CollabPosition]():             {string(CollabPositionMessage), string(CollabPositionCallArgs)},
	reflect.TypeFor[CollabForm]():                 {string(CollabFormPlaintext), string(CollabFormCiphertext)},
	reflect.TypeFor[CollabMessageKind]():          {string(CollabMessageKindTask), string(CollabMessageKindMessage), string(CollabMessageKindToolOutput), string(CollabMessageKindUnknown)},
	reflect.TypeFor[CollabItemClass]():            {string(CollabItemClassTerminated), string(CollabItemClassProtected), string(CollabItemClassUnreadMessage), string(CollabItemClassReplaceable)},
	reflect.TypeFor[CollabDeliveryOutcome]():      {string(CollabDeliveryOutcomeAcceptedOriginal), string(CollabDeliveryOutcomePlaceholder), string(CollabDeliveryOutcomePlaceholderReplay), string(CollabDeliveryOutcomeNoticePending), string(CollabDeliveryOutcomeNoticeWrittenUser), string(CollabDeliveryOutcomeFailed), string(CollabDeliveryOutcomeUndeliverable)},
	reflect.TypeFor[CollabEventSource]():          {string(CollabEventSourceIssued), string(CollabEventSourceSeenParentNotice), string(CollabEventSourceSeenOther)},
	reflect.TypeFor[CollabHintKind]():             {string(CollabHintKindConsumer), string(CollabHintKindIssuer), string(CollabHintKindPlaintextRoute)},
	reflect.TypeFor[CollabCooldown]():             {string(CollabCooldownNone), string(CollabCooldownLe10s), string(CollabCooldownGt10s), string(CollabCooldownHard)},
	reflect.TypeFor[CollabHintAttempt]():          {string(CollabHintAttemptNotAttempted), string(CollabHintAttemptSelected), string(CollabHintAttemptFellThrough)},
	reflect.TypeFor[CollabExecution]():            {string(CollabExecutionNotStarted), string(CollabExecutionPartial), string(CollabExecutionUnknown)},
	reflect.TypeFor[CollabEvidenceGrade]():        {string(CollabEvidenceGradeVerified), string(CollabEvidenceGradeClaimed), string(CollabEvidenceGradeUnclaimed)},
	reflect.TypeFor[CollabScope]():                {string(CollabScopeFragment), string(CollabScopeWholeItem)},
	reflect.TypeFor[CollabStage]():                {string(CollabStageOriginal), string(CollabStageSwitch), string(CollabStageProbe), string(CollabStageNamed), string(CollabStageDrop), string(CollabStageReplaceAll), string(CollabStageFloor), string(CollabStageReplay), string(CollabStageLocal)},
	reflect.TypeFor[CollabTransformationReason](): {string(CollabTransformationReasonReasoningRemoved), string(CollabTransformationReasonCompactionRemoved), string(CollabTransformationReasonEncryptedRemoved), string(CollabTransformationReasonOther)},
}
