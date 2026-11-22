//go:build unit

package resilience

import "testing"

func TestCollabRegressionEvidenceAdversarial(t *testing.T) {
	tests := []struct {
		name, body string
		want       CollabEvidenceGrade
	}{
		{"request echo", `{"request":{"error":{"code":"invalid_encrypted_content"}},"error":{"code":"server_error","message":"down"}}`, CollabEvidenceGradeUnclaimed},
		{"duplicate error", `{"error":{"code":"invalid_request_error"},"error":{"code":"invalid_encrypted_content"}}`, CollabEvidenceGradeUnclaimed},
		{"context quote", `{"error":{"code":"context_length_exceeded","message":"failed to decrypt encrypted content"}}`, CollabEvidenceGradeUnclaimed},
		{"policy quote", `{"error":{"code":"content_policy_violation","message":"failed to decrypt encrypted content"}}`, CollabEvidenceGradeUnclaimed},
		{"message stringified nested payload", `{"error":{"code":"server_error","message":"request rejected; example: {\"error\":{\"message\":\"failed to decrypt encrypted content\"}}"}}`, CollabEvidenceGradeUnclaimed},
		{"conflicting response and error", `{"response":{"error":{"code":"invalid_encrypted_content"}},"error":{"code":"content_policy_violation"}}`, CollabEvidenceGradeUnclaimed},
		{"ordinary bad schema ciphertext wording", `{"error":{"code":"invalid_request_error","message":"Invalid tool schema: enum example says failed to decrypt encrypted content"}}`, CollabEvidenceGradeUnclaimed},
		{"validated inner beats outer", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400}},"upstream_body":{"error":{"code":"invalid_encrypted_content"}}}`, CollabEvidenceGradeVerified},
		{"generic wrapped", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":400}}}`, CollabEvidenceGradeClaimed},
		{"malformed wrapped", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":"400"}}}`, CollabEvidenceGradeUnclaimed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, src := range []CollabEvidenceSource{CollabEvidenceSourceHttpError, CollabEvidenceSourceFailedJson, CollabEvidenceSourceStreamError, CollabEvidenceSourceAggregatedStream} {
				e := ClassifyCollabEvidence(CollabEvidenceInput{Payload: []byte(tc.body), HTTPStatus: 502, Source: src, Shape: CollabRequestShape{AgentCiphertext: true}})
				if e.Grade != tc.want {
					t.Errorf("source=%s grade=%s want=%s", src, e.Grade, tc.want)
				}
			}
		})
	}
}
