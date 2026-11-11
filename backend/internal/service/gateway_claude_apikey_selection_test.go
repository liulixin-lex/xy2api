//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type compatSelectionConcurrency struct {
	mockConcurrencyCache
	loaded, acquired []int64
}

func (c *compatSelectionConcurrency) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	for _, account := range accounts {
		c.loaded = append(c.loaded, account.ID)
	}
	return c.mockConcurrencyCache.GetAccountsLoadBatch(ctx, accounts)
}

func (c *compatSelectionConcurrency) AcquireAccountSlot(ctx context.Context, id int64, max int, requestID string) (bool, error) {
	c.acquired = append(c.acquired, id)
	return c.mockConcurrencyCache.AcquireAccountSlot(ctx, id, max, requestID)
}

func TestClaudeAPIKeyCompatSelectionBeforeLoad(t *testing.T) {
	for _, tc := range []struct{ entry, body string }{
		{"messages", `{"model":"claude-opus-5-5","context_management":{"edits":[{"type":"compact_20260112"}]}}`},
		{"responses", `{"model":"claude-opus-5-5","previous_response_id":"resp-provider-owned"}`},
		{"responses", `{"model":"claude-opus-5-5","input":[{"type":"reasoning","encrypted_content":"other-provider-state"}]}`},
	} {
		for _, alternative := range []bool{false, true} {
			t.Run(tc.entry+tc.body+map[bool]string{false: "/alone", true: "/alternative"}[alternative], func(t *testing.T) {
				a := compatTestAccount()
				repo := &mockAccountRepoForPlatform{accounts: []Account{*a}, accountsByID: map[int64]*Account{a.ID: a}}
				if alternative {
					off := *compatTestAccount()
					off.ID++
					off.Extra = nil
					off.Priority = a.Priority + 1
					repo.accounts = append(repo.accounts, off)
					repo.accountsByID[off.ID] = &off
				}
				s := compatTestService(newCompatMemoryStore())
				s.accountRepo = repo
				s.cfg.Gateway.Scheduling.LoadBatchEnabled = true
				concurrency := &compatSelectionConcurrency{}
				s.concurrencyService = NewConcurrencyService(concurrency)
				ctx, err := s.PrepareAnthropicCompatibilityForSelection(context.Background(), 1, 2, []byte(tc.body), tc.entry, "")
				require.NoError(t, err)
				require.True(t, IsAnthropicCompatCandidateError(s.anthropicCompatCandidateEligibility(ctx, a, "claude-opus-5-5")))
				result, err := s.SelectAccountWithLoadAwareness(ctx, nil, "", "claude-opus-5-5", nil, "", 0)
				if alternative {
					require.NoError(t, err)
					require.Equal(t, a.ID+1, result.Account.ID)
					result.ReleaseFunc()
					require.Equal(t, []int64{a.ID + 1}, concurrency.acquired)
				} else {
					require.ErrorIs(t, err, ErrNoAvailableAccounts)
					require.Nil(t, result)
					require.Zero(t, concurrency.loadBatchCalls)
					require.Zero(t, concurrency.acquireAccountCalls)
				}
				require.NotContains(t, concurrency.loaded, a.ID)
				require.NotContains(t, concurrency.acquired, a.ID)
			})
		}
	}
}

func TestClaudeAPIKeyCompatSelectionInvalidFieldsRemain400(t *testing.T) {
	s := compatTestService(newCompatMemoryStore())
	for _, body := range []string{`{"system":42}`, `{"metadata":"invalid"}`} {
		_, err := s.PrepareAnthropicCompatibilityForSelection(context.Background(), 1, 2, []byte(body), "messages", "")
		status, _, _, ok := AnthropicCompatErrorDetails(err)
		require.True(t, ok)
		require.Equal(t, http.StatusBadRequest, status)
		require.False(t, IsAnthropicCompatCandidateError(err))
	}
	_, err := s.PrepareAnthropicCompatibilityForSelection(context.Background(), 1, 2, []byte(`{"previous_response_id":"resp-1","input":[{"type":"reasoning","encrypted_content":"anthropic-thinking-v1:invalid"}]}`), "responses", "")
	status, _, _, ok := AnthropicCompatErrorDetails(err)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, status)
	require.False(t, IsAnthropicCompatCandidateError(err), "a capability mismatch must not hide a malformed signed envelope")
}

