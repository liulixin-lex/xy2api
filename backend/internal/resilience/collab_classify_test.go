//go:build unit

package resilience

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollabEvidenceClassification(t *testing.T) {
	for _, source := range []CollabEvidenceSource{CollabEvidenceSourceHttpError, CollabEvidenceSourceStreamError, CollabEvidenceSourceAggregatedStream, CollabEvidenceSourceFailedJson} {
		t.Run(string(source), func(t *testing.T) {
			for _, tc := range []struct {
				name, code, message string
				grade               CollabEvidenceGrade
				family              CollabFamily
			}{
				{"unnamed", "", "The encrypted content could not be decrypted.", CollabEvidenceGradeVerified, CollabFamilyOtherEncrypted},
				{"hydration", "", "Encrypted content hydration failed", CollabEvidenceGradeVerified, CollabFamilyOtherEncrypted},
				{"context", "ENCRYPTED_CONTEXT_INVALID", "unreadable", CollabEvidenceGradeVerified, CollabFamilyOtherEncrypted},
				{"agent", "invalid_encrypted_content", "Encrypted content could not be verified for item amsg_task", CollabEvidenceGradeVerified, CollabFamilyAgentMessage},
				{"reasoning", "invalid_encrypted_content", "Encrypted content could not be verified for item rs_alpha", CollabEvidenceGradeVerified, CollabFamilyReasoning},
				{"compaction", "invalid_encrypted_content", "Encrypted content could not be verified for item cmp_alpha", CollabEvidenceGradeVerified, CollabFamilyCompaction},
				{"call", "invalid_encrypted_content", "Encrypted content could not be verified for item fc_alpha", CollabEvidenceGradeVerified, CollabFamilyCallArgs},
				{"tool", "", "Encrypted function output content could not be decrypted or decoded", CollabEvidenceGradeVerified, CollabFamilyToolOutput},
				{"ordinary", "invalid_request_error", "invalid request", CollabEvidenceGradeUnclaimed, CollabFamilyNone},
				{"opaque", "", "Cannot prove ownership of opaque conversation state", CollabEvidenceGradeUnclaimed, CollabFamilyNone},
				{"ownership prose", "", "encrypted_agent_recovery_owner_conflict", CollabEvidenceGradeUnclaimed, CollabFamilyNone},
				{"policy", "content_policy_violation", "Quoted: encrypted content could not be decrypted", CollabEvidenceGradeUnclaimed, CollabFamilyNone},
				{"cyber policy", "cyber_policy", "encrypted content could not be decrypted", CollabEvidenceGradeUnclaimed, CollabFamilyNone},
				{"context wording", "invalid_request_error", "maximum context length exceeded", CollabEvidenceGradeUnclaimed, CollabFamilyNone},
			} {
				t.Run(tc.name, func(t *testing.T) {
					payload, err := json.Marshal(map[string]any{"error": map[string]string{"code": tc.code, "message": tc.message}})
					require.NoError(t, err)
					got := ClassifyCollabEvidence(CollabEvidenceInput{Payload: payload, HTTPStatus: 502, Source: source, Shape: CollabRequestShape{AgentCiphertext: true}})
					require.Equal(t, tc.grade, got.Grade)
					require.Equal(t, tc.family, got.Family)
					require.Equal(t, source, got.Source)
				})
			}
		})
	}
}

func TestCollabEvidenceOwnershipCodes(t *testing.T) {
	for code, want := range map[string]CollabOwnershipClass{
		"encrypted_agent_recovery_owner_conflict":       CollabOwnershipClassSourceConflict,
		"encrypted_agent_recovery_wire_changed":         CollabOwnershipClassRecognized,
		"encrypted_agent_recovery_owner_unavailable":    CollabOwnershipClassRecognized,
		"encrypted_agent_recovery_owner_conflict_extra": CollabOwnershipClassUnknownSuffix,
		"encrypted_agent_recovery_future":               CollabOwnershipClassUnknownSuffix,
	} {
		for _, spelling := range []string{code, strings.ToUpper(code)} {
			got := ClassifyCollabEvidence(CollabEvidenceInput{Payload: []byte(`{"error":{"code":"` + spelling + `"}}`)})
			require.Equal(t, CollabEvidenceGradeVerified, got.Grade)
			require.Equal(t, want, got.Ownership)
		}
	}
}

func TestCollabEvidenceValidatedInnerWins(t *testing.T) {
	const wrapper = `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400}},"upstream_body":{"error":{"code":"encrypted_context_invalid"}}}`
	got := ClassifyCollabEvidence(CollabEvidenceInput{Payload: []byte(wrapper), HTTPStatus: 502})
	require.Equal(t, CollabEvidenceGradeVerified, got.Grade)
	require.True(t, got.Inner.Validated)
	require.Equal(t, "encrypted_context_invalid", got.Inner.Code)
	for _, body := range []string{
		strings.Replace(wrapper, `"status_code":400`, `"status_code":401`, 1),
		strings.Replace(wrapper, `"status_code":400`, `"status_code":"400"`, 1),
		strings.Replace(wrapper, `"status_code":400`, `"status_code":4e2`, 1),
		strings.Replace(wrapper, `"status_code":400`, `"status_code":400,"status_code":500`, 1),
		wrapper[:len(wrapper)-1],
		`{"output":[{"error":{"code":"invalid_encrypted_content"}}]}`,
		`{"type":"proxy.upstream_error","status":400,"upstream_body":{"error":{"code":"invalid_encrypted_content"}}}`,
	} {
		evidence := ClassifyCollabEvidence(CollabEvidenceInput{Payload: []byte(body), HTTPStatus: 400, WrappedStatus: 400, Shape: CollabRequestShape{AgentCiphertext: true}})
		require.Equal(t, CollabEvidenceGradeUnclaimed, evidence.Grade, body)
	}
	proxy := []byte(`{"type":"proxy.upstream_error","status":400,"upstream_body":"{\"error\":{\"code\":\"invalid_encrypted_content\"}}"}`)
	require.Equal(t, CollabEvidenceGradeVerified, ClassifyCollabEvidence(CollabEvidenceInput{Payload: proxy, Event: "error"}).Grade)
}

func TestCollabEvidenceClaimRequiresValidatedWrapperAndNestedCiphertext(t *testing.T) {
	const wrapper = `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400,"message":"rejected"}}}`
	for _, shape := range []CollabRequestShape{{AgentCiphertext: true}, {ToolOutputCiphertext: true}, {ReasoningCiphertext: true}, {}} {
		got := ClassifyCollabEvidence(CollabEvidenceInput{Payload: []byte(wrapper), Shape: shape})
		want := CollabEvidenceGradeUnclaimed
		if shape.AgentCiphertext || shape.ToolOutputCiphertext {
			want = CollabEvidenceGradeClaimed
		}
		require.Equal(t, want, got.Grade)
	}
}

func TestCollabEvidenceConflictingNamesStayNamed(t *testing.T) {
	got := ClassifyCollabEvidence(CollabEvidenceInput{Payload: []byte(`{"error":{"code":"invalid_encrypted_content","item_id":"rs_alpha","message":"for item amsg_task"}}`)})
	require.NotNil(t, got.Named)
	require.True(t, got.Named.Ambiguous)
}

func TestCollabEvidenceSavedLocationIsDetached(t *testing.T) {
	index := 2
	snapshot := NewCollabSavedTerminal(CollabTerminalSnapshot{Rejection: CollabEvidence{Named: &CollabNamedItem{InputIndex: &index}}})
	index = 4
	require.Equal(t, 2, *snapshot.Snapshot.Rejection.Named.InputIndex)
}
