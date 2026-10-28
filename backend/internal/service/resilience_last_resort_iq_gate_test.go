//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

const iqLastResortModel = "gpt-5.1"

type iqLastResortMode struct {
	name     string
	advanced bool
	share    bool
}

func newIQLastResortService(t *testing.T, accounts ...Account) (*OpenAIGatewayService, *schedulerTestOpenAIAccountRepo) {
	t.Helper()
	withResilienceSettings(t, nil)
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	repo := &schedulerTestOpenAIAccountRepo{accounts: accounts}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        openAIGroupListingRepo{*repo},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	return svc, repo
}

func setIQLastResortAdvanced(t *testing.T, enabled bool) {
	t.Helper()
	openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
		enabled:   enabled,
		expiresAt: time.Now().Add(time.Minute).UnixNano(),
	})
}

func iqLastResortAccount(id int64) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 2, AccountGroups: []AccountGroup{{GroupID: shareTestGroupID}}}
}

func iqLastResortExtra(key string, resetAt time.Time) map[string]any {
	entry := map[string]any{}
	if !resetAt.IsZero() {
		entry["rate_limit_reset_at"] = resetAt.Format(time.RFC3339)
	}
	return map[string]any{"model_rate_limits": map[string]any{key: entry}}
}

func selectIQLastResort(t *testing.T, svc *OpenAIGatewayService, model string, requireCompact bool) (*AccountSelectionResult, error) {
	t.Helper()
	groupID := shareTestGroupID
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "iq-last-resort-session", model, nil, OpenAIUpstreamTransportAny, requireCompact)
	return selection, err
}

func TestOpenAILastResortKeepsShortOrdinaryModelLimitSoft(t *testing.T) {
	for _, mode := range []iqLastResortMode{{name: "legacy"}, {name: "advanced", advanced: true}} {
		t.Run(mode.name, func(t *testing.T) {
			reset := time.Now().Add(60 * time.Second)
			account := iqLastResortAccount(1)
			account.Extra = iqLastResortExtra(iqLastResortModel, reset)
			svc, _ := newIQLastResortService(t, account)
			setIQLastResortAdvanced(t, mode.advanced)

			selection, err := selectIQLastResort(t, svc, iqLastResortModel, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(1), selection.Account.ID)
		})
	}
}

func TestOpenAILastResortRejectsIQGateRegardlessOfReset(t *testing.T) {
	resetCases := []struct {
		name    string
		resetAt time.Time
	}{
		{name: "without reset"},
		{name: "future reset", resetAt: time.Now().Add(60 * time.Second)},
		{name: "expired reset", resetAt: time.Now().Add(-60 * time.Second)},
	}
	modes := []iqLastResortMode{{name: "legacy"}, {name: "advanced", advanced: true}, {name: "share", share: true}}
	for _, mode := range modes {
		for _, resetCase := range resetCases {
			t.Run(mode.name+"/"+resetCase.name, func(t *testing.T) {
				account := iqLastResortAccount(1)
				account.Extra = iqLastResortExtra("iq:"+iqLastResortModel, resetCase.resetAt)
				tier, _, _ := resiliencePenaltyOf(&account, iqLastResortModel, time.Now())
				require.Equal(t, resilience.TierHard, tier)
				var svc *OpenAIGatewayService
				if mode.share {
					env := newOpenAILastResortEnv(t, sharesched.SchedulerShare, 1, 5)
					env.account(lrAccountA).Extra = iqLastResortExtra("iq:"+iqLastResortModel, resetCase.resetAt)
					env.account(lrAccountB).Extra = iqLastResortExtra("iq:"+iqLastResortModel, resetCase.resetAt)
					svc = env.svc
				} else {
					svc, _ = newIQLastResortService(t, account)
					setIQLastResortAdvanced(t, mode.advanced)
				}

				selection, err := selectIQLastResort(t, svc, iqLastResortModel, false)
				if err == nil {
					require.Nil(t, selection)
				}
				require.ErrorIs(t, err, ErrNoAvailableAccounts)
			})
		}
	}
}

