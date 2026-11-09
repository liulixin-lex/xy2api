//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type stateRejectionAssertionEchoUpstream struct {
	assertion string
	sends     int
	token     string
}

func (u *stateRejectionAssertionEchoUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), accountID)
	u.assertion = req.Header.Get("Authorization")
	u.sends++
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "invalid_encrypted_content", "message": "could not validate encrypted continuation context: " + u.assertion + " credential " + u.token}}})
	return requestRepairFailedStream(string(payload)), nil
}
func (u *stateRejectionAssertionEchoUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, n)
}
func TestOpenAIStreamStateRejectionRedactsPersistedCredentials(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, logBody := range []bool{false, true} {
			t.Run("passthrough_"+fmtBool(passthrough)+"/body_"+fmtBool(logBody), func(t *testing.T) {
				withResilienceSettings(t, nil)
				svc, _, _ := terminalRejectionTestService()
				account := terminalRejectionTestAccount(1, "relay-a.example")
				account.Type = AccountTypeOAuth
				key, privateKey := newTestAgentIdentityKey(t)
				account.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
				account.Credentials["agent_runtime_id"] = key.runtimeID
				account.Credentials["agent_private_key"] = privateKey
				account.Credentials["task_id"] = key.taskID
				account.Extra["openai_passthrough"] = passthrough
				svc.cfg.Gateway.LogUpstreamErrorBody = logBody
				account.Credentials["access_token"] = "synthetic-enc252-token-value"
				logs := captureOpenAIObservationLogs(t)
				c, _ := newOpenAIUpstreamErrorTestContext(t)
				state := resilience.NewRequestState()
				state.SetSession(9, "v2:assertion-test", svc.openAISessionRecorder())
				ctx := resilience.WithRequestState(c.Request.Context(), state)
				c.Request = c.Request.WithContext(ctx)
				upstream := &stateRejectionAssertionEchoUpstream{token: account.GetCredential("access_token")}
				svc.httpUpstream = upstream
				_, err := svc.Forward(ctx, c, account, []byte(`{"model":"requested-model","stream":true,"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`))
				require.Error(t, err)
				require.Equal(t, 1, upstream.sends)
				require.True(t, strings.HasPrefix(upstream.assertion, "AgentAssertion "))
				events := currentOpsUpstreamEvents(c)
				require.Len(t, events, 1)
				require.Equal(t, "terminal_rejection", events[0].Kind)
				require.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
				observed, _ := json.Marshal(events)
				leaked := strings.Contains(string(observed), upstream.assertion)
				t.Logf("ops_events=%d outgoing_assertion_in_ops=%t", len(currentOpsUpstreamEvents(c)), leaked)
				if leaked {
					t.Error("outgoing authentication assertion persisted without redaction")
				}
				for _, record := range []string{string(observed), c.GetString(OpsUpstreamErrorMessageKey), c.GetString(OpsUpstreamErrorDetailKey), logs.String()} {
					if strings.Contains(record, upstream.assertion) || strings.Contains(record, upstream.token) {
						t.Error("sensitive authentication value in persistent record or log")
					}
				}
			})
		}
	}
}
