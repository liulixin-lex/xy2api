package resilience

import "bytes"

// CollabIngressObservation is a bounded, read-only inventory. It contains no
// request text. Count it once before route dispatch, including unsupported routes.
type CollabIngressObservation struct {
	Observed                 bool
	Untracked                bool
	Declared                 bool
	Carrier                  string
	EncryptedAgent           bool
	EncryptedTool            bool
	SuspectedCustomNamespace bool
	UnrecognizedCatalog      bool
}

// CollabIngressValidation is the ingress handler's result for these exact,
// unchanged bytes. Unknown/failed validation cannot produce observation evidence.
type CollabIngressValidation uint8

const (
	CollabIngressValidationUnknown CollabIngressValidation = iota
	CollabIngressValidationFailed
	CollabIngressValidationPassed
)

// InspectCollabIngress consumes the existing validation result, never validates
// again. Callers must pass Unknown after changing or replacing the body.
func InspectCollabIngress(body []byte, validation CollabIngressValidation) CollabIngressObservation {
	// Fixed protocol comparisons plus a bounded number of passes per byte.
	budget := collabIngressBudget{remaining: 512*int64(len(body)) + 512}
	return inspectCollabIngress(body, validation, &budget)
}

func inspectCollabIngress(body []byte, validation CollabIngressValidation, budget *collabIngressBudget) (out CollabIngressObservation) {
	defer func() {
		if failure := recover(); failure != nil {
			out = CollabIngressObservation{}
			if _, exhausted := failure.(collabIngressExhausted); exhausted {
				out.Untracked = true
			}
		}
	}()
	if validation != CollabIngressValidationPassed {
		return out
	}
	budget.spend(int64(len(body)))
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return out
	}
	root := budget.fields(body)
	if collabStringEqual(root.typ, "response.create") && collabRawObject(root.response) {
		root = budget.fields(root.response)
	}
	out.Observed = true
	out.Carrier = "none"
	inspectTools := func(raw []byte) bool {
		found := false
		budget.each(raw, func(_, tool []byte) {
			fields := budget.fields(tool)
			name := fields.name
			if len(name) == 0 || bytes.Equal(name, []byte("null")) || collabStringEqual(name, "") {
				name = budget.functionName(tool)
			}
			if collabStringEqual(fields.typ, "namespace") {
				if collabStringEqual(name, "collaboration") {
					found = true
				} else {
					budget.each(fields.tools, func(_, member []byte) {
						name := budget.fields(member).name
						if collabStringEqual(name, "spawn_agent") || collabStringEqual(name, "send_message") || collabStringEqual(name, "followup_task") {
							out.SuspectedCustomNamespace = true
						}
					})
				}
			}
			if collabStringPrefix(name, "collaboration.") || collabStringEqual(fields.namespace, "collaboration") {
				found = true
			}
		})
		return found
	}
	top, additional := inspectTools(root.tools), false
	budget.each(root.input, func(_, item []byte) {
		fields := budget.fields(item)
		switch {
		case collabStringEqual(fields.typ, "additional_tools"):
			additional = inspectTools(fields.tools) || additional
		case collabStringEqual(fields.typ, "agent_message"):
			budget.each(fields.content, func(_, part []byte) {
				f := budget.fields(part)
				if collabStringEqual(f.typ, "encrypted_content") || len(f.encrypted) > 0 {
					out.EncryptedAgent = true
				}
			})
		case collabStringEqual(fields.typ, "function_call_output") || collabStringEqual(fields.typ, "tool_output"):
			if len(fields.encrypted) > 0 {
				out.EncryptedTool = true
			}
			budget.each(fields.output, func(_, part []byte) {
				f := budget.fields(part)
				if collabStringEqual(f.typ, "encrypted_content") || len(f.encrypted) > 0 {
					out.EncryptedTool = true
				}
			})
		}
		if collabStringEqual(fields.role, "developer") {
			check := func(raw []byte) {
				if budget.contains(raw, "collaboration") && budget.contains(raw, "namespace") && !budget.contains(raw, "namespace collaboration") {
					out.UnrecognizedCatalog = true
				}
			}
			if collabRawString(fields.content) {
				check(fields.content)
			} else {
				budget.each(fields.content, func(_, part []byte) { check(budget.fields(part).text) })
			}
		}
	})
	out.Declared = top || additional
	switch {
	case top && additional:
		out.Carrier = "both"
	case top:
		out.Carrier = "tools"
	case additional:
		out.Carrier = "additional_tools"
	}
	return out
}

func (o CollabIngressObservation) Persist(controllerAdopted bool) bool {
	return controllerAdopted || o.Declared || o.EncryptedAgent || o.EncryptedTool
}

// CountCollabIngress accepts only enum labels; models and raw IDs are absent.
func CountCollabIngress(o CollabIngressObservation, route CollabRoute, kind CollabRequestKind) {
	if o.Untracked {
		Count("collab_ingress_untracked", 0, 0, "")
		return
	}
	if !o.Observed {
		Count("collab_ingress_unobserved", 0, 0, "")
		return
	}
	switch o.Carrier {
	case "none", "tools", "additional_tools", "both":
		Count("collab_ingress_carrier", 0, 0, o.Carrier)
	}
	v := CloneCollabObservation(&CollabObservation{Route: route, RequestKind: kind})
	if v == nil {
		return
	}
	Count("collab_ingress_route", 0, 0, string(v.Route))
	Count("collab_ingress_kind", 0, 0, string(v.RequestKind))
	if o.SuspectedCustomNamespace {
		Count("collab_ingress_custom_namespace", 0, 0, "")
	}
	if o.UnrecognizedCatalog {
		Count("collab_ingress_unrecognized_catalog", 0, 0, "")
	}
}

func ShouldLogCollabObservation(success bool, calls int, ingress CollabIngressObservation, sample uint64) bool {
	return !success || calls > 0 || ingress.EncryptedAgent || ingress.EncryptedTool || sample&15 == 0
}

// ClassifyCollabTransformationReason never casts arbitrary upstream text into
// a persisted reason. Unknown reports become the fixed "other" bucket.
func ClassifyCollabTransformationReason(reason string) CollabTransformationReason {
	switch reason {
	case "reasoning_removed":
		return CollabTransformationReasonReasoningRemoved
	case "compaction_removed":
		return CollabTransformationReasonCompactionRemoved
	case "encrypted_removed":
		return CollabTransformationReasonEncryptedRemoved
	default:
		return CollabTransformationReasonOther
	}
}

// These signals are observations only; callers supply verified facts and never
// make repair or moderation decisions from their counts.
type CollabObservationSignal string

const (
	CollabSignalForwardCall              CollabObservationSignal = "forward_call"
	CollabSignalOpaquePlaintextVeto      CollabObservationSignal = "opaque_plaintext_veto"
	CollabSignalOpaqueCiphertextMiss     CollabObservationSignal = "opaque_ciphertext_miss"
	CollabSignalReasoningOnlyCompletion  CollabObservationSignal = "reasoning_only_completion"
	CollabSignalPlaintextModerationBlock CollabObservationSignal = "plaintext_moderation_block"
)

func CountCollabObservationSignal(signal CollabObservationSignal) {
	switch signal {
	case CollabSignalForwardCall, CollabSignalOpaquePlaintextVeto, CollabSignalOpaqueCiphertextMiss, CollabSignalReasoningOnlyCompletion, CollabSignalPlaintextModerationBlock:
		Count("collab_observation_signal", 0, 0, string(signal))
	}
}
