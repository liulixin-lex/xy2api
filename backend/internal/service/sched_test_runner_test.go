//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/schedtest"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type schedTestMemoryStore struct {
	mu            sync.Mutex
	records       map[SchedAccountModel]SchedTestRecord
	finished      chan SchedTestRecord
	finishErr     error
	finishCalls   int
	deleteRows    int64
	deleteErr     error
	deletedBefore time.Time
}

func (s *schedTestMemoryStore) Insert(_ context.Context, r SchedTestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := SchedAccountModel{AccountID: r.AccountID, Model: r.Model}
	if last, ok := s.records[key]; ok {
		if last.Running {
			return ErrSchedTestRunning
		}
		if r.StartedAt.Sub(last.StartedAt) < schedquality.TestRetryEvery {
			return ErrSchedTestTooSoon
		}
	}
	s.records[key] = r
	return nil
}
func (s *schedTestMemoryStore) Finish(_ context.Context, r SchedTestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	if s.finishErr == nil {
		s.records[SchedAccountModel{AccountID: r.AccountID, Model: r.Model}] = r
	}
	s.finished <- r
	return s.finishErr
}
func (s *schedTestMemoryStore) Latest(_ context.Context, id int64, model string) (*SchedTestRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[SchedAccountModel{AccountID: id, Model: model}]
	if !ok {
		return nil, nil
	}
	return &r, nil
}
func (s *schedTestMemoryStore) Since(context.Context, time.Time) ([]SchedTestRecord, error) {
	return nil, nil
}
func (s *schedTestMemoryStore) Recent(context.Context, int64, int) ([]SchedTestRecord, error) {
	return nil, nil
}
func (s *schedTestMemoryStore) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	s.deletedBefore = before
	return s.deleteRows, s.deleteErr
}
func (s *schedTestMemoryStore) ReapStale(_ context.Context, before, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var affected int64
	for key, r := range s.records {
		if r.Running && r.StartedAt.Before(before) {
			r.Running = false
			r.AbortReason = "timeout"
			r.FinishedAt = now
			s.records[key] = r
			affected++
		}
	}
	return affected, nil
}

type schedTestAccounts struct{ account *Account }

func (a schedTestAccounts) GetByID(context.Context, int64) (*Account, error) { return a.account, nil }

// Embedding leaves unrelated cache methods unavailable: the runner only uses slots.
type schedTestSlotCache struct {
	ConcurrencyCache
	mu          sync.Mutex
	active, max int
	acquires    int
}

func (c *schedTestSlotCache) AcquireAccountSlot(ctx context.Context, _ int64, _ int, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.active++
	c.acquires++
	c.max = max(c.max, c.active)
	return true, nil
}
func (c *schedTestSlotCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	return nil
}

func schedTestFixture(concurrency int) (*SchedTestRunnerService, *schedTestMemoryStore, *fakeSchedTestForwarder, *fakeSchedTestAdmission, *schedTestSlotCache) {
	store := &schedTestMemoryStore{finished: make(chan SchedTestRecord, 4), records: make(map[SchedAccountModel]SchedTestRecord)}
	forward := &fakeSchedTestForwarder{}
	admission := &fakeSchedTestAdmission{}
	cache := &schedTestSlotCache{}
	account := &Account{ID: 7, Concurrency: concurrency}
	s := NewSchedTestRunnerService(forward, admission, NewConcurrencyService(cache), schedTestAccounts{account}, store, time.Now, nil, nil, nil)
	s.count = func(b []byte) int { return len(b) / 4 }
	return s, store, forward, admission, cache
}

func schedTestTier(body []byte) schedquality.TestTier {
	var v struct {
		Key string `json:"prompt_cache_key"`
	}
	_ = json.Unmarshal(body, &v)
	for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
		if len(v.Key) >= len(tier.String()) && v.Key[len(v.Key)-len(tier.String()):] == tier.String() {
			return tier
		}
	}
	return -1
}

func schedTestOK(ctx context.Context, turn int) *OpenAIForwardResult {
	prompt, cache, ttfo := turn*1000, (turn-1)*1000, 12
	resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(schedquality.AttemptSample{
		Class: schedquality.ClassRegular, Outcome: schedquality.OutcomeOK,
		TTFOMs: &ttfo, PromptTokens: &prompt, CacheReadTokens: &cache,
	})
	return &OpenAIForwardResult{Usage: OpenAIUsage{InputTokens: prompt, CacheReadInputTokens: cache, OutputTokens: 10}}
}

func TestSchedTestRunnerOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		count int
	}{
		{"normal", 9}, {"timeout", 8}, {"retry", 7}, {"one_busy", 6}, {"all_busy", 0}, {"internal_error", 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, f, admission, cache := schedTestFixture(10)
				defer s.Stop()
				admission.Fn = func(context.Context, *Account, string) (string, bool, string) { return "request-model", true, "" }
				var mu sync.Mutex
				turns := map[schedquality.TestTier]int{}
				acquire := s.acquire
				s.acquire = func(ctx context.Context, account *Account, tier schedquality.TestTier) (*AcquireResult, error) {
					if scenario.name == "all_busy" || scenario.name == "one_busy" && tier == schedquality.TierLarge {
						return &AcquireResult{}, nil
					}
					return acquire(ctx, account, tier)
				}
				f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, body []byte) (*OpenAIForwardResult, error) {
					var request struct{ Model string }
					if json.Unmarshal(body, &request) != nil || request.Model != "request-model" {
						t.Error("admitted request model was not used")
					}
					if scenario.name == "internal_error" {
						return nil, errors.New("credentials unavailable")
					}
					tier := schedTestTier(body)
					mu.Lock()
					turns[tier]++
					n := turns[tier]
					mu.Unlock()
					attempt := resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt)
					if attempt.Warm != (n > 1) || attempt.PrevPromptTokens != (n-1)*1000 {
						t.Errorf("tier %v turn %d: warm=%v prev=%d", tier, n, attempt.Warm, attempt.PrevPromptTokens)
					}
					if scenario.name == "timeout" && tier == schedquality.TierMedium && n == 2 {
						attempt.Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeTimeout})
						return nil, nil
					}
					if scenario.name == "retry" && tier == schedquality.TierMedium && n == 1 {
						attempt.Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeAccountFault, Detail: "first_fault"})
					}
					return schedTestOK(ctx, n), nil
				}
				ticket, err := s.Request(context.Background(), 7, "evidence", false, 37)
				require.NoError(t, err)
				require.True(t, ticket.Started)
				r := <-store.finished
				require.Len(t, r.Turns, scenario.count)
				require.False(t, r.Running)
				require.Equal(t, scenario.count > 0, r.Done)
				require.Equal(t, ticket.TestID, r.ID)
				require.Equal(t, "evidence", r.Model)
				require.False(t, r.Auto)
				require.EqualValues(t, 37, r.OperatorID)
				if scenario.name == "all_busy" {
					require.Equal(t, "busy", r.AbortReason)
					require.Equal(t, schedquality.TestSlotWait, r.FinishedAt.Sub(r.StartedAt))
					require.Empty(t, turns)
				} else if scenario.name == "internal_error" {
					require.Equal(t, "error", r.AbortReason)
				} else {
					require.Empty(t, r.AbortReason)
				}
				expectedTurns := [3]int{3, 3, 3}
				switch scenario.name {
				case "timeout":
					expectedTurns[1] = 2
				case "retry":
					expectedTurns[1] = 1
				case "one_busy":
					expectedTurns[2] = 0
				case "all_busy", "internal_error":
					expectedTurns = [3]int{}
				}
				for tier, n := range expectedTurns {
					require.Equal(t, n, turns[schedquality.TestTier(tier)])
				}
				var input, cached, output int64
				for _, row := range r.Turns {
					require.Equal(t, row.Turn > 1, row.Warm)
					if row.Turn == 1 {
						require.Nil(t, row.PrevPromptTokens)
					} else {
						require.Equal(t, (row.Turn-1)*1000, *row.PrevPromptTokens)
					}
					if scenario.name == "retry" && row.Tier == schedquality.TierMedium {
						require.Equal(t, schedquality.OutcomeAccountFault, row.Outcome)
						require.Equal(t, "first_fault", row.Detail)
						input += 1000
						output += 10
					} else if scenario.name == "timeout" && row.Tier == schedquality.TierMedium && row.Turn == 2 {
						require.Equal(t, schedquality.OutcomeTimeout, row.Outcome)
					} else {
						require.Equal(t, schedquality.OutcomeOK, row.Outcome)
						require.Equal(t, schedquality.ClassRegular, row.Class)
						require.Equal(t, 12, *row.TTFOMs)
						require.Equal(t, row.Turn*1000, *row.PromptTokens)
						require.Equal(t, (row.Turn-1)*1000, *row.CacheReadTokens)
						input += int64(row.Turn * 1000)
						cached += int64((row.Turn - 1) * 1000)
						output += 10
					}
				}
				require.Equal(t, input, r.InputTokens)
				require.Equal(t, cached, r.CachedTokens)
				require.Equal(t, output, r.OutputTokens)
				require.Zero(t, cache.active)
				if scenario.name == "internal_error" {
					require.Equal(t, 3, cache.acquires)
				} else {
					require.Equal(t, scenario.count, cache.acquires)
				}
			})
		})
	}
}

