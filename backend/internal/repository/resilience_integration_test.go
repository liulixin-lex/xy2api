//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Gateway resilience against real Redis and PostgreSQL (tasks 2.8, 7.7).

func TestResilienceStoreSessionStateExpires(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	store := NewResilienceStore(rdb)

	n, err := store.IncrSessionFailure(ctx, 7, "sess", 11, time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	n, err = store.IncrSessionFailure(ctx, 7, "sess", 11, time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	_, err = store.IncrSessionFailure(ctx, 7, "sess", 12, time.Minute)
	require.NoError(t, err)
	_, err = store.IncrSessionFailure(ctx, 8, "sess", 11, time.Minute)
	require.NoError(t, err)

	failures, err := store.SessionFailures(ctx, 7, "sess")
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{11: 2, 12: 1}, failures)
	require.NoError(t, store.ClearSessionFailure(ctx, 7, "sess", 11))
	failures, err = store.SessionFailures(ctx, 7, "sess")
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{12: 1}, failures)

	ttl, err := rdb.PTTL(ctx, resilienceSessionFailurePrefix+"7:sess").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 50*time.Second, "every failure refreshes the window")

	// Avoid entries end one by one; the key itself carries a TTL.
	require.NoError(t, store.AddSessionAvoid(ctx, 7, "sess", 11, 300*time.Millisecond))
	require.NoError(t, store.AddSessionAvoid(ctx, 7, "sess", 12, time.Minute))
	avoid, err := store.SessionAvoid(ctx, 7, "sess")
	require.NoError(t, err)
	require.Equal(t, map[int64]struct{}{11: {}, 12: {}}, avoid)
	time.Sleep(400 * time.Millisecond)
	avoid, err = store.SessionAvoid(ctx, 7, "sess")
	require.NoError(t, err)
	require.Equal(t, map[int64]struct{}{12: {}}, avoid)
	ttl, err = rdb.PTTL(ctx, resilienceSessionAvoidPrefix+"7:sess").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
}

func TestResilienceStoreGroupStabilityAndFallbackKeys(t *testing.T) {
	ctx := context.Background()
	store := NewResilienceStore(testRedis(t))
	now := time.Now()

	// The first healthy evaluation starts the stability clock; later ones keep it.
	first, err := store.MarkGroupStable(ctx, 9, now)
	require.NoError(t, err)
	again, err := store.MarkGroupStable(ctx, 9, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, first.UnixMilli(), again.UnixMilli())
	require.NoError(t, store.ClearGroupStable(ctx, 9))
	reset, err := store.MarkGroupStable(ctx, 9, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, now.Add(2*time.Minute).UnixMilli(), reset.UnixMilli())

	require.NoError(t, store.MarkFallbackKey(ctx, 9, 5, time.Minute))
	marked, err := store.FallbackKey(ctx, 9, 5)
	require.NoError(t, err)
	require.True(t, marked)
	require.NoError(t, store.ClearFallbackKey(ctx, 9, 5))
	marked, err = store.FallbackKey(ctx, 9, 5)
	require.NoError(t, err)
	require.False(t, marked)
}

