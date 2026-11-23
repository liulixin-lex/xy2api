//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collabLastUsedRepo struct {
	AccountRepository
	f      *selectionRuntimeFixture
	writes int
}

func (r *collabLastUsedRepo) BatchUpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	for i := range r.f.repo.accounts {
		if at, ok := updates[r.f.repo.accounts[i].ID]; ok {
			r.writes++
			r.f.repo.accounts[i].LastUsedAt = &at
		}
	}
	return nil
}
func TestCollabOptionalOllamaFailureLastUsed(t *testing.T) {
	for _, target := range []string{"https://ollama.com", "https://opencode.ai/zen/go/v1"} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional_%t", target, optional), func(t *testing.T) {
				a, b := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
				a.Credentials = map[string]any{"api_key": "synthetic", "base_url": target}
				old := time.Now().Add(-time.Hour)
				b.LastUsedAt = &old
				f := newSelectionRuntimeFixture(t, a, b)
				repo := &collabLastUsedRepo{f: f}
				deferred := NewDeferredService(repo, nil, time.Second)
				f.svc.deferredService = deferred
				f.svc.responseHeaderFilter = compileResponseHeaderFilter(f.svc.cfg)
				upstream := &failingOpenAIHTTPUpstream{err: errors.New("connection reset")}
				f.svc.httpUpstream = upstream
				parent := resilience.NewRequestState()
				child := resilience.NewCollabChildState(parent)
				ctx := resilience.WithCollabAttempt(resilience.WithRequestState(t.Context(), child), resilience.CollabAttempt{Optional: optional})
				selected, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.EqualValues(t, 1, selected.Account.ID)
				body, _ := json.Marshal(map[string]any{"model": "m", "stream": false, "input": []json.RawMessage{json.RawMessage(collabBatchTask)}})
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
				_, err = f.svc.Forward(ctx, c, selected.Account, body)
				require.Error(t, err)
				require.Positive(t, upstream.calls)
				CombineSelectionRelease(selected, nil)()
				_, queued := deferred.lastUsedUpdates.Load(a.ID)
				deferred.flushLastUsed()
				next, err := f.svc.SelectCollabAccount(resilience.WithRequestState(t.Context(), resilience.NewRequestState()), ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, next)
				CombineSelectionRelease(next, nil)()
				t.Logf("optional=%t queued=%t persisted=%d next_account=%d", optional, queued, repo.writes, next.Account.ID)
				want := 1
				wantID := int64(2)
				if optional {
					want = 0
					wantID = 1
				}
				assert.Equal(t, want, repo.writes)
				assert.Equal(t, wantID, next.Account.ID)
			})
		}
	}
}
func TestCollabSettlementLastUsedInventory(t *testing.T) {
	for _, mode := range []string{"ordinary", "delivered_optional", "abandoned_optional"} {
		t.Run(mode, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, &openAIRecordUsageBillingRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.cfg.RunMode = config.RunModeSimple
			attempt := resilience.CollabAttempt{Optional: mode != "ordinary", Delivered: mode == "delivered_optional", Abandoned: mode == "abandoned_optional"}
			ctx := resilience.WithCollabAttempt(t.Context(), attempt)
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "synthetic-settlement", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 10, OutputTokens: 1}},
				APIKey: &APIKey{ID: 1, Group: &Group{RateMultiplier: 1}}, User: &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			_, queued := svc.deferredService.lastUsedUpdates.Load(int64(3))
			t.Logf("mode=%s logs=%d last_used_queued=%t", mode, logs.calls, queued)
			require.Equal(t, mode != "abandoned_optional", queued)
		})
	}
}

func TestCollabDeliveredSettlementRetainsLastUsedAndQuota(t *testing.T) {
	var ordinaryQuota float64
	for _, mode := range []string{"ordinary", "delivered_optional", "undelivered_optional"} {
		t.Run(mode, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := &openAIRecordUsageBillingRepoStub{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			a, b := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
			a.Extra = map[string]any{"quota_limit": 1.0}
			old := time.Now().Add(-time.Hour)
			b.LastUsedAt = &old
			f := newSelectionRuntimeFixture(t, a, b)
			repo := &collabLastUsedRepo{f: f}
			svc.deferredService = NewDeferredService(repo, nil, time.Second)
			ctx := resilience.WithCollabAttempt(t.Context(), resilience.CollabAttempt{Optional: mode != "ordinary", Delivered: mode == "delivered_optional"})
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "synthetic-settled", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 20}},
				APIKey: &APIKey{ID: 11, Group: &Group{RateMultiplier: 1}}, User: &User{ID: 12}, Account: &a,
			})
			require.NoError(t, err)
			svc.deferredService.flushLastUsed()
			if mode == "undelivered_optional" {
				require.Zero(t, billing.calls)
				require.Zero(t, repo.writes)
				require.Zero(t, logs.calls)
				return
			}
			require.Equal(t, 1, billing.calls)
			require.Positive(t, billing.lastCmd.BalanceCost)
			require.Equal(t, 1, repo.writes)
			require.Positive(t, billing.lastCmd.AccountQuotaCost)
			if mode == "ordinary" {
				ordinaryQuota = billing.lastCmd.AccountQuotaCost
			} else {
				require.Equal(t, ordinaryQuota, billing.lastCmd.AccountQuotaCost)
			}
		})
	}
}