func TestSchedTestRunnerHardAdmission(t *testing.T) {
	for _, missingModel := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_model=%v", missingModel), func(t *testing.T) {
			s, store, f, a, cache := schedTestFixture(10)
			defer s.Stop()
			f.Fn = func(context.Context, *gin.Context, *Account, []byte) (*OpenAIForwardResult, error) {
				t.Error("request sent after rejection")
				return nil, nil
			}
			a.Fn = func(context.Context, *Account, string) (string, bool, string) { return "", missingModel, "blocked" }
			ticket, err := s.Request(context.Background(), 7, "evidence", false, 2)
			require.NoError(t, err)
			require.False(t, ticket.Started)
			require.Equal(t, "not_schedulable", ticket.Why)
			r, err := store.Latest(context.Background(), 7, "evidence")
			require.NoError(t, err)
			require.Equal(t, "not_schedulable", r.AbortReason)
			require.False(t, r.Running)
			require.False(t, r.Done)
			require.Empty(t, r.Turns)
			require.Zero(t, cache.acquires)
		})
	}
	t.Run("changes_while_running", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, store, f, a, cache := schedTestFixture(10)
			defer s.Stop()
			var mu sync.Mutex
			completed := 0
			a.Fn = func(context.Context, *Account, string) (string, bool, string) {
				mu.Lock()
				defer mu.Unlock()
				return "mapped", completed == 0, "blocked"
			}
			f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
				mu.Lock()
				completed++
				mu.Unlock()
				return schedTestOK(ctx, 1), nil
			}
			_, err := s.Request(context.Background(), 7, "evidence", false, 0)
			require.NoError(t, err)
			r := <-store.finished
			require.True(t, r.Done)
			require.NotEmpty(t, r.Turns)
			require.LessOrEqual(t, len(r.Turns), 3)
			require.Equal(t, completed, len(r.Turns))
			for _, row := range r.Turns {
				require.Equal(t, 1, row.Turn)
			}
			require.Zero(t, cache.active)
		})
	})
}

func TestSchedTestRunnerTierConcurrency(t *testing.T) {
	for _, tc := range []struct{ limit, parallel int }{{1, 1}, {2, 1}, {3, 2}, {10, 3}} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, f, _, cache := schedTestFixture(tc.limit)
				defer s.Stop()
				entered := make(chan struct{}, 9)
				release := make(chan struct{})
				f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
					entered <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
					}
					return schedTestOK(ctx, 1), nil
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				synctest.Wait()
				require.Len(t, entered, tc.parallel)
				require.Equal(t, tc.parallel, cache.active)
				require.Equal(t, tc.parallel, cache.max)
				close(release)
				r := <-store.finished
				require.Len(t, r.Turns, 9)
				require.LessOrEqual(t, cache.max, tc.parallel)
				require.Zero(t, cache.active)
				require.Equal(t, 9, cache.acquires)
			})
		})
	}
}

