//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type enc252XFlakyCredentialRepo struct {
	AccountRepository
	parent      *Account
	fail        bool
	failedReads int
}

func (r *enc252XFlakyCredentialRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.fail {
		r.failedReads++
		return nil, errors.New("synthetic credential repository outage")
	}
	return r.parent, nil
}

type enc252XShadowEchoUpstream struct {
	repo      *enc252XFlakyCredentialRepo
	assertion string
	sends     int
}

func (u *enc252XShadowEchoUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), id)
	u.assertion = req.Header.Get("Authorization")
	u.sends++
	token := u.repo.parent.GetCredential("access_token")
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": "invalid_encrypted_content", "message": "could not validate encrypted continuation context: " + u.assertion + " access_token=" + token, "access_token": token}}})
	u.repo.fail = true
	return requestRepairFailedStream(string(payload)), nil
}
func (u *enc252XShadowEchoUpstream) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}
func TestEnc252XShadowTerminalOpsFailsClosed(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, bodyLog := range []bool{false, true} {
			t.Run("pass_"+fmtBool(pass)+"/body_"+fmtBool(bodyLog), func(t *testing.T) {
				withResilienceSettings(t, nil)
				svc, _, _ := terminalRejectionTestService()
				parent := terminalRejectionTestAccount(10, "relay-a.example")
				parent.Type = AccountTypeOAuth
				key, privateKey := newTestAgentIdentityKey(t)
				parent.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
				parent.Credentials["agent_runtime_id"] = key.runtimeID
				parent.Credentials["agent_private_key"] = privateKey
				parent.Credentials["task_id"] = key.taskID
				parent.Credentials["access_token"] = "synthetic-shadow-token-not-in-shadow"
				account := terminalRejectionTestAccount(1, "relay-a.example")
				account.Type = AccountTypeOAuth
				account.ParentAccountID = &parent.ID
				account.Extra["openai_passthrough"] = pass
				repo := &enc252XFlakyCredentialRepo{parent: parent}
				svc.accountRepo = repo
				svc.cfg.Gateway.LogUpstreamErrorBody = bodyLog
				logs := captureOpenAIObservationLogs(t)
				c, _ := newOpenAIUpstreamErrorTestContext(t)
				state := resilience.NewRequestState()
				state.SetSession(9, "v2:shadow-assertion-test", svc.openAISessionRecorder())
				ctx := resilience.WithRequestState(c.Request.Context(), state)
				c.Request = c.Request.WithContext(ctx)
				upstream := &enc252XShadowEchoUpstream{repo: repo}
				svc.httpUpstream = upstream
				_, err := svc.Forward(ctx, c, account, []byte(`{"model":"requested-model","stream":true,"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"cipher"}]}]}`))
				require.Error(t, err)
				require.Equal(t, 1, upstream.sends)
				require.True(t, strings.HasPrefix(upstream.assertion, "AgentAssertion "))
				events := currentOpsUpstreamEvents(c)
				require.Len(t, events, 1)
				require.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
				require.Equal(t, "other_encrypted", events[0].StateFamily)
				require.Greater(t, repo.failedReads, 0)
				observed, _ := json.Marshal(events)
				leaked := strings.Contains(string(observed), upstream.assertion)
				t.Logf("ops_events=%d outgoing_assertion_in_ops=%t credential_repo_failed_reads=%d", len(events), leaked, repo.failedReads)
				for _, record := range []string{string(observed), c.GetString(OpsUpstreamErrorMessageKey), c.GetString(OpsUpstreamErrorDetailKey), logs.String()} {
					if strings.Contains(record, upstream.assertion) || strings.Contains(record, parent.GetCredential("access_token")) {
						t.Error("authentication assertion or token persisted when credential lookup fails")
					}
				}
			})
		}
	}
}

func TestOpenAITerminalOpsRedactionDoesNotRequireIdentityLookup(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for _, account := range []*Account{nil, {Platform: PlatformOpenAI, Type: AccountTypeOAuth}} {
		body := []byte(`AgentAssertion synthetic-assertion access_token=synthetic-known-token`)
		redacted := redactOpenAITerminalOpsSensitiveBody(context.Background(), svc.accountRepo, account, body)
		require.NotContains(t, string(redacted), "synthetic-assertion")
		require.NotContains(t, string(redacted), "synthetic-known-token")
	}
	parentID := int64(10)
	shadow := &Account{ParentAccountID: &parentID}
	for _, repo := range []AccountRepository{nil, &enc252XFlakyCredentialRepo{fail: true}, &enc252XFlakyCredentialRepo{}} {
		redacted := redactOpenAITerminalOpsSensitiveBody(context.Background(), repo, shadow, []byte("unstructured unknown parent secret"))
		require.Equal(t, "[redacted: credentials unavailable]", string(redacted))
	}
}
