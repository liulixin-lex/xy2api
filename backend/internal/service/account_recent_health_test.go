//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recentHealthUsageLogRepoStub only implements the recent-health capability; the embedded
// UsageLogRepository is nil, so any other call would panic.
type recentHealthUsageLogRepoStub struct {
	UsageLogRepository
	ids             []int64
	firstTokenSince time.Time
	errorsSince     time.Time
	cacheSince      time.Time
	stats           map[int64]AccountRecentHealth
	err             error
}

func (s *recentHealthUsageLogRepoStub) GetAccountRecentHealthBatch(_ context.Context, accountIDs []int64, firstTokenSince, errorsSince, cacheSince time.Time) (map[int64]AccountRecentHealth, error) {
	s.ids = append([]int64(nil), accountIDs...)
	s.firstTokenSince = firstTokenSince
	s.errorsSince = errorsSince
	s.cacheSince = cacheSince
	return s.stats, s.err
}

// The reader is matched by type assertion, so a stale stub signature would only fail at runtime.
var _ AccountRecentHealthBatchReader = (*recentHealthUsageLogRepoStub)(nil)

func TestAccountUsageService_GetRecentHealthBatch(t *testing.T) {
	t.Run("dedupes ids, uses the 3, 10 and 30 minute windows and fills missing accounts", func(t *testing.T) {
		avg := int64(1234)
		repo := &recentHealthUsageLogRepoStub{stats: map[int64]AccountRecentHealth{
			7: {FirstTokenAvgMs: &avg, FirstTokenSamples: 4, RecentErrors: 2, InputTokens: 3, CacheCreationTokens: 40, CacheReadTokens: 57, CacheSamples: 6},
		}}
		svc := &AccountUsageService{usageLogRepo: repo}

		before := time.Now()
		got, err := svc.GetRecentHealthBatch(context.Background(), []int64{7, 0, 9, 7, -1})
		after := time.Now()

		require.NoError(t, err)
		require.Equal(t, []int64{7, 9}, repo.ids)
		require.WithinRange(t, repo.firstTokenSince, before.Add(-AccountRecentFirstTokenWindow), after.Add(-AccountRecentFirstTokenWindow))
		require.WithinRange(t, repo.errorsSince, before.Add(-AccountRecentErrorsWindow), after.Add(-AccountRecentErrorsWindow))
		require.Equal(t, 30*time.Minute, AccountRecentCacheWindow)
		require.WithinRange(t, repo.cacheSince, before.Add(-AccountRecentCacheWindow), after.Add(-AccountRecentCacheWindow))
		require.False(t, repo.cacheSince.After(repo.firstTokenSince), "the 30-minute scan must cover the first-token window")
		require.Equal(t, map[int64]AccountRecentHealth{
			7: {FirstTokenAvgMs: &avg, FirstTokenSamples: 4, RecentErrors: 2, InputTokens: 3, CacheCreationTokens: 40, CacheReadTokens: 57, CacheSamples: 6},
			9: {},
		}, got)
	})

	t.Run("no valid ids skips the repository", func(t *testing.T) {
		repo := &recentHealthUsageLogRepoStub{}
		got, err := (&AccountUsageService{usageLogRepo: repo}).GetRecentHealthBatch(context.Background(), []int64{0, -3})
		require.NoError(t, err)
		require.Empty(t, got)
		require.Nil(t, repo.ids)
	})

	t.Run("repository without the capability yields no data", func(t *testing.T) {
		got, err := (&AccountUsageService{usageLogRepo: struct{ UsageLogRepository }{}}).GetRecentHealthBatch(context.Background(), []int64{1})
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("repository errors are returned", func(t *testing.T) {
		repoErr := errors.New("db down")
		_, err := (&AccountUsageService{usageLogRepo: &recentHealthUsageLogRepoStub{err: repoErr}}).GetRecentHealthBatch(context.Background(), []int64{1})
		require.ErrorIs(t, err, repoErr)
	})
}
