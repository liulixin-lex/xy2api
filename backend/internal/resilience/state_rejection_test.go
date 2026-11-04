package resilience

import "testing"

func TestClassifyStateRejection(t *testing.T) {
	for _, tc := range []struct {
		code, message string
		family        StateFamily
	}{
		{"CLIENT_TOOL_CONTINUATION_UNAVAILABLE", "neutral", StateRelay},
		{"", "Cannot prove ownership of opaque conversation state", StateRelay},
		{"", "Unrecognized conversation state", StateRelay},
		{"", "Encrypted content is invalid for this conversation", StateConversationInvalid},
		{"invalid_encrypted_content", "Encrypted content is invalid for this conversation", StateConversationInvalid},
		{"server_error", "Encrypted function output content could not be decrypted", StateFunctionOutput},
		{"invalid_encrypted_content", "The encrypted content could not be verified for item amsg_1", StateAgentMessage},
		{"", "Encrypted content hydration failed for item rs_1", StateReasoning},
		{"encrypted_context_invalid", "for item cmp_1", StateReasoning},
		{"encrypted_context_invalid", "details redacted", StateOtherEncrypted},
		{"", "Could not validate encrypted continuation context", StateOtherEncrypted},
		{"server_error", "opaque conversation state", StateNone},
		{"rate_limit_exceeded", "unrecognized conversation state", StateNone},
		{"server_error", "Encrypted content is invalid for this conversation", StateNone},
		{"", "for item amsg_1", StateNone},
		{"", "invalid for this conversation", StateNone},
		{"", "encrypted payload rejected", StateNone},
		{"", "details redacted", StateNone},
	} {
		t.Run(tc.code+"/"+tc.message, func(t *testing.T) {
			r := ClassifyStateRejection(tc.code, tc.message)
			if r.Family != tc.family || r.RequestScoped() != (tc.family != StateNone) {
				t.Fatalf("got %+v, want %q", r, tc.family)
			}
			if r.Encrypted != IsEncryptedContentRejection(tc.code, tc.message) {
				t.Fatal("encrypted repair predicate changed")
			}
			if r.RequestScoped() {
				for _, source := range []Source{SourceHTTP, SourceStreamEvent} {
					classified := Classify(Outcome{Source: source, StatusCode: 502, Code: tc.code, Message: tc.message}, nil)
					if classified.StateFamily != tc.family || classified.Category != CategoryClientError || classified.Category.CountsForSession() || classified.Category.SoftCooldown() {
						t.Fatalf("got %+v", classified)
					}
				}
			}
		})
	}
}

func TestStateRejectionLeavesRecoverableCategories(t *testing.T) {
	for _, tc := range []struct {
		code     string
		status   int
		category Category
	}{
		{"insufficient_quota", 429, CategoryBalance},
		{"rate_limit_exceeded", 429, CategoryRateLimit},
		{"model_not_found", 404, CategoryModelUnsupported},
		{"server_is_overloaded", 503, CategoryCapacityShed},
	} {
		r := Classify(Outcome{StatusCode: tc.status, Code: tc.code, Message: "try again"}, nil)
		if r.Category != tc.category || r.StateFamily != StateNone || !r.Category.SwitchAccount() {
			t.Fatalf("%s: %+v", tc.code, r)
		}
	}
}
