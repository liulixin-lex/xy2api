//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeAPIKeyCompatBoundSelectionRetainsProfitGate(t *testing.T) {
	for _, mode := range []string{"no_concurrency", "acquired", "waiting"} {
		t.Run(mode, func(t *testing.T) {
			group := gatewayProfitTestGroup(991221, PlatformAnthropic)
			entry := gatewayProfitTestGroup(991220, PlatformAnthropic)
			entry.ClaudeCodeOnly, entry.FallbackGroupID, entry.ProfitControlEnabled = true, &group.ID, false
			account := *compatTestAccount()
			rate := 0.2
			account.RateMultiplier = &rate
			account.AccountGroups = []AccountGroup{{AccountID: account.ID, GroupID: group.ID}}
			account.GroupIDs = []int64{group.ID}
			repo := &mockAccountRepoForPlatform{accounts: []Account{account}, accountsByID: map[int64]*Account{account.ID: &account}}
			store := newCompatMemoryStore()
			svc := &GatewayService{accountRepo: repo, groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{entry.ID: entry, group.ID: group}}, cache: store, cfg: testConfig()}
			if mode != "no_concurrency" {
				svc.concurrencyService = NewConcurrencyService(&mockConcurrencyCache{acquireResults: map[int64]bool{account.ID: mode == "acquired"}})
			}
			ctx := gatewayProfitTestContext(entry)
			profile, err := ResolveAnthropicCompatProfile(&account)
			require.NoError(t, err)
			scope := &anthropicCompatScope{Record: &anthropicCompatRecord{AccountID: account.ID, ProfileRevision: profile.Revision, CredentialRevision: compatCredentialRevision(store, &account)}}
			ctx = context.WithValue(ctx, anthropicCompatScopeKey{}, scope)
			bound, err := svc.SelectAccountWithLoadAwareness(ctx, &entry.ID, "", "claude-opus-5-5", nil, "", 0)
			require.NoError(t, err)
			require.NotNil(t, bound)
			if bound.ReleaseFunc != nil {
				defer bound.ReleaseFunc()
			}
			require.Equal(t, mode != "waiting", bound.Acquired)
			if mode == "waiting" {
				require.NotNil(t, bound.WaitPlan)
			}
			increased := *bound.Account
			expensive := 0.9
			increased.RateMultiplier = &expensive
			_, vetoed, reason := svc.GatewayProfitControlVetoLatest(ContextWithSelectionProfitGate(ctx, bound), &increased)
			t.Logf("mode=%s gate=%v veto=%v reason=%s", mode, bound.ProfitGateActive(), vetoed, reason)
			require.True(t, bound.ProfitGateActive())
			gate, ok := ContextWithSelectionProfitGate(ctx, bound).Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
			require.True(t, ok)
			require.Equal(t, group.ID, gate.groupID)
			require.True(t, vetoed)
			require.Equal(t, "profit_threshold", reason)
		})
	}
}
