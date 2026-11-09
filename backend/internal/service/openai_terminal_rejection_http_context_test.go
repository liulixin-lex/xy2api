//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type enc252R2HTTPKnownErrorUpstream struct {
	repo  *enc252XFlakyCredentialRepo
	sends int
}

func (u *enc252R2HTTPKnownErrorUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), id)
	u.sends++
	u.repo.fail = true
	return newOpenAIUpstreamErrorResponse(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input exceeds limit"}}`), nil
}
func (u *enc252R2HTTPKnownErrorUpstream) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}
func TestOpenAIHTTPKnownContextLimitDoesNotRebind(t *testing.T) {
	for _, pass := range []bool{false, true} {
		t.Run("pass_"+fmtBool(pass), func(t *testing.T) {
			withResilienceSettings(t, nil)
			svc, store, cache := terminalRejectionTestService()
			parent := terminalRejectionTestAccount(10, "relay-a.example")
			parent.Type = AccountTypeOAuth
			key, privateKey := newTestAgentIdentityKey(t)
			parent.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			parent.Credentials["agent_runtime_id"] = key.runtimeID
			parent.Credentials["agent_private_key"] = privateKey
			parent.Credentials["task_id"] = key.taskID
			account := terminalRejectionTestAccount(1, "relay-a.example")
			account.Type = AccountTypeOAuth
			account.ParentAccountID = &parent.ID
			account.Extra["openai_passthrough"] = pass
			repo := &enc252XFlakyCredentialRepo{parent: parent}
			svc.accountRepo = repo
			upstream := &enc252R2HTTPKnownErrorUpstream{repo: repo}
			svc.httpUpstream = upstream
			const session = "v2:http-known-error"
			cache.bindings[svc.openAISessionCacheKey(session)] = account.ID
			for range resilience.Current().Session.StreakThreshold {
				repo.fail = false
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				state := resilience.NewRequestState()
				state.SetSession(9, session, svc.openAISessionRecorder())
				ctx := resilience.WithRequestState(c.Request.Context(), state)
				c.Request = c.Request.WithContext(ctx)
				_, err := svc.Forward(ctx, c, account, []byte(`{"model":"requested-model","stream":false,"input":"hello"}`))
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, rec.Code)
			}
			avoid, err := store.SessionAvoid(t.Context(), 9, session)
			require.NoError(t, err)
			t.Logf("sends=%d owner_after=%d avoided_account_count=%d", upstream.sends, cache.bindings[svc.openAISessionCacheKey(session)], len(avoid))
			require.Equal(t, resilience.Current().Session.StreakThreshold, upstream.sends)
			require.Empty(t, avoid, "known context length rejection must not enter unknown rejection streak")
			require.Equal(t, account.ID, cache.bindings[svc.openAISessionCacheKey(session)])
		})
	}
}