func TestSchedTestRunnerConcurrentRequestsAndRetryWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, f, _, _ := schedTestFixture(10)
		defer s.Stop()
		// Keep the first test running while both requests race through Latest/Insert.
		release := make(chan struct{})
		f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
			<-release
			return schedTestOK(ctx, 1), nil
		}
		start := make(chan struct{})
		tickets := make(chan SchedTestTicket, 2)
		for range 2 {
			go func() {
				<-start
				ticket, err := s.Request(context.Background(), 7, "evidence", false, 0)
				if err != nil {
					t.Error(err)
				}
				tickets <- ticket
			}()
		}
		close(start)
		a, b := <-tickets, <-tickets
		require.NotEqual(t, a.Started, b.Started)
		if a.Started {
			require.Equal(t, "already_running", b.Why)
		} else {
			require.Equal(t, "already_running", a.Why)
		}
		close(release)
		<-store.finished
		// The normal three rounds finish at t=10s; retry uses StartedAt, not FinishedAt.
		<-time.After(49 * time.Second)
		ticket, err := s.Request(context.Background(), 7, "evidence", false, 0)
		require.NoError(t, err)
		require.Equal(t, "too_soon", ticket.Why)
		<-time.After(time.Second)
		ticket, err = s.Request(context.Background(), 7, "evidence", false, 0)
		require.NoError(t, err)
		require.True(t, ticket.Started)
		<-store.finished
	})
}

func TestSchedTestRunnerTurnDeadline(t *testing.T) {
	for _, firstByte := range []bool{false, true} {
		t.Run(fmt.Sprint(firstByte), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, f, _, cache := schedTestFixture(10)
				defer s.Stop()
				f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
					sample := schedquality.AttemptSample{Outcome: schedquality.OutcomeOther}
					if firstByte {
						n := 12
						sample.Outcome = schedquality.OutcomeOK
						sample.TTFOMs = &n
					}
					<-ctx.Done()
					resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(sample)
					return nil, ctx.Err()
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				r := <-store.finished
				require.True(t, r.Done)
				require.Len(t, r.Turns, 3)
				require.Equal(t, schedquality.TestTurnTimeout, r.FinishedAt.Sub(r.StartedAt))
				want := schedquality.OutcomeTimeout
				if firstByte {
					want = schedquality.OutcomeOther
				}
				for _, row := range r.Turns {
					require.Equal(t, want, row.Outcome)
					if firstByte {
						require.NotNil(t, row.TTFOMs)
					} else {
						require.Nil(t, row.TTFOMs)
					}
				}
				require.Zero(t, cache.active)
			})
		})
	}
}

func TestSchedTestRunnerTotalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, f, _, cache := schedTestFixture(1)
		defer s.Stop()
		f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
			select {
			case <-time.After(100 * time.Second):
				return schedTestOK(ctx, 1), nil
			case <-ctx.Done():
				first := 12
				resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeOK, TTFOMs: &first})
				return nil, ctx.Err()
			}
		}
		_, err := s.Request(context.Background(), 7, "evidence", false, 0)
		require.NoError(t, err)
		r := <-store.finished
		require.True(t, r.Done)
		require.Len(t, r.Turns, 4)
		require.Equal(t, schedquality.TestTimeout, r.FinishedAt.Sub(r.StartedAt))
		ok := 0
		for _, row := range r.Turns {
			if row.Outcome == schedquality.OutcomeOK {
				ok++
			} else {
				require.Equal(t, schedquality.OutcomeOther, row.Outcome)
				require.Equal(t, "cancelled", row.Detail)
				require.NotNil(t, row.TTFOMs)
			}
		}
		require.Equal(t, 3, ok)
		require.EqualValues(t, 3000, r.InputTokens)
		require.EqualValues(t, 30, r.OutputTokens)
		require.Zero(t, cache.active)
	})
}

func TestSchedTestRunnerStopCancelsInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, f, _, cache := schedTestFixture(10)
		f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
			<-ctx.Done()
			first := 12
			resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeOK, TTFOMs: &first})
			return nil, ctx.Err()
		}
		_, err := s.Request(context.Background(), 7, "evidence", false, 0)
		require.NoError(t, err)
		synctest.Wait()
		require.Equal(t, 3, cache.active)
		s.Stop()
		r := <-store.finished
		require.True(t, r.Done)
		require.Len(t, r.Turns, 3)
		for _, row := range r.Turns {
			require.Equal(t, schedquality.OutcomeOther, row.Outcome)
			require.Equal(t, "cancelled", row.Detail)
		}
		require.Zero(t, cache.active)
		_, err = s.Request(context.Background(), 7, "evidence", false, 0)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestSchedTestFinishFailureAndReapStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, f, _, _ := schedTestFixture(10)
		defer s.Stop()
		store.finishErr = errors.New("write failed")
		f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
			return schedTestOK(ctx, 1), nil
		}
		_, err := s.Request(context.Background(), 7, "evidence", false, 0)
		require.NoError(t, err)
		<-store.finished
		s.wg.Wait()
		require.Equal(t, 1, store.finishCalls)
		r, err := store.Latest(context.Background(), 7, "evidence")
		require.NoError(t, err)
		require.True(t, r.Running)
		// Seven minutes is the stale cutoff; the repository comparison is strict.
		<-time.After(schedquality.TestStaleAfter - time.Since(r.StartedAt))
		rows, err := s.ReapStale(context.Background())
		require.NoError(t, err)
		require.Zero(t, rows)
		r, _ = store.Latest(context.Background(), 7, "evidence")
		require.True(t, r.Running)
		<-time.After(time.Nanosecond)
		rows, err = s.ReapStale(context.Background())
		require.NoError(t, err)
		require.EqualValues(t, 1, rows)
		r, _ = store.Latest(context.Background(), 7, "evidence")
		require.False(t, r.Running)
		require.False(t, r.Done)
		require.Equal(t, "timeout", r.AbortReason)
		require.Equal(t, time.Now(), r.FinishedAt)
	})
}

func TestSchedTestRunnerEffortCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name        string
		levels      []string
		noReasoning bool
		want        string
	}{
		{"unsupported_xhigh", []string{"low", "high"}, false, "high"},
		{"highest_not_last", []string{"max", "low", "high"}, false, "max"},
		{"ultra", []string{"ultra", "high"}, false, "ultra"},
		{"none_only", []string{"none"}, false, "none"},
		{"reasoning_disabled", nil, true, "none"},
		{"supported", []string{"max", "xhigh"}, false, "xhigh"},
		{"unknown", nil, false, "xhigh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, f, _, _ := schedTestFixture(10)
				defer s.Stop()
				account, _ := s.accounts.GetByID(context.Background(), 7)
				reasoning := !tc.noReasoning
				account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
					"evidence": {SupportedReasoningLevels: tc.levels, Reasoning: &reasoning},
				}})
				f.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, body []byte) (*OpenAIForwardResult, error) {
					var req struct {
						Reasoning struct {
							Effort string `json:"effort"`
						} `json:"reasoning"`
					}
					if err := json.Unmarshal(body, &req); err != nil {
						t.Error(err)
					}
					if schedTestTier(body) == schedquality.TierLarge && req.Reasoning.Effort != tc.want {
						t.Errorf("effort=%q want=%q", req.Reasoning.Effort, tc.want)
					}
					return schedTestOK(ctx, 1), nil
				}
				_, err := s.Request(context.Background(), 7, "evidence", false, 0)
				require.NoError(t, err)
				<-store.finished
			})
		})
	}
	require.Equal(t, "xhigh", schedTestEffort(&Account{}, "unknown-model", schedquality.TierLarge))
	require.Equal(t, "xhigh", schedTestEffort(&Account{}, "gpt-custom", schedquality.TierLarge))
}

func TestSchedTestBuildWithGatewayTokenEstimator(t *testing.T) {
	for tier := schedquality.TierSmall; tier <= schedquality.TierLarge; tier++ {
		body := schedtest.Build(tier, 1, schedtest.Session{ID: "estimator", Model: "gpt-6.1-sol"}, schedTestCount)
		n := schedTestCount(body)
		target := schedquality.TestTiers[tier].Tokens
		require.InDelta(t, target, n, float64(target)*schedquality.TestPromptTolerance, "tier %s", tier)
	}
}

type schedTestObsRecorder struct{ counts map[string]int64 }

func (o *schedTestObsRecorder) Count(name string, n int64) { o.counts[name] += n }
func (*schedTestObsRecorder) Cycle(time.Duration, bool)    {}

func schedTestCaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func schedTestLogEvent(t *testing.T, logs *bytes.Buffer, message string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var event map[string]any
		require.NoError(t, json.Unmarshal(line, &event))
		if event["msg"] == message {
			found = append(found, event)
		}
	}
	require.Len(t, found, 1)
	return found[0]
}

