//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPEncryptedLineageCredentialScopeNative(t *testing.T) {
	for _, change := range []string{"same", "account", "key", "base_url", "missing_secret", "old_key", "restart"} {
		t.Run(change, func(t *testing.T) {
			withResilienceSettings(t, nil)
			body := []byte(`{"model":"gpt-5.5","session_id":"m10-lineage","stream":true,"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"bad-A","summary":[]},{"type":"reasoning","id":"rs_good","encrypted_content":"good-B","summary":[]},{"type":"compaction","id":"cmp_good","encrypted_content":"good-C"}]}`)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIUpstreamErrorResponse(400, `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be verified for item rs_bad"}}`),
				webSearchSuccessStream("resp_first"), webSearchSuccessStream("resp_second"),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			svc.cfg.JWT.Secret = "m10-fixture-secret"
			if change == "missing_secret" {
				svc.cfg.JWT.Secret = ""
			}
			account := webSearchReplayAccount(false)
			first := newWebSearchReplayRequest(t)
			_, err := svc.Forward(first.ctx, first.c, account, body)
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 2)
			require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
			version := svc.UpstreamCredentialVersion(account, account.GetCredential("api_key"))
			session := svc.GenerateSessionHash(first.c, body)
			if change != "missing_secret" {
				known := svc.sessionInvalidEncryptedContentDigests(getOpenAIGroupIDFromContext(first.c), httpEncryptedLineageKey(session, account.ID, version))
				require.Equal(t, map[string]struct{}{openAIEncryptedContentDigest("bad-A"): {}}, known)
			}
			switch change {
			case "account":
				account.ID++
			case "key":
				account.Credentials["api_key"] = "rotated-fixture-key"
			case "base_url":
				account.Credentials["base_url"] = "https://other.fixture.invalid/v1"
			case "old_key":
				svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
				svc.markOpenAIWSInvalidEncryptedContentLineage(getOpenAIGroupIDFromContext(first.c), session, collectOpenAIEncryptedContentDigestsRaw(body))
			case "restart":
				svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
			}
			second := newWebSearchReplayRequest(t)
			_, err = svc.Forward(second.ctx, second.c, account, body)
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 3)
			next := upstream.bodies[2]
			require.Equal(t, change != "same", gjson.GetBytes(next, "input.0.encrypted_content").Exists())
			require.Equal(t, "good-B", gjson.GetBytes(next, "input.1.encrypted_content").String())
			require.Equal(t, "good-C", gjson.GetBytes(next, "input.2.encrypted_content").String())
		})
	}
}
