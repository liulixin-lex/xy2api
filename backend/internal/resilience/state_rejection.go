package resilience

import "strings"

// StateFamily identifies request-scoped continuation failures, independently
// of the narrower repair eligibility check.
type StateFamily string

const (
	StateNone                StateFamily = ""
	StateRelay               StateFamily = "relay_state"
	StateConversationInvalid StateFamily = "conversation_invalid"
	StateFunctionOutput      StateFamily = "function_output"
	StateAgentMessage        StateFamily = "agent_message"
	StateReasoning           StateFamily = "reasoning"
	StateOtherEncrypted      StateFamily = "other_encrypted"
	ReasonConversationState              = "conversation_state"
)

type StateRejection struct {
	Family    StateFamily
	Encrypted bool
}

func (r StateRejection) RequestScoped() bool { return r.Family != StateNone }

// ClassifyStateRejection preserves the v3 guard: dedicated codes and the
// existing encrypted rejection predicate win; only an absent code permits
// the two narrow conversation-state message fallbacks. Type is not evidence.
func ClassifyStateRejection(code, message string) StateRejection {
	r := StateRejection{Encrypted: IsEncryptedContentRejection(code, message)}
	code = strings.ToLower(strings.TrimSpace(code))
	message = strings.ToLower(message)
	relay := code == "client_tool_continuation_unavailable" || (code == "" &&
		(strings.Contains(message, "opaque conversation state") || strings.Contains(message, "unrecognized conversation state")))
	conversation := strings.Contains(message, "encrypted content is invalid for this conversation")
	requestScoped := relay || r.Encrypted || (code == "" && conversation)
	if !requestScoped {
		return r
	}
	switch {
	case relay:
		r.Family = StateRelay
	case conversation:
		r.Family = StateConversationInvalid
	case strings.Contains(message, "encrypted function output"):
		r.Family = StateFunctionOutput
	case strings.Contains(message, "for item amsg_"):
		r.Family = StateAgentMessage
	case strings.Contains(message, "for item rs_"), strings.Contains(message, "for item cmp_"):
		r.Family = StateReasoning
	default:
		r.Family = StateOtherEncrypted
	}
	return r
}