func TestSchedTestRunnerTiersAndEvents(t *testing.T) {
	for _, auto := range []bool{true, false} {
		t.Run(fmt.Sprintf("auto=%v", auto), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := schedTestCaptureLogs(t)
				s, store, forward, _, cache := schedTestFixture(10)
				defer s.Stop()
				obs := &schedTestObsRecorder{counts: make(map[string]int64)}
				s.obs = obs
				var mu sync.Mutex
				turns := map[schedquality.TestTier]int{}
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, body []byte) (*OpenAIForwardResult, error) {
					mu.Lock()
					defer mu.Unlock()
					tier := schedTestTier(body)
					turns[tier]++
					return schedTestOK(ctx, turns[tier]), nil
				}
				ticket, err := s.Request(context.Background(), 7, "evidence", auto, 0)
				require.NoError(t, err)
				record := <-store.finished
				s.wg.Wait()
				want := map[schedquality.TestTier]int{schedquality.TierSmall: 3}
				if !auto {
					want[schedquality.TierMedium], want[schedquality.TierLarge] = 3, 3
				}
				require.Equal(t, want, turns)
				require.Equal(t, len(want)*3, cache.acquires)
				require.EqualValues(t, len(want)*6000, record.InputTokens)
				require.EqualValues(t, len(want)*3000, record.CachedTokens)
				event := schedTestLogEvent(t, logs, "sched_quality.test")
				require.Equal(t, map[string]any{
					"component": "sched_quality", "test_id": ticket.TestID, "account_id": float64(7), "model": "evidence", "auto": auto,
					"result": "clean", "input_tokens": float64(record.InputTokens), "cached_tokens": float64(record.CachedTokens), "duration_ms": float64((2 * schedquality.TestTurnGap).Milliseconds()),
					"time": event["time"], "level": "INFO", "msg": "sched_quality.test",
				}, event)
				require.Equal(t, map[string]int64{"tests": 1}, obs.counts)
				_, err = s.Request(context.Background(), 7, "evidence", auto, 0)
				require.NoError(t, err)
				require.Equal(t, map[string]int64{"tests": 1}, obs.counts, "rejected retries do not count as tests")
			})
		})
	}
}

func TestSchedTestRunnerAbortAndDirtyEvents(t *testing.T) {
	for _, scenario := range []struct{ name, result, abort, blocked string }{
		{"initial_admission", "aborted", "not_schedulable", "rate_limited"},
		{"round_admission", "aborted", "not_schedulable", "rate_limited"},
		{"slot_admission", "aborted", "not_schedulable", "rate_limited"},
		{"busy", "aborted", "busy", ""},
		{"internal_error", "aborted", "error", ""},
		{"panic", "aborted", "error", ""},
		{"fault", "dirty", "", ""},
		{"partial_admission", "dirty", "", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := schedTestCaptureLogs(t)
				s, store, forward, admission, _ := schedTestFixture(1)
				defer s.Stop()
				obs := &schedTestObsRecorder{counts: make(map[string]int64)}
				s.obs = obs
				now := s.now()
				future := now.Add(time.Hour)
				account := &Account{ID: 7, Status: StatusActive, Schedulable: true, RateLimitResetAt: &future}
				s.accounts = schedTestAccounts{account}
				calls, sent := 0, 0
				admission.Fn = func(context.Context, *Account, string) (string, bool, string) {
					calls++
					blocked := scenario.name == "initial_admission" || scenario.name == "round_admission" && calls > 1 || scenario.name == "slot_admission" && calls > 2 || scenario.name == "partial_admission" && sent > 0
					return "mapped", !blocked, "not_schedulable"
				}
				if scenario.name == "busy" {
					s.acquire = func(context.Context, *Account, schedquality.TestTier) (*AcquireResult, error) {
						return &AcquireResult{}, nil
					}
				}
				forward.Fn = func(ctx context.Context, _ *gin.Context, _ *Account, _ []byte) (*OpenAIForwardResult, error) {
					if scenario.name == "panic" {
						panic("private panic text")
					}
					if scenario.name == "internal_error" {
						return nil, errors.New("private error text")
					}
					sent++
					if scenario.name == "fault" {
						resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt).Sink(schedquality.AttemptSample{Outcome: schedquality.OutcomeAccountFault})
						return nil, nil
					}
					return schedTestOK(ctx, sent), nil
				}
				ticket, err := s.Request(context.Background(), 7, "evidence", true, 0)
				require.NoError(t, err)
				if ticket.Started {
					<-store.finished
				}
				s.wg.Wait()
				event := schedTestLogEvent(t, logs, "sched_quality.test")
				require.Equal(t, scenario.result, event["result"])
				if scenario.abort == "" {
					require.NotContains(t, event, "abort_reason")
				} else {
					require.Equal(t, scenario.abort, event["abort_reason"])
				}
				if scenario.blocked == "" {
					require.NotContains(t, event, "blocked_by")
				} else {
					require.Equal(t, scenario.blocked, event["blocked_by"])
				}
				require.EqualValues(t, 1, obs.counts["tests"])
				if scenario.result == "aborted" {
					require.EqualValues(t, 1, obs.counts["tests_aborted"])
				} else {
					require.Zero(t, obs.counts["tests_aborted"])
				}
				require.NotContains(t, fmt.Sprint(event), "private")
			})
		})
	}
	t.Run("timeout", func(t *testing.T) {
		logs := schedTestCaptureLogs(t)
		s, _, _, _, _ := schedTestFixture(1)
		defer s.Stop()
		now := time.Now()
		s.logTest(context.Background(), SchedTestRecord{AbortReason: "timeout", TestResult: schedquality.TestResult{StartedAt: now, FinishedAt: now.Add(schedquality.TestStaleAfter)}}, "rate_limited")
		event := schedTestLogEvent(t, logs, "sched_quality.test")
		require.Equal(t, "aborted", event["result"])
		require.Equal(t, "timeout", event["abort_reason"])
		require.NotContains(t, event, "blocked_by")
	})
}

