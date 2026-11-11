//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func TestOpenAILegacyReplacementSessionTriesNextSlot(t *testing.T) {
	group := int64(73)
	accounts := []Account{}
	for id := int64(1); id <= 3; id++ {
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}})
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	session := "replacement"
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
	first, second := int64(2), int64(3)
	acquired := []int64{}
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap:        map[int64]*AccountLoadInfo{2: {LoadRate: 0}, 3: {LoadRate: 20}},
		acquireResults: map[int64]bool{first: false, second: true}, acquiredIDs: &acquired,
	})
	svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:" + session: 1}}
	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &group, session, "gpt-test", map[int64]struct{}{1: {}})
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Equal(t, second, result.Account.ID)
	require.Equal(t, []int64{first, second}, acquired)
	result.ReleaseFunc()
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{2: false, 3: false},
	})
	result, err = svc.SelectAccountWithLoadAwareness(context.Background(), &group, session, "gpt-test", map[int64]struct{}{1: {}})
	require.NoError(t, err)
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan, "all full candidates fall back to the existing queue")
	// Initial selection and requests without a session retain minimum-load order.
	for _, key := range []string{"", "new-session"} {
		acquired = nil
		svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{1: {LoadRate: 100}, 2: {LoadRate: 0}, 3: {LoadRate: 20}}, acquiredIDs: &acquired})
		result, err = svc.SelectAccountWithLoadAwareness(context.Background(), &group, key, "gpt-test", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), result.Account.ID)
		result.ReleaseFunc()
	}
}

func TestOpenAIShareCompactReplacementKeepsLowestLoad(t *testing.T) {
	env := newOpenAIShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShare), 1, 2, 3)
	env.svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
	for _, id := range []int64{2, 3} {
		env.account(id).Extra = map[string]any{"openai_compact_supported": true}
	}
	env.svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{2: {LoadRate: 0}, 3: {LoadRate: 20}},
	})
	session := "compact-replacement"
	env.cache.bind("openai:"+session, 1, time.Hour)
	group := shareTestGroupID
	require.True(t, env.share.shareSchedulingActive(group))
	result, decision, err := env.svc.SelectAccountWithScheduler(context.Background(), &group, "", session, "gpt-5.1",
		map[int64]struct{}{1: {}}, OpenAIUpstreamTransportAny, true)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	defer result.ReleaseFunc()
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, int64(2), result.Account.ID, "share compact bypass keeps the original minimum-load order")
}
