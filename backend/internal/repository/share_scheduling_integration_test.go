//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	redisclient "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// shareIntegrationGroup creates a committed group, since the repository runs
// on the shared *sql.DB, and removes it with everything that refers to it.
func shareIntegrationGroup(t *testing.T, name string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO groups(name,platform,rate_multiplier,status) VALUES($1,'anthropic',1,'active') RETURNING id`,
		fmt.Sprintf("%s-%d", name, time.Now().UnixNano())).Scan(&id))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE group_id = $1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM share_scheduling_policies WHERE group_id = $1`, id)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, id)
	})
	return id
}

// shareIntegrationRedis is a client without the key-prefix hook: the store
// uses commands the hook does not rewrite. Keys are scoped by group id.
func shareIntegrationRedis(t *testing.T, groupID int64) *redisclient.Client {
	t.Helper()
	opts := *integrationRedis.Options()
	rdb := redisclient.NewClient(&opts)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, pattern := range []string{
			fmt.Sprintf("xy2:*:%d:*", groupID),
			shareSwitchKey(groupID),
			buildSessionKey(groupID, "*"),
		} {
			keys, err := integrationRedis.Keys(ctx, pattern).Result()
			if err == nil && len(keys) > 0 {
				_ = integrationRedis.Del(ctx, keys...).Err()
			}
		}
		_ = rdb.Close()
	})
	return rdb
}

func TestShareSchedulingPolicyCAS(t *testing.T) {
	ctx := context.Background()
	repo := NewShareSchedulingRepository(integrationDB)
	groupID := shareIntegrationGroup(t, "share-cas")

	missing, err := repo.GetPolicy(ctx, groupID)
	require.NoError(t, err)
	require.Nil(t, missing)

	first, err := repo.SavePolicy(ctx, groupID, 0, "shadow", []byte(`{"scheduler":"shadow"}`), 7)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Version)
	require.Equal(t, int64(7), *first.UpdatedBy)
	_, err = repo.SavePolicy(ctx, groupID, 0, "share", []byte(`{"scheduler":"share"}`), 7)
	require.ErrorIs(t, err, service.ErrShareSchedulingVersionConflict, "a second create loses")

	// Concurrent saves of the same version: exactly one wins.
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.SavePolicy(ctx, groupID, 1, "share", []byte(fmt.Sprintf(`{"scheduler":"share","n":%d}`, i)), 0)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, service.ErrShareSchedulingVersionConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
	require.Equal(t, int32(7), conflicts.Load())

	stored, err := repo.GetPolicy(ctx, groupID)
	require.NoError(t, err)
	require.Equal(t, int64(2), stored.Version)
	require.Equal(t, "share", stored.Scheduler)
	require.Nil(t, stored.UpdatedBy, "a system save records no admin")
	require.True(t, json.Valid(stored.Policy))
	require.False(t, stored.UpdatedAt.Before(stored.CreatedAt))

	all, err := repo.ListPolicies(ctx)
	require.NoError(t, err)
	found := false
	for _, row := range all {
		found = found || row.GroupID == groupID
	}
	require.True(t, found)
}

