//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSanitizeUpstreamErrorMessageRedactsProviderIdentifiersOnlyAtProjection(t *testing.T) {
	message := "org-AbC123456 proj_PqR123456 workspace_id=ws_1234567 project id: project-123456 account id: acct_123456 response_id=resp_123456"
	got := SanitizeUpstreamErrorMessage(message)
	for _, secret := range []string{"org-AbC123456", "proj_PqR123456", "ws_1234567", "project-123456", "acct_123456"} {
		require.NotContains(t, got, secret)
	}
	require.Contains(t, got, "org-[redacted]")
	require.Contains(t, got, "proj_[redacted]")
	require.Contains(t, got, "workspace_id=[redacted]")
	require.Contains(t, got, "project id: [redacted]")
	require.Contains(t, got, "response_id=resp_123456", "response IDs are not provider account identifiers")
	require.Contains(t, sanitizeUpstreamErrorMessage(message), "org-AbC123456", "classification sanitizer keeps its existing scope")

	query := SanitizeUpstreamErrorMessage("failed https://relay.example/?key=secret123")
	require.True(t, strings.Contains(query, "key=***"))
}

func TestSanitizeUpstreamErrorBodyTouchesOnlyErrorMessages(t *testing.T) {
	body := []byte(`{"error":{"message":"workspace_id=ws_1234567 org-AbC123456","code":"bad_request","param":"response_id=resp_123456"},"response_id":"resp_987654"}`)
	got := sanitizeUpstreamErrorBodyForClient(body)
	require.NotContains(t, gjson.GetBytes(got, "error.message").String(), "ws_1234567")
	require.NotContains(t, gjson.GetBytes(got, "error.message").String(), "org-AbC123456")
	require.Equal(t, "bad_request", gjson.GetBytes(got, "error.code").String())
	require.Equal(t, "response_id=resp_123456", gjson.GetBytes(got, "error.param").String())
	require.Equal(t, "resp_987654", gjson.GetBytes(got, "response_id").String())
}

func TestGeminiSkippedFailoverClonesUpstreamHeaders(t *testing.T) {
	svc := &GeminiMessagesCompatService{}
	account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth}
	headers := http.Header{"Retry-After": []string{"17"}, "X-Request-Id": []string{"upstream-id"}}
	failoverErr := svc.skippedErrorPolicyFailoverError(nil, account, http.StatusTooManyRequests, headers, []byte(`{"error":{"message":"slow down"}}`), "upstream-id")
	require.NotNil(t, failoverErr)
	headers.Set("Retry-After", "2")
	require.Equal(t, "17", failoverErr.ResponseHeaders.Get("Retry-After"))
	require.Equal(t, "upstream-id", failoverErr.ResponseHeaders.Get("X-Request-Id"))
}
