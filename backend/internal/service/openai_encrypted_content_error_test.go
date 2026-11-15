//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptedContentAmbiguousRejectionContract(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"cipher-bad"},{"type":"reasoning","id":"rs_good","encrypted_content":"cipher-good"}]}`)
	account := &Account{Credentials: map[string]any{"api_key": "fixture-credential-secret"}}
	for _, tc := range []struct {
		name, payload, param string
		unique               bool
	}{
		{"missing", `{"error":{"code":"invalid_encrypted_content","message":"Could not verify encrypted content"}}`, "", false},
		{"conflicting", `{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"Could not verify for item rs_good"}}`, "input[0].encrypted_content", false},
		{"located", `{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content","message":"Could not verify for item rs_bad"}}`, "input[0].encrypted_content", true},
		{"item_only", `{"error":{"code":"invalid_encrypted_content","message":"Could not verify for item rs_bad"}}`, "", true},
		{"redaction", `{"error":{"code":"invalid_encrypted_content","message":"Could not verify cipher-bad with fixture-credential-secret"},"debug":"arbitrary response body"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newOpenAIEncryptedContentRejection(context.Background(), account, body, []byte(tc.payload))
			require.Equal(t, tc.param, err.Param)
			require.Contains(t, err.Message, "Upstream detail:")
			if tc.unique {
				require.Contains(t, err.Message, "Remove only the identified encrypted item")
			} else {
				require.Contains(t, err.Message, "Rebuild the request from complete readable history")
				require.NotContains(t, err.Message, "Remove only")
			}
			require.NotContains(t, err.Message, "cipher-bad")
			require.NotContains(t, err.Message, "fixture-credential-secret")
			require.NotContains(t, err.Message, "arbitrary response body")
			if tc.name == "conflicting" {
				require.Contains(t, err.Message, "rs_good")
				require.Contains(t, err.Message, "input[0].encrypted_content")
			}
		})
	}
}
