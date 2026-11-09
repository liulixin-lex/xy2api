//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAITerminalOpsUsesHTTPStorageSanitizer(t *testing.T) {
	raw := []byte(`{"error":{"message":"could not validate encrypted continuation context","code":"invalid_encrypted_content"},"headers":{"authorization":"Bearer unknown-oauth","cookie":"unknown-cookie","x-api-key":"unknown-api-key"},"access_token":"unknown-access-token","refresh_token":"unknown-refresh-token","api_key":"unknown-key","session_token":"unknown-session","client_secret":"unknown-secret","max_tokens":4096}`)
	account := terminalRejectionTestAccount(1, "relay-a.example")
	httpOps := &OpsInsertErrorLogInput{UpstreamErrors: []*OpsUpstreamErrorEvent{{Kind: "http_error", UpstreamStatusCode: 400, Detail: string(raw)}}}
	require.NoError(t, SanitizeOpsUpstreamErrorsForQueue(httpOps))
	httpEvents, err := ParseOpsUpstreamErrors(*httpOps.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Len(t, httpEvents, 1)
	expected := httpEvents[0].Detail
	svc, _, _ := terminalRejectionTestService()
	svc.cfg.Gateway.LogUpstreamErrorBody = true
	svc.cfg.Gateway.LogUpstreamErrorBodyMaxBytes = OpsErrorLogQueueBodyMaxBytes
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	svc.recordOpenAIStreamUpstreamError(c, account, false, "", "terminal_rejection", raw, "could not validate encrypted continuation context", 400)
	events := currentOpsUpstreamEvents(c)
	require.Len(t, events, 1)
	got := []byte(events[0].Detail)
	t.Logf("same_http_storage_sanitizer=%t", expected == string(got))
	require.Equal(t, expected, string(got), "terminal SSE ops must retain the HTTP ops JSON sanitization semantics")
	require.Contains(t, string(got), `"max_tokens":4096`)
	for _, secret := range []string{"unknown-oauth", "unknown-cookie", "unknown-api-key", "unknown-access-token", "unknown-refresh-token", "unknown-key", "unknown-session", "unknown-secret"} {
		require.NotContains(t, string(got), secret)
	}
}

func TestOpenAITerminalOpsRedactsKnownCredentialValuesForEveryAuthMode(t *testing.T) {
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(typ, func(t *testing.T) {
			account := terminalRejectionTestAccount(1, "relay-a.example")
			account.Type = typ
			var secrets []string
			for _, key := range []string{"access_token", "refresh_token", "id_token", "api_key", "cookie", "authorization", "session_key"} {
				value := "synthetic-opaque-" + key + "-value"
				account.Credentials[key] = value
				secrets = append(secrets, value)
			}
			escapedCookie := `synthetic-cookie-"quoted\value"`
			account.Credentials["session_cookie"] = escapedCookie
			secrets = append(secrets, escapedCookie)
			raw, err := json.Marshal(map[string]any{"error": map[string]string{"message": strings.Join(secrets, " opaque ")}})
			require.NoError(t, err)
			original := append([]byte(nil), raw...)
			got := redactOpenAITerminalOpsSensitiveBody(t.Context(), nil, account, raw)
			var decoded map[string]map[string]string
			require.NoError(t, json.Unmarshal(got, &decoded))
			for _, secret := range secrets {
				require.NotContains(t, decoded["error"]["message"], secret)
			}
			require.Equal(t, original, raw, "private input evidence must retain its original bytes")
		})
	}
}
