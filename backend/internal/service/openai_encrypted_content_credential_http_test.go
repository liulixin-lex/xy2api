//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPEncryptedLineageCredentialScopeNative(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, change := range []string{"same", "account", "key", "base_url", "missing_secret", "old_key", "restart", "group", "user", "model", "ttl"} {
			t.Run(fmt.Sprintf("passthrough=%t/%s", passthrough, change), func(t *testing.T) {
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
				account := webSearchReplayAccount(passthrough)
				first := newWebSearchReplayRequest(t)
				groupID := int64(7)
				first.c.Set("api_key", &APIKey{ID: 101, UserID: 1, GroupID: &groupID})
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
				case "ttl":
					store := svc.getOpenAIWSStateStore().(*defaultOpenAIWSStateStore)
					store.sessionInvalidEncryptedMu.Lock()
					for key, binding := range store.sessionInvalidEncrypted {
						binding.expiresAt = time.Now().Add(-time.Second)
						store.sessionInvalidEncrypted[key] = binding
					}
					store.sessionInvalidEncryptedMu.Unlock()
				}
				second := newWebSearchReplayRequest(t)
				secondKey := &APIKey{ID: 101, UserID: 1, GroupID: &groupID}
				if change == "group" {
					otherGroup := int64(8)
					secondKey.GroupID = &otherGroup
				}
				if change == "user" {
					secondKey.ID = 102
					secondKey.UserID = 2
				}
				second.c.Set("api_key", secondKey)
				if change == "model" {
					body = []byte(strings.Replace(string(body), "gpt-5.5", "gpt-5.6-sol", 1))
				}
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

}

func TestHTTPEncryptedLineageCredentialScopeM3(t *testing.T) {
	for _, change := range []string{"same", "key", "base_url", "token", "missing_secret", "old_key", "restart"} {
		t.Run(change, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{JWT: config.JWTConfig{Secret: "fixture-secret"}}, openaiWSStateStore: NewOpenAIWSStateStore(nil)}
			account := webSearchReplayAccount(false)
			req := newWebSearchReplayRequest(t)
			body := []byte(m3TwoItemRequest)
			token := "actual-token"
			if change == "missing_secret" {
				svc.cfg.JWT.Secret = ""
			}
			version := svc.UpstreamCredentialVersion(account, token)
			attempt := svc.armOpenAIM3(req.c, account, body, version)
			// Rotate the mutable account after arming: persistence still uses the send's version.
			account.Credentials["api_key"] = "mutated-after-send"
			learnErr := attempt.learn(req.ctx, body, nil, "Encrypted content could not be verified for item amsg_one", 400, 0)
			var signal *openAIRequestRepairSignal
			require.ErrorAs(t, learnErr, &signal)
			if version != "" {
				require.Len(t, svc.getOpenAIWSStateStore().GetSessionBadEncryptedSlots(attempt.request.groupID, openAIEncryptedLineageVersionedSession(attempt.request.session, version), account.ID), 1)
			}
			switch change {
			case "key":
				token = "rotated-key"
			case "base_url":
				account.Credentials["base_url"] = "https://different.fixture.invalid/v1"
			case "token":
				token = "rotated-token"
			case "old_key":
				svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
				slots, _, err := openAIM3Slots(body)
				require.NoError(t, err)
				svc.openaiWSStateStore.MarkSessionBadEncryptedSlots(attempt.request.groupID, attempt.request.session, account.ID, []openAIBadEncryptedSlot{slots[0].id.openAIBadEncryptedSlot}, svc.openAIWSSessionStickyTTL())
			case "restart":
				svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
			}
			second := newWebSearchReplayRequest(t)
			next := svc.armOpenAIM3(second.c, account, body, svc.UpstreamCredentialVersion(account, token))
			derived, err := next.derive(context.Background(), body)
			require.NoError(t, err)
			require.Equal(t, change != "same", strings.Contains(string(derived), `"id":"amsg_one","content":[{"type":"encrypted_content"`))
			require.Equal(t, "encrypted_content", gjson.GetBytes(derived, "input.1.content.0.type").String())
		})
	}
}
