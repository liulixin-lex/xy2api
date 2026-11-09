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

// Short credentials are scrubbed at decoded boundaries. Ambiguous embedded
// values hide their containing text rather than weakening the baseline mask.
func TestOpenAITerminalOpsShortCredentialBoundaries(t *testing.T) {
	for _, mode := range []string{AccountTypeAPIKey, AccountTypeOAuth, OpenAIAuthModeAgentIdentity} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name, key, secret, message, want string
			}{
				{"one_character", "api_key", "a", "a; a; validation data", ""},
				{"seven_characters", "api_key", "abcdefg", "xabcdefgy abcdefg", "[redacted]"},
				{"eight_characters", "api_key", "abcdefgh", "xabcdefghy abcdefgh", "x[redacted]y [redacted]"},
				{"unicode_runes", "api_key", "短凭据", "短凭据值 短凭据", "[redacted]"},
				{"escaped_short", "cookie", `a"b\c`, `xa"b\cy a"b\c`, `[redacted]`},
				{"escaped_threshold", "cookie", "a<b", "xa<by a<b", "[redacted]"},
				{"sensitive_field", "api_key", "a", "Bearer a validation", ""},
				{"identity_runtime", "agent_runtime_id", "a", "a validation", ""},
				{"identity_task", "task_id", "a", "a validation", ""},
				{"numeric_identity", "task_id", "123", "x123y 123", "[redacted]"},
			} {
				if (tc.key == "agent_runtime_id" || tc.key == "task_id") && mode != OpenAIAuthModeAgentIdentity {
					continue
				}
				t.Run(tc.name, func(t *testing.T) {
					account := terminalRejectionTestAccount(1, "relay-a.example")
					account.Type = mode
					account.Credentials = map[string]any{tc.key: tc.secret}
					if tc.name == "numeric_identity" {
						account.Credentials[tc.key] = 123
					}
					if mode == OpenAIAuthModeAgentIdentity {
						account.Type = AccountTypeOAuth
						account.Credentials[openAIAuthModeCredentialKey] = mode
					}
					raw, err := json.Marshal(map[string]string{"message": tc.message, "api_key": tc.secret})
					require.NoError(t, err)
					original := string(raw)
					got := redactOpenAITerminalOpsSensitiveBody(t.Context(), nil, account, raw)
					require.Equal(t, original, string(raw), "classification and client input must stay pristine")
					if tc.want == "" {
						require.JSONEq(t, `"[redacted]"`, string(got), "credential-bearing object keys must fail closed")
						return
					}
					var decoded map[string]string
					require.NoError(t, json.Unmarshal(got, &decoded), string(got))
					t.Logf("message=%q api_key=%q", decoded["message"], decoded["api_key"])
					require.Equal(t, tc.want, decoded["message"])
					require.Equal(t, "[REDACTED]", decoded["api_key"])
					require.Equal(t, original, string(raw), "classification and client input must stay pristine")
				})
			}
		})
	}
}

func TestOpenAITerminalOpsDecodedCredentialsNeverReachQueue(t *testing.T) {
	for _, secret := range []string{"q7V", "synthetic-long-credential"} {
		for _, tc := range []struct{ name, prefix, suffix string }{
			{"LF", `\n`, "; rejected"},
			{"CR", `\r`, "; rejected"},
			{"TAB", `\t`, "; rejected"},
			{"warningLF", `warning\n`, "; rejected"},
			{"warningTAB", `warning\t`, "; rejected"},
			{"unicodeLF", `warning\u000a`, "; rejected"},
			{"adjacentQuotes", `warning \"`, `\"; rejected`},
			{"embedded", "prefix", "suffix"},
		} {
			t.Run(secret+"/"+tc.name, func(t *testing.T) {
				messageJSON := `"` + tc.prefix + secret + tc.suffix + `"`
				var message string
				require.NoError(t, json.Unmarshal([]byte(messageJSON), &message))
				// Exercise all string-bearing locations, not only error.message.
				raw := []byte(`{"message":` + messageJSON + `,"detail":` + messageJSON + `,"nested":{"items":[` + messageJSON + `]},"api_key":` + messageJSON + `}`)
				original := string(raw)
				account := terminalRejectionTestAccount(1, "fixture.example")
				account.Credentials = map[string]any{"api_key": secret}
				got := redactOpenAITerminalOpsSensitiveBody(t.Context(), nil, account, raw)
				require.True(t, json.Valid(got), string(got))
				require.NotContains(t, string(got), secret)
				t.Logf("sanitized=%s", got)
				svc, _, _ := terminalRejectionTestService()
				svc.cfg.Gateway.LogUpstreamErrorBody = true
				svc.cfg.Gateway.LogUpstreamErrorBodyMaxBytes = OpsErrorLogQueueBodyMaxBytes
				c, _ := newOpenAIUpstreamErrorTestContext(t)
				svc.recordOpenAIStreamUpstreamError(c, account, false, "", "terminal_rejection", raw, message, 400)
				events := currentOpsUpstreamEvents(c)
				require.Len(t, events, 1)
				require.NotContains(t, events[0].Message, secret)
				require.NotContains(t, events[0].Detail, secret)
				input := &OpsInsertErrorLogInput{UpstreamErrors: events}
				require.NoError(t, SanitizeOpsUpstreamErrorsForQueue(input))
				require.NotNil(t, input.UpstreamErrorsJSON)
				require.NotContains(t, *input.UpstreamErrorsJSON, secret)
				t.Logf("queued=%s", *input.UpstreamErrorsJSON)
				require.Equal(t, original, string(raw))
			})
		}
	}
}