func TestSchedTestCleanupEvents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     int64
		err      error
		category string
	}{
		{"deleted", 4, nil, ""}, {"empty", 0, nil, ""},
		{"database", 0, errors.New("private database details"), "db"},
		{"timeout", 0, fmt.Errorf("private wrapped: %w", context.DeadlineExceeded), "timeout"},
		{"query_canceled", 0, fmt.Errorf("private wrapped: %w", &pq.Error{Code: "57014", Message: "private database details"}), "timeout"},
		{"internal", 0, context.Canceled, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := schedTestCaptureLogs(t)
			s, store, _, _, _ := schedTestFixture(1)
			defer s.Stop()
			store.deleteRows, store.deleteErr = tc.rows, tc.err
			start := time.Now()
			calls := 0
			s.now = func() time.Time { calls++; return start.Add(time.Duration(calls-1) * 25 * time.Millisecond) }
			s.cleanup(context.Background())
			require.Equal(t, start.Add(-schedquality.TestRetention), store.deletedBefore)
			event := schedTestLogEvent(t, logs, "sched_quality.cleanup")
			want := map[string]any{"component": "sched_quality", "kind": "tests", "rows": float64(tc.rows), "duration_ms": float64(25), "time": event["time"], "level": "INFO", "msg": "sched_quality.cleanup"}
			if tc.err != nil {
				want["level"], want["error"] = "ERROR", tc.category
			}
			require.Equal(t, want, event)
			require.NotContains(t, logs.String(), "private")
		})
	}
}

type schedTestLeaderLock struct{ acquired bool }

func (l schedTestLeaderLock) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	return l.acquired, nil
}
func (schedTestLeaderLock) ReleaseLeaderLock(context.Context, string, string) error { return nil }

func TestSchedTestCleanupOnlyWhenExecuted(t *testing.T) {
	for _, acquired := range []bool{false, true} {
		t.Run(fmt.Sprint(acquired), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := schedTestCaptureLogs(t)
				s, store, _, _, _ := schedTestFixture(1)
				defer s.Stop()
				s.lockCache = schedTestLeaderLock{acquired: acquired}
				store.deleteRows = 2
				s.Start(context.Background())
				synctest.Wait()
				<-time.After(schedquality.CleanupInterval)
				synctest.Wait()
				s.Stop()
				if acquired {
					event := schedTestLogEvent(t, logs, "sched_quality.cleanup")
					require.Equal(t, float64(2), event["rows"])
					require.False(t, store.deletedBefore.IsZero())
				} else {
					require.Empty(t, logs.String())
					require.True(t, store.deletedBefore.IsZero())
				}
			})
		})
	}
}