func TestOpenAILastResortRejectsCompactOnlyIQGate(t *testing.T) {
	account := iqLastResortAccount(1)
	account.RateLimitResetAt = timePointer(time.Now().Add(60 * time.Second))
	account.Credentials = map[string]any{
		"model_mapping":         map[string]any{"public": "gpt-6-astra"},
		"compact_model_mapping": map[string]any{"public": "gpt-5.6-luna"},
	}
	account.Extra = iqLastResortExtra("iq:gpt-5.6-luna", time.Now().Add(-60*time.Second))
	svc, _ := newIQLastResortService(t, account)
	setIQLastResortAdvanced(t, false)

	selection, err := selectIQLastResort(t, svc, "public", true)
	if err == nil {
		require.Nil(t, selection)
	}
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestOpenAILastResortFreshVerificationRejectsNewIQGate(t *testing.T) {
	modes := []iqLastResortMode{{name: "legacy"}, {name: "advanced", advanced: true}, {name: "share", share: true}}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			var svc *OpenAIGatewayService
			var repo *schedulerTestOpenAIAccountRepo
			if mode.share {
				env := newOpenAILastResortEnv(t, sharesched.SchedulerShare, 1, 5)
				svc, repo = env.svc, &env.repo
			} else {
				account := iqLastResortAccount(1)
				account.RateLimitResetAt = timePointer(time.Now().Add(60 * time.Second))
				svc, repo = newIQLastResortService(t, account)
				setIQLastResortAdvanced(t, mode.advanced)
			}
			_, err := svc.resilience.groupAccountsForLastResort(context.Background(), svc.accountRepo, shareTestGroupID)
			require.NoError(t, err)

			for i := range repo.accounts {
				if repo.accounts[i].ID == lrAccountB {
					repo.accounts[i].Schedulable = false
				}
				if repo.accounts[i].ID == 1 || repo.accounts[i].ID == lrAccountA {
					repo.accounts[i].Extra = iqLastResortExtra("iq:"+iqLastResortModel, time.Now().Add(-60*time.Second))
				}
			}

			selection, err := selectIQLastResort(t, svc, iqLastResortModel, false)
			if err == nil {
				require.Nil(t, selection, "fresh IQ-gated account must not be dispatched")
			}
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
		})
	}
}

func TestOpenAILastResortFreshCompactGateUsesFreshMapping(t *testing.T) {
	planned := iqLastResortAccount(1)
	planned.Credentials = map[string]any{
		"model_mapping":         map[string]any{"gpt-6-astra": "gpt-6-astra"},
		"compact_model_mapping": map[string]any{"gpt-6-astra": "gpt-6.1-sol"},
	}
	planned.Extra = map[string]any{"openai_responses_mode": "force_responses"}
	fresh := planned
	fresh.Credentials = map[string]any{
		"model_mapping":         map[string]any{"gpt-6-astra": "gpt-6-astra"},
		"compact_model_mapping": map[string]any{"gpt-6-astra": "gpt-5.6-luna"},
	}
	fresh.Extra = iqLastResortExtra("iq:gpt-5.6-luna", time.Now().Add(-time.Minute))
	accounts := &iqRunAccounts{accounts: map[int64]*Account{fresh.ID: &fresh}}
	gateway := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICompactModel: "gpt-6.1-sol"}}}
	resilienceService := NewResilienceService(nil, nil)

	_, ok := resilienceService.verifyLastResortAccount(context.Background(), accounts, shareTestGroupID, &planned, "gpt-6-astra", true,
		resiliencePenaltyOf, func(account *Account) string {
			return gateway.openAICodexTicketOutboundModel(account, "gpt-6-astra", true)
		})
	require.False(t, ok, "the fresh luna compact mapping must be checked")
}

func timePointer(value time.Time) *time.Time {
	return &value
}