func TestResilienceStoreCountersAndCacheRatio(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	store := NewResilienceStore(rdb)

	for want := int64(1); want <= 31; want++ {
		got, err := store.IncrUndelivered(ctx, 3, "2026101519")
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	ttl, err := rdb.TTL(ctx, resilienceUndeliveredPrefix+"3:2026101519").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Hour)

	_, ok, err := store.SessionCacheRatio(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, store.SetSessionCacheRatio(ctx, "k", 0.8125, time.Minute))
	ratio, ok, err := store.SessionCacheRatio(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.InDelta(t, 0.8125, ratio, 1e-9)
}

func TestUndeliveredRepositoryRoundTripAndRetention(t *testing.T) {
	ctx := context.Background()
	repo := NewUndeliveredRepository(integrationDB)
	tag := fmt.Sprintf("itest-undelivered-%d", time.Now().UnixNano())
	userID, keyID, groupID := int64(900001), int64(900002), int64(900003)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM gateway_undelivered_requests WHERE request_id LIKE $1 || '%'`, tag)
		require.NoError(t, err)
	})

	insert := func(stage string, billed bool, usage []byte) {
		require.NoError(t, repo.InsertUndelivered(ctx, &service.UndeliveredRequest{
			RequestID: tag + "-" + stage, ClientRequestID: "c-" + stage, UserID: &userID, APIKeyID: &keyID, GroupID: &groupID,
			Platform: service.PlatformOpenAI, Model: "gpt-6.1-sol", InboundEndpoint: "/v1/responses",
			DisconnectStage: stage, DurationMs: 125400, UpstreamUsage: usage, EstimatedUpstreamCost: 0.25, Billed: billed,
		}))
	}
	insert("pre_output", false, nil)
	insert("header_wait", true, []byte(`{"input_tokens":10,"output_tokens":2}`))

	list, page, err := repo.ListUndelivered(ctx, service.UndeliveredFilter{APIKeyID: keyID}, pagination.PaginationParams{Page: 1, PageSize: 1})
	require.NoError(t, err)
	require.Equal(t, int64(2), page.Total)
	require.Equal(t, 2, page.Pages)
	require.Len(t, list, 1)
	require.Equal(t, tag+"-header_wait", list[0].RequestID, "newest first")
	require.True(t, list[0].Billed)
	require.JSONEq(t, `{"input_tokens":10,"output_tokens":2}`, string(list[0].UpstreamUsage))
	require.Equal(t, groupID, *list[0].GroupID)

	list, _, err = repo.ListUndelivered(ctx, service.UndeliveredFilter{UserID: userID, Since: time.Now().Add(time.Hour)}, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, list)

	// Undelivered requests never write usage rows.
	var usageRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_logs WHERE request_id LIKE $1`, tag+"%").Scan(&usageRows))
	require.Zero(t, usageRows)

	_, err = integrationDB.ExecContext(ctx, `UPDATE gateway_undelivered_requests SET created_at = now() - interval '40 days' WHERE request_id = $1`, tag+"-pre_output")
	require.NoError(t, err)
	deleted, err := repo.DeleteUndeliveredBefore(ctx, time.Now().Add(-30*24*time.Hour))
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1))
	list, page, err = repo.ListUndelivered(ctx, service.UndeliveredFilter{APIKeyID: keyID}, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, tag+"-header_wait", list[0].RequestID)
}

func TestResilienceRepositoryOutputP99(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewResilienceRepository(integrationDB)
	tag := fmt.Sprintf("itest-resil-%d", time.Now().UnixNano())
	// The database is shared by the package: remove every row of this test
	// so dashboard and listing suites keep their exact counts.
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM usage_logs WHERE request_id LIKE $1 || '%'`,
			`DELETE FROM api_keys WHERE name = $1`,
			`DELETE FROM users WHERE email = $1 || '@example.com'`,
			`DELETE FROM accounts WHERE name LIKE $1 || '%'`,
		} {
			_, err := integrationDB.ExecContext(context.Background(), q, tag)
			require.NoError(t, err)
		}
	})

	healthy := mustCreateAccount(t, client, &service.Account{Name: tag + "-ok", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	user := mustCreateUser(t, client, &service.User{Email: tag + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + tag, Name: tag})
	model := tag + "-model"
	for i := 1; i <= 100; i++ {
		_, err := client.UsageLog.Create().SetUserID(user.ID).SetAPIKeyID(key.ID).SetAccountID(healthy.ID).
			SetRequestID(fmt.Sprintf("%s-%d", tag, i)).SetModel("upstream-name").SetRequestedModel(model).
			SetOutputTokens(i * 10).Save(ctx)
		require.NoError(t, err)
	}
	p99, err := repo.OutputTokenP99ByModel(ctx, time.Now().Add(-time.Hour), 50)
	require.NoError(t, err)
	require.Equal(t, 990, p99[model], "keyed by the requested model, lower case")
	p99, err = repo.OutputTokenP99ByModel(ctx, time.Now().Add(-time.Hour), 101)
	require.NoError(t, err)
	_, ok := p99[model]
	require.False(t, ok, "too few samples")
}