func TestShareSchedulingAccountUsage(t *testing.T) {
	ctx := context.Background()
	repo := NewShareSchedulingRepository(integrationDB)
	groupID := shareIntegrationGroup(t, "share-usage")
	otherGroup := shareIntegrationGroup(t, "share-usage-other")

	var userID, keyID int64
	suffix := time.Now().UnixNano()
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, fmt.Sprintf("share-usage-%d@example.test", suffix)).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'share') RETURNING id`, userID, fmt.Sprintf("sk-share-%d", suffix)).Scan(&keyID))
	accounts := make([]int64, 2)
	for i := range accounts {
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			`INSERT INTO accounts(name,platform,type,credentials,extra) VALUES($1,'anthropic','oauth','{}'::jsonb,'{}'::jsonb) RETURNING id`,
			fmt.Sprintf("share-usage-%d-%d", suffix, i)).Scan(&accounts[i]))
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id = $1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, fmt.Sprintf("{%d,%d}", accounts[0], accounts[1]))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id = $1`, keyID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE group_id = ANY($1)`, fmt.Sprintf("{%d,%d}", groupID, otherGroup))
	})
	insert := func(group, account int64, input, output, cacheCreate, cacheRead int, cost float64, firstToken *int, at time.Time) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,input_tokens,output_tokens,
			cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,first_token_ms,created_at) VALUES($1,$2,$3,$4,'claude-x',$5,$6,$7,$8,$9,$9,$10,$11)`,
			userID, keyID, account, group, input, output, cacheCreate, cacheRead, cost, firstToken, at)
		require.NoError(t, err)
	}
	ms := func(n int) *int { return &n }
	now := time.Now()
	insert(groupID, accounts[0], 100, 10, 5, 1000, 0.5, ms(400), now.Add(-10*time.Minute))
	insert(groupID, accounts[0], 200, 20, 0, 0, 0.25, ms(1000), now.Add(-5*time.Minute))
	insert(groupID, accounts[1], 50, 5, 0, 0, 0.1, nil, now.Add(-time.Minute))
	insert(groupID, accounts[1], 999, 999, 0, 0, 9, ms(9), now.Add(-2*time.Hour))    // outside the window
	insert(otherGroup, accounts[1], 999, 999, 0, 0, 9, ms(9), now.Add(-time.Minute)) // another group
	insertError := func(group int64, status int, errorType string, at time.Time) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,status_code,group_id,created_at)
			VALUES('upstream',$1,$2,$3,$4)`, errorType, status, group, at)
		require.NoError(t, err)
	}
	insertError(groupID, 502, "upstream_error", now.Add(-3*time.Minute))
	insertError(groupID, 200, "cyber_policy", now.Add(-3*time.Minute))    // a visible block on an open stream
	insertError(groupID, 200, "upstream_error", now.Add(-3*time.Minute))  // a recovered attempt, no error
	insertError(groupID, 503, "upstream_error", now.Add(-2*time.Hour))    // outside the window
	insertError(otherGroup, 502, "upstream_error", now.Add(-time.Minute)) // another group

	usage, err := repo.AccountUsage(ctx, groupID, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Len(t, usage, 2)
	require.Equal(t, service.ShareSchedulingUsage{Requests: 2, Tokens: 1335, StandardCost: 0.75}, usage[accounts[0]])
	require.Equal(t, service.ShareSchedulingUsage{Requests: 1, Tokens: 55, StandardCost: 0.1}, usage[accounts[1]])
	usage, err = repo.AccountUsage(ctx, groupID, now.Add(-time.Hour), now.Add(-4*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(2), usage[accounts[0]].Requests, "the window end is exclusive of later rows")
	require.NotContains(t, usage, accounts[1])

	summaries, err := repo.GroupSummaries(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, service.ShareSchedulingGroupSummary{Requests: 3, CacheReadTokens: 1000, PromptTokens: 1355}, summaries[groupID])
	require.Equal(t, int64(1), summaries[otherGroup].Requests)

	metrics, err := repo.GroupMetrics(ctx, groupID, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, int64(3), metrics.Requests)
	require.Equal(t, int64(1000), metrics.CacheReadTokens)
	require.Equal(t, int64(1355), metrics.PromptTokens)
	require.Equal(t, int64(2), metrics.TTFTSamples)
	require.Equal(t, 700, *metrics.TTFTP50Ms)
	require.Equal(t, 970, *metrics.TTFTP95Ms)
	require.Equal(t, int64(2), metrics.Errors, "client-visible errors and cyber blocks, not recovered attempts")

	empty, err := repo.GroupMetrics(ctx, groupID, now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Zero(t, empty.Requests)
	require.Nil(t, empty.TTFTP50Ms)
}

// TestShareSchedulingAccountAttempts: the health factor reads successes per
// account and every failed upstream attempt of the window, grouped.
func TestShareSchedulingAccountAttempts(t *testing.T) {
	ctx := context.Background()
	repo := NewShareSchedulingRepository(integrationDB)
	groupID := shareIntegrationGroup(t, "share-attempts")
	suffix := time.Now().UnixNano()
	var userID, keyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, fmt.Sprintf("share-attempts-%d@example.test", suffix)).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'share') RETURNING id`, userID, fmt.Sprintf("sk-attempts-%d", suffix)).Scan(&keyID))
	accounts := make([]int64, 3)
	for i := range accounts {
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			`INSERT INTO accounts(name,platform,type,credentials,extra) VALUES($1,'anthropic','oauth','{}'::jsonb,'{}'::jsonb) RETURNING id`,
			fmt.Sprintf("share-attempts-%d-%d", suffix, i)).Scan(&accounts[i]))
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE group_id = $1`, groupID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM usage_logs WHERE user_id = $1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id = ANY($1)`, fmt.Sprintf("{%d,%d,%d}", accounts[0], accounts[1], accounts[2]))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE id = $1`, keyID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	now := time.Now()
	for i, at := range []time.Time{now.Add(-time.Minute), now.Add(-10 * time.Minute), now.Add(-2 * time.Hour)} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,total_cost,actual_cost,created_at)
			VALUES($1,$2,$3,$4,'claude-x',0,0,$5)`, userID, keyID, accounts[i%2], groupID, at)
		require.NoError(t, err)
	}
	event := func(account int64, kind string, status int, category string) map[string]any {
		ev := map[string]any{"account_id": account, "kind": kind, "upstream_status_code": status, "message": "upstream failed"}
		if category != "" {
			ev["resilience_category"] = category
		}
		return ev
	}
	insertRow := func(status int, at time.Time, events ...map[string]any) {
		raw, err := json.Marshal(events)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,status_code,group_id,created_at,upstream_errors)
			VALUES('upstream','upstream_error',$1,$2,$3,$4::jsonb)`, status, groupID, at, string(raw))
		require.NoError(t, err)
	}
	// A recovered request: two failed attempts, then success elsewhere.
	insertRow(200, now.Add(-2*time.Minute), event(accounts[0], "failover", 502, "TRANSIENT"), event(accounts[1], "request_error", 0, ""))
	insertRow(502, now.Add(-3*time.Minute), event(accounts[0], "failover", 502, "TRANSIENT"), event(accounts[2], "failover", 402, "BALANCE"))
	insertRow(502, now.Add(-2*time.Hour), event(accounts[0], "failover", 502, "TRANSIENT")) // outside the window
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs(error_phase,error_type,status_code,group_id,created_at,upstream_errors)
		VALUES('upstream','upstream_error',502,$1,$2,'null'::jsonb)`, groupID, now.Add(-time.Minute))
	require.NoError(t, err)

	got, err := repo.AccountAttempts(ctx, accounts, now.Add(-30*time.Minute), now)
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{accounts[0]: 1, accounts[1]: 1}, got.Successes)
	counts := map[service.ShareSchedulingUpstreamAttempt]int64{}
	for _, f := range got.Failures {
		counts[f.ShareSchedulingUpstreamAttempt] += f.Count
	}
	require.Equal(t, map[service.ShareSchedulingUpstreamAttempt]int64{
		{AccountID: accounts[0], Kind: "failover", StatusCode: 502, Category: "TRANSIENT", Message: "upstream failed"}: 2,
		{AccountID: accounts[1], Kind: "request_error", StatusCode: 0, Message: "upstream failed"}:                     1,
		{AccountID: accounts[2], Kind: "failover", StatusCode: 402, Category: "BALANCE", Message: "upstream failed"}:   1,
	}, counts)

	only, err := repo.AccountAttempts(ctx, accounts[2:], now.Add(-30*time.Minute), now)
	require.NoError(t, err)
	require.Empty(t, only.Successes)
	require.Len(t, only.Failures, 1, "attempts of other accounts are left out")
}

