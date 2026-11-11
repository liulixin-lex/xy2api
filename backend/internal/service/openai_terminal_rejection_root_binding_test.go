//go:build unit

package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAITerminalRejectionRootRebindKeepsNewChildrenOnOwner(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("sse_"+fmtBool(stream), func(t *testing.T) {
			withResilienceSettings(t, nil)
			groupID := int64(102091)
			accounts := []Account{
				{ID: 39101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"}},
				{ID: 39102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"plan_type": "team"}},
			}
			cfg := &config.Config{}
			store := newMemResilienceStore()
			repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
			cache := &atomicStickyTestCache{bindings: make(map[string]int64)}
			svc := &OpenAIGatewayService{
				cfg: cfg, accountRepo: repo, cache: cache, resilience: NewResilienceService(nil, store),
				rateLimitService:   newOpenAISelectionRateLimitService(),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{39101: true, 39102: true}}),
			}

			root, _ := newOpenAIUpstreamErrorTestContext(t)
			root.Set("api_key", &APIKey{ID: 7, GroupID: &groupID})
			root.Request.Header.Set("session-id", "root-thread")
			root.Request.Header.Set(openAIWSThreadIDHeader, "root-thread")
			body := []byte(`{"model":"gpt-6-sol","input":"hello"}`)
			session := svc.GenerateSessionHash(root, body)
			cache.bindings[svc.openAISessionCacheKey(session)] = 39101
			state := resilience.NewRequestState()
			state.SetSession(groupID, session, svc.openAISessionRecorder())
			ctx := resilience.WithRequestState(root.Request.Context(), state)
			ctx = WithOpenAITerminalRejectionScope(ctx, root, body)
			root.Request = root.Request.WithContext(ctx)
			account := terminalRejectionTestAccount(39101, "relay-a.example")
			for range resilience.Current().Session.StreakThreshold {
				if stream {
					finish := beginOpenAIStateRejectionAttempt(ctx, root, account, body)
					payload := []byte(`{"type":"response.failed","response":{"error":{"code":"client_tool_continuation_unavailable","message":"opaque conversation state"}}}`)
					captureOpenAIStateRejection(root, account, payload, "opaque conversation state", "native_sse", false, nil, http.StatusBadRequest)
					err := errors.New("terminal")
					svc.finishOpenAIStreamStateRejection(ctx, root, account, &err)
					finish(&err)
				} else {
					svc.noteOpenAITerminalRejection(ctx, account, http.StatusBadRequest, nil, []byte(terminalRejectionBody), errors.New("terminal"))
				}
			}
			selected, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", session, "gpt-6-sol", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selected)
			require.Equal(t, int64(39102), selected.Account.ID)
			if selected.ReleaseFunc != nil {
				selected.ReleaseFunc()
			}
			require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &groupID, session, selected.Account.ID))
			child, _ := newOpenAIUpstreamErrorTestContext(t)
			child.Set("api_key", &APIKey{ID: 7, GroupID: &groupID})
			child.Request.Header.Set("session-id", "root-thread")
			child.Request.Header.Set(openAIWSThreadIDHeader, "new-child")
			child.Request.Header.Set(codexParentThreadIDHeader, "root-thread")
			child.Request.Header.Set(openAISubagentHeader, "collab_spawn")
			childBody := []byte(`{"model":"gpt-6-sol","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"from-new-root-owner"}]}]}`)
			childHash := svc.GenerateSessionHash(child, childBody)
			childCtx := svc.WithOpenAIGuardianParentAffinity(child.Request.Context(), child, childBody, "gpt-6-sol")
			selectedChild, _, err := svc.SelectAccountWithScheduler(childCtx, &groupID, "", childHash, "gpt-6-sol", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selectedChild)
			if selectedChild.ReleaseFunc != nil {
				defer selectedChild.ReleaseFunc()
			}
			t.Logf("root_selected=%d shared_binding=%d new_child_selected=%d", selected.Account.ID, cache.bindings[svc.openAISessionCacheKey(session)], selectedChild.Account.ID)
			require.Equal(t, selected.Account.ID, selectedChild.Account.ID, "new child carrying new root owner's ciphertext must follow that owner")

		})
	}
}
