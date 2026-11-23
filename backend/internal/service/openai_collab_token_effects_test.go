//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestCollabOptionalTokenFailurePreservesAccount(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprint(optional), func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			blocker := &runtimeBlockRecorder{}
			account := &Account{ID: 17, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "synthetic-expired", "expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}}
			provider := NewOpenAITokenProvider(repo, newOpenAITokenCacheStub(), nil)
			provider.SetAccountRuntimeBlocker(blocker)
			ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: optional})
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo, openAITokenProvider: provider}
			body, err := json.Marshal(map[string]any{"model": "example-model", "input": []json.RawMessage{json.RawMessage(collabBatchTask)}, "stream": false})
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
			_, err = svc.Forward(ctx, c, account, body)
			require.ErrorContains(t, err, "refresh_token is missing")
			want := 1
			if optional {
				want = 0
			}
			require.Equal(t, want, repo.setErrorCalls)
			require.Len(t, blocker.accounts, want)
			if !optional {
				require.Equal(t, "missing_refresh_token", blocker.reasons[0])
				require.Equal(t, account.ID, blocker.accounts[0].ID)
			}
		})
	}
}

func TestCollabOptionalTokenMaintenanceStillAllowed(t *testing.T) {
	ctx := resilience.WithCollabAttempt(context.Background(), resilience.CollabAttempt{Optional: true})
	t.Run("cache", func(t *testing.T) {
		cache := newOpenAITokenCacheStub()
		repo := &rateLimitAccountRepoStub{}
		blocker := &runtimeBlockRecorder{}
		account := &Account{ID: 18, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "synthetic-valid", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
		provider := NewOpenAITokenProvider(repo, cache, nil)
		provider.SetAccountRuntimeBlocker(blocker)
		token, err := provider.GetAccessToken(ctx, account)
		require.NoError(t, err)
		require.Equal(t, "synthetic-valid", token)
		require.EqualValues(t, 1, cache.setCalled)
		require.Zero(t, repo.setErrorCalls)
		require.Empty(t, blocker.accounts)
	})
	t.Run("refresh", func(t *testing.T) {
		account := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"access_token": "synthetic-old", "refresh_token": "synthetic-refresh", "expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}}
		repo := &tokenRefreshAccountRepo{accountsByID: map[int64]*Account{account.ID: account}}
		executor := &tokenRefresherStub{credentials: map[string]any{"access_token": "synthetic-new", "refresh_token": "synthetic-next", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
		provider := NewOpenAITokenProvider(repo, nil, nil)
		provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)
		token, err := provider.GetAccessToken(ctx, account)
		require.NoError(t, err)
		require.Equal(t, "synthetic-new", token)
		require.Equal(t, 1, executor.calls)
		require.Equal(t, 1, repo.updateCredentialsCalls)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.setTempUnschedCalls)
	})
}