func TestShareSchedulingStoreCounters(t *testing.T) {
	ctx := context.Background()
	groupID := shareIntegrationGroup(t, "share-counters")
	rdb := shareIntegrationRedis(t, groupID)
	store := NewShareSchedulingStore(rdb)

	now := time.Now()
	minute := now.Unix() / 60
	batch := func(n int64) service.ShareSchedulingStatsBatch {
		return service.ShareSchedulingStatsBatch{Counters: map[service.ShareSchedulingCounter]int64{
			{Kind: "share", GroupID: groupID, Minute: minute, Field: "requests"}:        n,
			{Kind: "share", GroupID: groupID, Minute: minute - 30, Field: "requests"}:   n,
			{Kind: "share", GroupID: groupID, Minute: minute - 200, Field: "requests"}:  n,
			{Kind: "share", GroupID: groupID, Minute: minute - 3000, Field: "requests"}: n,
			{Kind: "cache", GroupID: groupID, Minute: minute, Field: "class:cache_hit"}: n,
		}}
	}
	require.NoError(t, store.WriteStats(ctx, batch(2)))
	require.NoError(t, store.WriteStats(ctx, batch(3)))

	hour, err := store.ReadCounters(ctx, "share", groupID, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"requests": 10}, hour, "both flushes add up; older minutes stay outside")
	six, err := store.ReadCounters(ctx, "share", groupID, now.Add(-6*time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, int64(15), six["requests"], "long windows read hour buckets plus the minutes of the current hour, without double counting")
	day, err := store.ReadCounters(ctx, "share", groupID, now.Add(-72*time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, int64(15), day["requests"], "a read covers at most one day")
	cache, err := store.ReadCounters(ctx, "cache", groupID, now.Add(-time.Minute), now)
	require.NoError(t, err)
	require.Equal(t, int64(5), cache["class:cache_hit"])

	// A past window reads whole hourly buckets only.
	hours, err := store.ReadHourCounters(ctx, "share", groupID, (minute-200)/60, (minute-200)/60+1)
	require.NoError(t, err)
	require.Equal(t, int64(5), hours["requests"], "one hour holds both flushes of minute-200")
	hours, err = store.ReadHourCounters(ctx, "share", groupID, (minute-3000)/60, minute/60+1)
	require.NoError(t, err)
	require.Equal(t, int64(15), hours["requests"], "a read covers at most one day of hours")
	hours, err = store.ReadHourCounters(ctx, "share", groupID, minute/60, minute/60)
	require.NoError(t, err)
	require.Empty(t, hours)

	ttl, err := rdb.TTL(ctx, shareCounterKey("share", groupID, minute)).Result()
	require.NoError(t, err)
	assertTTLWithin(t, ttl, 2*time.Hour, shareSchedulingMinuteTTL)
	ttl, err = rdb.TTL(ctx, shareHourCounterKey("share", groupID, minute/60)).Result()
	require.NoError(t, err)
	assertTTLWithin(t, ttl, 25*time.Hour, service.ShareShareCounterRetention)
	ttl, err = rdb.TTL(ctx, shareHourCounterKey("cache", groupID, minute/60)).Result()
	require.NoError(t, err)
	assertTTLWithin(t, ttl, 193*time.Hour, service.ShareCacheCounterRetention)
}

func TestShareSchedulingStoreSwitchAnchor(t *testing.T) {
	ctx := context.Background()
	groupID := shareIntegrationGroup(t, "share-switch")
	rdb := shareIntegrationRedis(t, groupID)
	store := NewShareSchedulingStore(rdb)

	at, err := store.SwitchedAt(ctx, groupID)
	require.NoError(t, err)
	require.True(t, at.IsZero(), "no switch recorded")

	switched := time.UnixMilli(time.Now().Add(-time.Hour).UnixMilli())
	require.NoError(t, store.RecordSwitch(ctx, groupID, switched))
	at, err = store.SwitchedAt(ctx, groupID)
	require.NoError(t, err)
	require.True(t, switched.Equal(at))
	ttl, err := rdb.TTL(ctx, shareSwitchKey(groupID)).Result()
	require.NoError(t, err)
	assertTTLWithin(t, ttl, 193*time.Hour, service.ShareSwitchAnchorTTL)
}

func TestShareSchedulingStoreBroadcast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	groupID := shareIntegrationGroup(t, "share-broadcast")
	store := NewShareSchedulingStore(shareIntegrationRedis(t, groupID))

	received := make(chan int64, 4)
	store.SubscribeChanges(ctx, func(id int64) { received <- id })
	deadline := time.After(5 * time.Second)
	// The subscription is asynchronous: publish until it is live.
	for {
		require.NoError(t, store.PublishChange(context.Background(), groupID))
		select {
		case id := <-received:
			require.Equal(t, groupID, id)
			return
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("no broadcast received")
		}
	}
}

