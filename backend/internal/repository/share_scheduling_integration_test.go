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
			fmt.Sprintf("xy2:share:dec:%d", groupID),
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
	insert := func(group, account int64, input, output, cacheCreate, cacheRead int, cost float64, at time.Time) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,group_id,model,input_tokens,output_tokens,
			cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,created_at) VALUES($1,$2,$3,$4,'claude-x',$5,$6,$7,$8,$9,$9,$10)`,
			userID, keyID, account, group, input, output, cacheCreate, cacheRead, cost, at)
		require.NoError(t, err)
	}
	now := time.Now()
	insert(groupID, accounts[0], 100, 10, 5, 1000, 0.5, now.Add(-10*time.Minute))
	insert(groupID, accounts[0], 200, 20, 0, 0, 0.25, now.Add(-5*time.Minute))
	insert(groupID, accounts[1], 50, 5, 0, 0, 0.1, now.Add(-time.Minute))
	insert(groupID, accounts[1], 999, 999, 0, 0, 9, now.Add(-2*time.Hour))   // outside the window
	insert(otherGroup, accounts[1], 999, 999, 0, 0, 9, now.Add(-time.Minute)) // another group

	usage, err := repo.AccountUsage(ctx, groupID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, usage, 2)
	require.Equal(t, service.ShareSchedulingUsage{Requests: 2, Tokens: 1335, StandardCost: 0.75}, usage[accounts[0]])
	require.Equal(t, service.ShareSchedulingUsage{Requests: 1, Tokens: 55, StandardCost: 0.1}, usage[accounts[1]])
}

func TestShareSchedulingStoreCountersAndDecisions(t *testing.T) {
	ctx := context.Background()
	groupID := shareIntegrationGroup(t, "share-counters")
	rdb := shareIntegrationRedis(t, groupID)
	store := NewShareSchedulingStore(rdb)

	now := time.Now()
	minute := now.Unix() / 60
	batch := func(n int64, decisions ...string) service.ShareSchedulingStatsBatch {
		b := service.ShareSchedulingStatsBatch{Counters: map[service.ShareSchedulingCounter]int64{
			{Kind: "share", GroupID: groupID, Minute: minute, Field: "requests"}:         n,
			{Kind: "share", GroupID: groupID, Minute: minute - 30, Field: "requests"}:    n,
			{Kind: "share", GroupID: groupID, Minute: minute - 3000, Field: "requests"}:  n,
			{Kind: "cache", GroupID: groupID, Minute: minute, Field: "acct_ttft:7:1000"}: n,
		}, Decisions: map[int64][][]byte{}}
		for _, d := range decisions {
			b.Decisions[groupID] = append(b.Decisions[groupID], []byte(d))
		}
		return b
	}
	require.NoError(t, store.WriteStats(ctx, batch(2, `{"n":1}`, `{"n":2}`)))
	require.NoError(t, store.WriteStats(ctx, batch(3, `{"n":3}`)))

	hour, err := store.ReadCounters(ctx, "share", groupID, now.Add(-time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"requests": 10}, hour, "both flushes add up; older minutes stay outside")
	day, err := store.ReadCounters(ctx, "share", groupID, now.Add(-72*time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, int64(10), day["requests"], "a read covers at most one day")
	cache, err := store.ReadCounters(ctx, "cache", groupID, now.Add(-time.Minute), now)
	require.NoError(t, err)
	require.Equal(t, int64(5), cache["acct_ttft:7:1000"])

	ttl, err := rdb.TTL(ctx, shareCounterKey("share", groupID, minute)).Result()
	require.NoError(t, err)
	assertTTLWithin(t, ttl, 25*time.Hour, 26*time.Hour)

	recent, err := store.RecentDecisions(ctx, groupID, 2)
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte(`{"n":3}`), []byte(`{"n":2}`)}, recent, "newest first")
	for i := range 600 {
		require.NoError(t, store.WriteStats(ctx, service.ShareSchedulingStatsBatch{Decisions: map[int64][][]byte{groupID: {[]byte(fmt.Sprintf(`{"i":%d}`, i))}}}))
	}
	all, err := store.RecentDecisions(ctx, groupID, 0)
	require.NoError(t, err)
	require.Len(t, all, 500, "the decision ring is capped")
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
	store := NewShareSchedulingStore(rdb)

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

	// Rebalance clears cold placements only and never continuation keys.
	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, "cold", 6, time.Minute))
	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, "openai:response:resp_1", 6, time.Minute))
	var seen []int64
	cleared, err := store.ClearBindings(ctx, groupID, func(b service.ShareSchedulingBinding) bool {
		seen = append(seen, b.AccountID)
		return b.Remaining < 2*time.Minute
	})
	require.NoError(t, err)
	require.Equal(t, 2, cleared, "idle and cold go, the warm race binding stays")
	require.ElementsMatch(t, []int64{winner, 5, 6}, seen, "protected keys are never offered")
	_, _, err = cache.GetSessionAccountIDWithTTL(ctx, groupID, "cold")
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	_, _, err = cache.GetSessionAccountIDWithTTL(ctx, groupID, "race")
	require.NoError(t, err)
	exists, err := rdb.Exists(ctx, buildSessionKey(groupID, "openai:response:resp_1")).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), exists)
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
