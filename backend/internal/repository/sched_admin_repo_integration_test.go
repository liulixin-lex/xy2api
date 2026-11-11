//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedAdminRepoEngine struct {
	service.SchedQualityEngine
	at time.Time
}

func (e *schedAdminRepoEngine) Reset(context.Context, int64) (time.Time, error) {
	return e.at, nil
}

func TestSchedAdminNarrowDatabaseClearsAndSnapshot(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	cache := NewSchedulerCache(testRedis(t))
	r := newAccountRepositoryWithSQL(client, integrationDB, cache)
	now := time.Now().UTC().Truncate(time.Microsecond)
	until := now.Add(time.Hour)
	extra := map[string]any{"keep": "unchanged", "model_rate_limits": map[string]any{"a": map[string]any{"rate_limit_reset_at": until.Format(time.RFC3339Nano)}, "b": map[string]any{"rate_limit_reset_at": until.Format(time.RFC3339Nano)}, "iq:m": map[string]any{}}}
	a := mustCreateAccount(t, client, &service.Account{Name: "sched-admin-narrow", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: extra, Status: service.StatusError, ErrorMessage: "bad", RateLimitedAt: &now, RateLimitResetAt: &until, OverloadUntil: &until})
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=false,temp_unschedulable_until=$2,temp_unschedulable_reason='transient:original' WHERE id=$1`, a.ID, until)
	require.NoError(t, err)
	checkPropagation := func() {
		t.Helper()
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type=$2`, a.ID, service.SchedulerOutboxEventAccountChanged).Scan(&n))
		require.Equal(t, 1, n)
		require.NoError(t, func() error {
			_, err := integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, a.ID)
			return err
		}())
	}
	original := extra["model_rate_limits"].(map[string]any)["a"]
	ok, err := r.SchedAdminClearModelLimit(ctx, a.ID, "a", map[string]any{"changed": true})
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = r.SchedAdminClearModelLimit(ctx, a.ID, "a", original)
	require.NoError(t, err)
	require.True(t, ok)
	checkPropagation()
	got, err := r.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra["model_rate_limits"], "a")
	require.Contains(t, got.Extra["model_rate_limits"], "b")
	require.Contains(t, got.Extra["model_rate_limits"], "iq:m")
	require.Equal(t, "unchanged", got.Extra["keep"])
	require.Equal(t, "transient:original", got.TempUnschedulableReason)
	ok, err = r.SchedAdminClearModelLimit(ctx, a.ID, "iq:m", map[string]any{})
	require.Error(t, err)
	require.False(t, ok)
	ok, err = r.SchedAdminClearError(ctx, a.ID, "newer")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = r.SchedAdminClearError(ctx, a.ID, "bad")
	require.NoError(t, err)
	require.True(t, ok)
	checkPropagation()
	got, err = r.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusActive, got.Status)
	require.Empty(t, got.ErrorMessage)
	require.False(t, got.Schedulable)
	wrong := until.Add(time.Second)
	ok, err = r.SchedAdminClear429(ctx, a.ID, &now, &wrong)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = r.SchedAdminClear429(ctx, a.ID, &now, &until)
	require.NoError(t, err)
	require.True(t, ok)
	checkPropagation()
	got, err = r.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Nil(t, got.RateLimitedAt)
	require.Nil(t, got.RateLimitResetAt)
	require.NotNil(t, got.OverloadUntil)
	require.NotNil(t, got.TempUnschedulableUntil)
	ok, err = r.SchedAdminClearOverload(ctx, a.ID, &wrong)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = r.SchedAdminClearOverload(ctx, a.ID, &until)
	require.NoError(t, err)
	require.True(t, ok)
	checkPropagation()
	cached, err := cache.GetAccount(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Nil(t, cached.OverloadUntil)
	require.False(t, cached.Schedulable)
	require.Contains(t, cached.Extra["model_rate_limits"], "iq:m")
}
func TestSchedAdminTempClearDatabaseRedisAndLegacyDetail(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	r := newAccountRepositoryWithSQL(client, integrationDB, nil)
	cache := &tempUnschedCache{rdb: testRedis(t)}
	rl := service.NewRateLimitService(r, nil, nil, nil, cache)
	s := service.NewSchedAdminService(service.SchedAdminDependencies{Accounts: r, RateLimit: rl})
	a := mustCreateAccount(t, client, &service.Account{Name: "sched-admin-temp", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	state := &service.TempUnschedState{UntilUnix: until.Unix(), TriggeredAtUnix: time.Now().Unix(), MatchedKeyword: "rule", ErrorMessage: "original", RuleIndex: 2}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	reason := string(raw)
	require.NoError(t, r.SetTempUnschedulable(ctx, a.ID, until, reason))
	require.NoError(t, cache.SetTempUnsched(ctx, a.ID, state))
	detail, err := rl.GetTempUnschedStatus(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, detail)
	ok, err := s.ClearAutoState(ctx, a.ID, "temp", 7)
	require.NoError(t, err)
	require.True(t, ok)
	detail, err = rl.GetTempUnschedStatus(ctx, a.ID)
	require.NoError(t, err)
	require.Nil(t, detail)
	cached, err := cache.GetTempUnsched(ctx, a.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
	require.NoError(t, r.SetTempUnschedulable(ctx, a.ID, until, "auth:replacement"))
	ok, err = r.SchedAdminClearTemp(ctx, a.ID, reason, &until)
	require.NoError(t, err)
	require.False(t, ok)
	got, err := r.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "auth:replacement", got.TempUnschedulableReason)
	replacement := *state
	replacement.ErrorMessage = "new source"
	require.NoError(t, cache.SetTempUnsched(ctx, a.ID, &replacement))
	ok, err = cache.SchedAdminDeleteTempIf(ctx, a.ID, reason, &until)
	require.NoError(t, err)
	require.False(t, ok)
	cached, err = cache.GetTempUnsched(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, "new source", cached.ErrorMessage)
	replacementRaw, err := json.Marshal(replacement)
	require.NoError(t, err)
	ok, err = cache.SchedAdminDeleteTempIf(ctx, a.ID, string(replacementRaw), &until)
	require.NoError(t, err)
	require.True(t, ok)
}
func TestSchedAdminHealthCounterResetBeforeThreshold(t *testing.T) {
	ctx := context.Background()
	cache := &tempUnschedCache{rdb: testRedis(t)}
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	a := mustCreateAccount(t, client, &service.Account{Name: "sched-admin-counters", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	c403, timeout := NewOpenAI403CounterCache(cache.rdb), NewTimeoutCounterCache(cache.rdb)
	for i := 0; i < 2; i++ {
		_, err := c403.IncrementOpenAI403Count(ctx, a.ID, 5)
		require.NoError(t, err)
		_, err = timeout.IncrementTimeoutCount(ctx, a.ID, 5)
		require.NoError(t, err)
	}
	n, tripped, err := cache.RecordOpenAIAPIKeyHealthFailure(ctx, a.ID, 5, 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.False(t, tripped)
	n, _, err = cache.RecordOpenAIAPIKeyHealthFailure(ctx, a.ID, 5, 3)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	rl := service.NewRateLimitService(repo, nil, nil, nil, cache)
	rl.SetOpenAI403CounterCache(c403)
	rl.SetTimeoutCounterCache(timeout)
	rl.SetOpenAIAPIKeyHealthCache(cache)
	s := service.NewSchedAdminService(service.SchedAdminDependencies{Accounts: repo, Engine: &schedAdminRepoEngine{at: time.Now().UTC()}, RateLimit: rl})
	result, err := s.Reset(ctx, a.ID, 7)
	require.NoError(t, err)
	require.Empty(t, result.Failed)
	for _, key := range []string{"403_counter", "stream_timeout_counter", "health_counter"} {
		require.Contains(t, result.Cleared, service.SchedResetItem{Key: key, Kind: key})
	}
	exists, err := cache.rdb.Exists(ctx, cache.openAIAPIKeyHealthKey(a.ID), cache.openAIAPIKeyHealthKey(a.ID)+":sequence", fmt.Sprintf("%s%d", openAI403CounterPrefix, a.ID), fmt.Sprintf("%s%d", timeoutCounterPrefix, a.ID)).Result()
	require.NoError(t, err)
	require.Zero(t, exists)
	n, tripped, err = cache.RecordOpenAIAPIKeyHealthFailure(ctx, a.ID, 5, 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.False(t, tripped)
}