func TestClaudeAPIKeyCompatBoundSelectionNeverFailsOver(t *testing.T) {
	for _, failure := range []string{"capability", "model", "profile revision", "excluded", "missing"} {
		t.Run(failure, func(t *testing.T) {
			store := newCompatMemoryStore()
			s := compatTestService(store)
			a := compatTestAccount()
			off := *compatTestAccount()
			off.ID++
			off.Extra = nil
			repo := &mockAccountRepoForPlatform{accounts: []Account{*a, off}, accountsByID: map[int64]*Account{a.ID: a, off.ID: &off}}
			s.accountRepo = repo
			concurrency := &compatSelectionConcurrency{}
			s.concurrencyService = NewConcurrencyService(concurrency)
			body := []byte(`{"model":"claude-opus-5-5"}`)
			if failure == "capability" {
				body = []byte(`{"model":"claude-opus-5-5","compaction":{"type":"summarize"}}`)
			}
			ctx, err := s.PrepareAnthropicCompatibilityForSelection(context.Background(), 1, 2, body, "messages", "")
			require.NoError(t, err)
			profile, err := ResolveAnthropicCompatProfile(a)
			require.NoError(t, err)
			record := &anthropicCompatRecord{AccountID: a.ID, Model: "claude-opus-5-5", ProfileRevision: profile.Revision, CredentialRevision: compatCredentialRevision(store, a)}
			compatScope(ctx).Record = record
			excluded := map[int64]struct{}{}
			model := record.Model
			wantStatus := http.StatusBadRequest
			switch failure {
			case "model":
				model = "claude-opus-4-8"
			case "profile revision":
				record.ProfileRevision = "older-profile-revision"
			case "excluded":
				excluded[a.ID] = struct{}{}
				wantStatus = http.StatusServiceUnavailable
			case "missing":
				repo.accounts = []Account{off}
				wantStatus = http.StatusServiceUnavailable
			}
			result, err := s.SelectAccountWithLoadAwareness(ctx, nil, "", model, excluded, "", 0)
			require.Nil(t, result)
			require.Error(t, err)
			require.False(t, IsAnthropicCompatCandidateError(err))
			status, _, _, ok := AnthropicCompatErrorDetails(err)
			require.True(t, ok)
			require.Equal(t, wantStatus, status)
			require.Empty(t, concurrency.loaded)
			require.Empty(t, concurrency.acquired)
		})
	}
}

func TestClaudeAPIKeyCompatMappingAndAvailability(t *testing.T) {
	s := compatTestService(newCompatMemoryStore())
	a := compatTestAccount()
	a.Credentials["model_mapping"] = map[string]any{"dispatch-opus": "claude-opus-5-5"}
	ctx := WithAnthropicCompatDispatchModel(context.Background(), "dispatch-opus")
	require.True(t, s.isModelSupportedByAccountWithContext(ctx, a, "public-opus"))
	require.False(t, s.isModelSupportedByAccountWithContext(context.Background(), a, "claude-opus-5-5"), "mapping admission and profile support must intersect")
	// Persistently configured support survives cooldown, missing identity and a
	// request capability rejection; none of these should become a model 404.
	delete(a.Extra, AnthropicCompatIdentitySeedExtraKey)
	until := time.Now().Add(time.Hour)
	a.RateLimitResetAt = &until
	s.accountRepo = &mockAccountRepoForPlatform{accounts: []Account{*a}}
	ctx, err := s.PrepareAnthropicCompatibilityForSelection(ctx, 1, 2, []byte(`{"compaction":true}`), "messages", "")
	require.NoError(t, err)
	require.False(t, s.anthropicCompatCandidateEligible(ctx, a, "public-opus"))
	diag := s.DiagnoseModelAvailabilityForPlatform(ctx, nil, "public-opus", PlatformAnthropic)
	require.True(t, diag.HasAccountsInPool)
	require.True(t, diag.HasModelSupport)
	require.False(t, s.DiagnoseModelAvailabilityForPlatform(context.Background(), nil, "claude-sonnet-4-6", PlatformAnthropic).HasModelSupport)
	catalog := s.GetAvailableModelCatalog(context.Background(), nil, PlatformAnthropic)
	require.True(t, catalog.RestrictFallback)
	require.Equal(t, []string{"dispatch-opus"}, catalog.Models)
}
