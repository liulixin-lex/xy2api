//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIHTTPErrorRedactionPreservesOrdinaryBody(t *testing.T) {
	svc, _, _ := terminalRejectionTestService()
	raw := []byte(`{"error":{"type":"invalid_request_error","code":"invalid_parameter","message":"page token=abc123 cookie=xyz","next_token":"opaque-cursor"}}`)
	for _, typ := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(typ, func(t *testing.T) {
			account := terminalRejectionTestAccount(1, "relay-a.example")
			account.Type = typ
			observed := svc.redactAgentIdentitySensitiveBody(t.Context(), account, raw)
			t.Logf("ordinary_body_unchanged=%t", string(raw) == string(observed))
			require.Equal(t, raw, observed, "ordinary HTTP body must retain the 0.5.11 bytes")
		})
	}
}

type enc252HTTPRepairCredentialRepo struct {
	*enc252XFlakyCredentialRepo
	failureReads int
}

func (r *enc252HTTPRepairCredentialRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.failureReads > 0 {
		r.failureReads--
		r.failedReads++
		return nil, errors.New("synthetic HTTP credential lookup outage")
	}
	return r.enc252XFlakyCredentialRepo.GetByID(ctx, id)
}

type enc252HTTPShadowRepairUpstream struct {
	repo       *enc252HTTPRepairCredentialRepo
	bodies     [][]byte
	assertion  string
	classified bool
	svc        *OpenAIGatewayService
	account    *Account
}

func (u *enc252HTTPShadowRepairUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	RecordOpsUpstreamSendBoundary(req.Context(), id)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	u.assertion = req.Header.Get("Authorization")
	if len(u.bodies) == 1 {
		raw := []byte(`{"response":{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified for item rs_test"}}}`)
		u.repo.fail = true
		u.classified = isOpenAIEncryptedContentRejectionPayload(u.svc.redactAgentIdentitySensitiveBody(req.Context(), u.account, raw))
		u.repo.fail = false
		// Fail identity/redaction lookups after the HTTP response, then recover
		// for the next request's authentication headers.
		u.repo.failureReads = 2
		return newOpenAIUpstreamErrorResponse(http.StatusBadRequest, string(raw)), nil
	}
	u.repo.fail = true
	payload, _ := json.Marshal(map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"code": "invalid_encrypted_content", "message": "The encrypted content could not be verified for item rs_test " + u.assertion}}})
	return requestRepairFailedStream(string(payload)), nil
}
func (u *enc252HTTPShadowRepairUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

func TestOpenAIHTTPShadowLookupFailurePreservesEncryptedRepairAndOpsRedaction(t *testing.T) {
	withResilienceSettings(t, nil)
	svc, _, _ := terminalRejectionTestService()
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
	repo := &enc252HTTPRepairCredentialRepo{enc252XFlakyCredentialRepo: &enc252XFlakyCredentialRepo{parent: parent}}
	svc.accountRepo = repo
	req := newWebSearchReplayRequest(t)
	upstream := &enc252HTTPShadowRepairUpstream{repo: repo, svc: svc, account: account}
	svc.httpUpstream = upstream
	_, err := svc.Forward(req.ctx, req.c, account, []byte(encryptedRepairRequestBody))
	require.Error(t, err)
	t.Logf("lookup_failure_classified=%t upstream_attempts=%d encrypted_repair=%t", upstream.classified, len(upstream.bodies), openAIRequestRepairFrom(req.c).encryptedTried)
	require.True(t, upstream.classified, "outage must not erase HTTP rejection classification")
	require.Len(t, upstream.bodies, 2)
	require.True(t, openAIRequestRepairFrom(req.c).encryptedTried)
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
	events := currentOpsUpstreamEvents(req.c)
	observed, _ := json.Marshal(events)
	require.NotEmpty(t, events)
	require.NotContains(t, string(observed), upstream.assertion)
	require.NotContains(t, req.c.GetString(OpsUpstreamErrorMessageKey), upstream.assertion)
	require.NotContains(t, req.c.GetString(OpsUpstreamErrorDetailKey), upstream.assertion)
}
