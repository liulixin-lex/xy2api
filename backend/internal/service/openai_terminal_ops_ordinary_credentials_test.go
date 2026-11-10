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

type enc252R3BareCredentialEchoUpstream struct {
	token string
	auth  string
	sends int
}

func (u *enc252R3BareCredentialEchoUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), id)
	u.sends++
	u.auth = req.Header.Get("Authorization")
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "invalid_encrypted_content", "message": "could not validate encrypted continuation context for credential " + u.token}}})
	return requestRepairFailedStream(string(payload)), nil
}
func (u *enc252R3BareCredentialEchoUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}
func TestEnc252R3OrdinaryCredentialNotPersisted(t *testing.T) {
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, pass := range []bool{false, true} {
			for _, bodyLog := range []bool{false, true} {
				t.Run(typ+"/pass_"+fmtBool(pass)+"/body_"+fmtBool(bodyLog), func(t *testing.T) {
					withResilienceSettings(t, nil)
					svc, _, _ := terminalRejectionTestService()
					account := terminalRejectionTestAccount(1, "relay-a.example")
					account.Type = typ
					const token = "synthetic-enc252-r3-bare-secret"
					account.Credentials["access_token"] = token
					account.Credentials["api_key"] = token
					account.Extra["openai_passthrough"] = pass
					require.False(t, account.IsOpenAIAgentIdentity())
					svc.cfg.Gateway.LogUpstreamErrorBody = bodyLog
					logs := captureOpenAIObservationLogs(t)
					c, _ := newOpenAIUpstreamErrorTestContext(t)
					state := resilience.NewRequestState()
					state.SetSession(9, "v2:ordinary-credential", svc.openAISessionRecorder())
					ctx := resilience.WithRequestState(c.Request.Context(), state)
					c.Request = c.Request.WithContext(ctx)
					upstream := &enc252R3BareCredentialEchoUpstream{token: token}
					svc.httpUpstream = upstream
					_, err := svc.Forward(ctx, c, account, []byte(`{"model":"requested-model","stream":true,"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`))
					require.Error(t, err)
					require.Equal(t, 1, upstream.sends)
					require.Equal(t, "Bearer "+token, upstream.auth)
					events := currentOpsUpstreamEvents(c)
					require.Len(t, events, 1)
					require.Equal(t, "terminal_rejection", events[0].Kind)
					require.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
					require.Equal(t, "other_encrypted", events[0].StateFamily)
					observed, _ := json.Marshal(events)
					leaked := strings.Contains(string(observed), token)
					t.Logf("ops_events=%d known_credential_in_ops=%t credential_in_message=%t credential_in_detail=%t credential_in_logs=%t", len(events), leaked, strings.Contains(c.GetString(OpsUpstreamErrorMessageKey), token), strings.Contains(c.GetString(OpsUpstreamErrorDetailKey), token), strings.Contains(logs.String(), token))
					for _, record := range []string{string(observed), c.GetString(OpsUpstreamErrorMessageKey), c.GetString(OpsUpstreamErrorDetailKey), logs.String()} {
						if strings.Contains(record, token) {
							t.Error("known ordinary account credential was persisted in terminal SSE ops")
						}
					}
				})
			}
		}
	}
}