func TestShareSchedulingBindingsOnRedis(t *testing.T) {
	ctx := context.Background()
	groupID := shareIntegrationGroup(t, "share-bindings")
	rdb := shareIntegrationRedis(t, groupID)
	cache := &gatewayCache{rdb: rdb}

	_, _, err := cache.GetSessionAccountIDWithTTL(ctx, groupID, "absent")
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)

	// SET NX: concurrent placements of one new session agree on one account.
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := cache.SetSessionAccountIDIfAbsent(ctx, groupID, "race", int64(100+i), time.Hour)
			require.NoError(t, err)
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
	winner, remaining, err := cache.GetSessionAccountIDWithTTL(ctx, groupID, "race")
	require.NoError(t, err)
	require.GreaterOrEqual(t, winner, int64(100))
	assertTTLWithin(t, remaining, 59*time.Minute, time.Hour)

	// GET+PTTL tells the idle time of a binding written with the policy TTL.
	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, "idle", 5, 90*time.Second))
	id, remaining, err := cache.GetSessionAccountIDWithTTL(ctx, groupID, "idle")
	require.NoError(t, err)
	require.Equal(t, int64(5), id)
	assertTTLWithin(t, remaining, 85*time.Second, 90*time.Second)
}

func TestShareSchedulingCacheSessionSwap(t *testing.T) {
	ctx := context.Background()
	groupID := shareIntegrationGroup(t, "share-sess")
	rdb := shareIntegrationRedis(t, groupID)
	store := NewShareSchedulingStore(rdb)

	previous, err := store.SwapCacheSession(ctx, groupID, "fp", []byte("1:100:300:4000"), 6*time.Hour)
	require.NoError(t, err)
	require.Nil(t, previous)
	previous, err = store.SwapCacheSession(ctx, groupID, "fp", []byte("2:200:300:5000"), 6*time.Hour)
	require.NoError(t, err)
	require.Equal(t, []byte("1:100:300:4000"), previous)
	ttl, err := rdb.TTL(ctx, shareCacheSessionKey(groupID, "fp")).Result()
	require.NoError(t, err)
	assertTTLWithin(t, ttl, 5*time.Hour, 6*time.Hour)
}
