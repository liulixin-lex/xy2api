//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recentHealthUsageLogRepoStub struct {
	UsageLogRepository
	ids          []int64
	now          time.Time
	observations map[int64][]GroupQualityObservation
	err          error
}

func (s *recentHealthUsageLogRepoStub) GetAccountRecentHealthBatch(_ context.Context, accountIDs []int64, now time.Time) (map[int64][]GroupQualityObservation, error) {
	s.ids = append([]int64(nil), accountIDs...)
	s.now = now
	return s.observations, s.err
}

var _ AccountRecentHealthBatchReader = (*recentHealthUsageLogRepoStub)(nil)

func TestAccountUsageService_GetRecentHealthBatch(t *testing.T) {
	base := time.Now().UTC()
	repo := &recentHealthUsageLogRepoStub{observations: map[int64][]GroupQualityObservation{
		7: {{AccountID: 7, RequestID: "ok", At: base.Add(-time.Minute), Success: true}},
	}}
	got, err := (&AccountUsageService{usageLogRepo: repo}).GetRecentHealthBatch(context.Background(), []int64{7, 0, 9, 7, -1})
	require.NoError(t, err)
	require.Equal(t, []int64{7, 9}, repo.ids)
	require.WithinDuration(t, time.Now(), repo.now, time.Second)
	require.Equal(t, 1, got[7].Failure.Calls)
	require.NotContains(t, got, int64(9))

	repo = &recentHealthUsageLogRepoStub{}
	got, err = (&AccountUsageService{usageLogRepo: repo}).GetRecentHealthBatch(context.Background(), []int64{0, -3})
	require.NoError(t, err)
	require.Empty(t, got)
	require.Nil(t, repo.ids)

	got, err = (&AccountUsageService{usageLogRepo: struct{ UsageLogRepository }{}}).GetRecentHealthBatch(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Empty(t, got)

	repoErr := errors.New("db down")
	_, err = (&AccountUsageService{usageLogRepo: &recentHealthUsageLogRepoStub{err: repoErr}}).GetRecentHealthBatch(context.Background(), []int64{1})
	require.ErrorIs(t, err, repoErr)
}

func TestAccountRecentHealthFor_Table(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	obs := func(id string, at time.Time) GroupQualityObservation {
		return GroupQualityObservation{AccountID: 1, RequestID: id, At: at}
	}
	t.Run("empty data returns null for each metric", func(t *testing.T) {
		got := AccountRecentHealthFor(nil, now)
		require.Nil(t, got.Failure)
		require.Nil(t, got.FirstOutput)
		require.Nil(t, got.Cache)
	})

	t.Run("success and same-account same-request soft failure count once as one failure", func(t *testing.T) {
		at := now.Add(-30 * time.Second)
		got := AccountRecentHealthFor([]GroupQualityObservation{
			{AccountID: 4, RequestID: "same", At: at, Success: true},
			{AccountID: 4, RequestID: "same", At: at, SoftFailure: true, Kind: "stream_failed"},
			{AccountID: 4, RequestID: "same", At: at, SoftFailure: true, Kind: "stream_failed"},
		}, now)
		require.Equal(t, 1, got.Failure.Calls)
		require.Equal(t, 1, got.Failure.Failures)
		require.Equal(t, 1, got.Failure.StreamFailures)
	})

	t.Run("same request failures at distinct attempt times each count and old successes are excluded", func(t *testing.T) {
		requestID := "two-attempts"
		got := AccountRecentHealthFor([]GroupQualityObservation{
			{AccountID: 4, RequestID: requestID, At: now.Add(-20 * time.Second), SoftFailure: true, Kind: "stream_error"},
			{AccountID: 4, RequestID: requestID, At: now.Add(-19 * time.Second), SoftFailure: true, Kind: "stream_failed"},
			{AccountID: 4, RequestID: "old-success", At: now.Add(-61 * time.Minute), Success: true},
		}, now).Failure
		require.Equal(t, 2, got.Calls)
		require.Equal(t, 2, got.Failures)
		require.Equal(t, 2, got.StreamFailures)
	})

	t.Run("failure event count and age bounds are independent", func(t *testing.T) {
		observations := make([]GroupQualityObservation, 0, 212)
		for i := 0; i < 205; i++ {
			at := now.Add(-time.Duration(i+1) * time.Second)
			o := obs(fmt.Sprintf("r-%03d", i), at)
			o.Success = true
			if i < 15 {
				o.SoftFailure, o.Kind = true, "first_output_timeout"
			}
			observations = append(observations, o)
		}
		old := obs("older-than-hour", now.Add(-61*time.Minute))
		old.SoftFailure, old.Kind = true, "stream_failed"
		observations = append(observations, old)
		got := AccountRecentHealthFor(observations, now).Failure
		require.Equal(t, 200, got.Calls, "the event count cap applies after the one-hour age bound")
		require.Equal(t, 15, got.Failures)
		require.Equal(t, 15, got.FirstOutputTimeouts)
		require.Zero(t, got.StreamFailures)
		require.Equal(t, 200, got.SpanSeconds)
	})

	t.Run("age bound excludes older events when the event cap is not reached", func(t *testing.T) {
		recent := obs("recent", now.Add(-time.Minute))
		recent.Success = true
		older := obs("older", now.Add(-61*time.Minute))
		older.SoftFailure, older.Kind = true, "stream_failed"
		got := AccountRecentHealthFor([]GroupQualityObservation{recent, older}, now).Failure
		require.Equal(t, 1, got.Calls)
		require.Zero(t, got.Failures)
		require.Equal(t, 60, got.SpanSeconds)
	})

	t.Run("failure kinds and Wilson interval", func(t *testing.T) {
		observations := make([]GroupQualityObservation, 100)
		for i := range observations {
			o := obs(fmt.Sprintf("r-%d", i), now.Add(-time.Duration(i+1)*time.Second))
			o.Success = true
			if i < 4 {
				o.SoftFailure, o.Kind = true, "first_output_timeout"
			} else if i < 7 {
				o.SoftFailure, o.Kind = true, "stream_failed"
			} else if i < 10 {
				o.SoftFailure, o.Category = true, "STALL"
			} else if i == 10 {
				o.SoftFailure, o.Kind = true, "failover"
			}
			observations[i] = o
		}
		got := AccountRecentHealthFor(observations, now).Failure
		require.Equal(t, 100, got.Calls)
		require.Equal(t, 11, got.Failures)
		require.Equal(t, 4, got.FirstOutputTimeouts)
		require.Equal(t, 6, got.StreamFailures)
		require.Equal(t, 1, got.OtherFailures)
		require.InDelta(t, 0.06254, got.WilsonLow, 0.0001)
		require.InDelta(t, 0.18631, got.WilsonHigh, 0.0001)
	})

	t.Run("first output uses recent strict streaming continuations and interpolated percentiles", func(t *testing.T) {
		values := []int{1000, 2000, 3000, 4000, 10000}
		observations := make([]GroupQualityObservation, 0, len(values)+3)
		for i, value := range values {
			o := obs(fmt.Sprintf("ttft-%d", i), now.Add(-time.Duration(i+1)*time.Minute))
			o.Success, o.StrictContinuation, o.Streaming, o.FirstOutputMs = true, true, true, value
			observations = append(observations, o)
		}
		for _, o := range []GroupQualityObservation{
			{AccountID: 1, RequestID: "not-strict", At: now.Add(-time.Minute), Success: true, Streaming: true, FirstOutputMs: 99999},
			{AccountID: 1, RequestID: "non-stream", At: now.Add(-time.Minute), Success: true, StrictContinuation: true, FirstOutputMs: 99999},
			{AccountID: 1, RequestID: "zero-output", At: now.Add(-time.Minute), Success: true, StrictContinuation: true, Streaming: true, FirstOutputMs: 0},
			{AccountID: 1, RequestID: "old", At: now.Add(-121 * time.Minute), Success: true, StrictContinuation: true, Streaming: true, FirstOutputMs: 99999},
		} {
			observations = append(observations, o)
		}
		got := AccountRecentHealthFor(observations, now).FirstOutput
		require.Equal(t, 5, got.Samples)
		require.Equal(t, 3000, got.P50Ms)
		require.Equal(t, 7600, got.P90Ms)
		require.InDelta(t, 0.2, got.SlowShare, 0.0001)
		require.Equal(t, 300, got.SpanSeconds)
	})

	t.Run("sample cap is applied after the two-hour bound", func(t *testing.T) {
		observations := make([]GroupQualityObservation, 0, 52)
		for i := 0; i < 51; i++ {
			o := obs(fmt.Sprintf("sample-%02d", i), now.Add(-time.Duration(i+1)*time.Second))
			o.Success, o.StrictContinuation, o.Streaming, o.FirstOutputMs = true, true, true, i+1
			observations = append(observations, o)
		}
		old := obs("outside-two-hours", now.Add(-121*time.Minute))
		old.Success, old.StrictContinuation, old.Streaming, old.FirstOutputMs = true, true, true, 100000
		observations = append(observations, old)
		got := AccountRecentHealthFor(observations, now).FirstOutput
		require.Equal(t, AccountRecentSampleMax, got.Samples)
		require.Equal(t, 50, got.SpanSeconds)
	})

	t.Run("cache buckets use previous prompt and sum billed token buckets", func(t *testing.T) {
		reads := []int64{0, 1023, 1024, 1843, 1844}
		observations := make([]GroupQualityObservation, 0, len(reads)+1)
		for i, read := range reads {
			o := obs(fmt.Sprintf("cache-%d", i), now.Add(-time.Duration(i+1)*time.Minute))
			o.Success, o.StrictContinuation = true, true
			o.PromptTokens, o.PreviousPromptTokens, o.CacheReadTokens = 4096, 2048, read
			observations = append(observations, o)
		}
		ineligible := obs("previous-prompt-too-small", now.Add(-time.Minute))
		ineligible.Success, ineligible.StrictContinuation = true, true
		ineligible.PromptTokens, ineligible.PreviousPromptTokens, ineligible.CacheReadTokens = 4096, 2047, 2048
		observations = append(observations, ineligible)
		got := AccountRecentHealthFor(observations, now).Cache
		require.Equal(t, 5, got.Samples)
		require.Equal(t, 3, got.Kept)
		require.Equal(t, 1, got.Zero)
		require.Equal(t, 1, got.Partial)
		require.Equal(t, 2, got.Mid)
		require.Equal(t, 1, got.Full)
		require.Equal(t, int64(5734), got.ReadTokens)
		require.Equal(t, int64(20480), got.PromptTokens)
		require.Equal(t, 300, got.SpanSeconds)
	})
}
