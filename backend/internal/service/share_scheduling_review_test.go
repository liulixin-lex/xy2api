//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func TestShareReviewDefaultWeightsMatchBothAdapters(t *testing.T) {
	for _, mode := range []string{sharesched.DefaultWeightConcurrency, sharesched.DefaultWeightEqual} {
		for _, load := range []int{-2, 0, 1, 50} {
			t.Run(mode+"/"+time.Duration(load).String(), func(t *testing.T) {
				p := shareTestPolicy(sharesched.SchedulerShare)
				p.Defaults.Weight = mode
				env := newShareTestEnv(t, p, Account{ID: 1, Concurrency: 10, LoadFactor: &load}, Account{ID: 2, Concurrency: 10})
				gid := shareTestGroupID
				_, inputs, err := env.gateway.shareGatewayCandidates(context.Background(), nil, &gid, "", nil, 0)
				require.NoError(t, err)
				views, _ := shareMemberViews(&p, env.repo.accounts, time.Now())
				d := sharesched.Plan(&p, sharesched.Request{GroupID: gid, Key: "weight"}, inputs.cands)
				for i := range views {
					require.Equal(t, views[i].Weight, d.Entries[i].Weight)
				}
				openai := newOpenAIShareTestEnv(t, p, 1, 2)
				openai.account(1).Concurrency, openai.account(2).Concurrency = 10, 10
				openai.account(1).LoadFactor = &load
				cands, _, _, err := openai.svc.shareOpenAICandidates(context.Background(), openAIShareSelection{groupID: &gid, platform: PlatformOpenAI}, 0)
				require.NoError(t, err)
				d = sharesched.Plan(&p, sharesched.Request{GroupID: gid, Key: "weight"}, cands)
				for _, entry := range d.Entries {
					require.Equal(t, views[0].Weight, entry.Weight)
				}
			})
		}
	}
}

type shareReviewWaitCache struct {
	*shareTestConcurrency
	commit bool
	cancel context.CancelFunc
}

func (c *shareReviewWaitCache) RegisterAccountWait(ctx context.Context, id int64, attempt string, max int, ttl time.Duration) (bool, error) {
	if c.commit {
		_, _ = c.shareTestConcurrency.RegisterAccountWait(ctx, id, attempt, max, ttl)
	}
	if c.cancel != nil {
		c.cancel()
	}
	return false, errors.New("registration acknowledgement lost")
}
func TestShareReviewWaitErrorsNeverReleaseAnotherAttempt(t *testing.T) {
	for _, commit := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			cache := &shareReviewWaitCache{shareTestConcurrency: newShareTestConcurrency(), commit: commit}
			cache.waiting[1] = 1
			ctx, cancel := context.WithCancel(context.Background())
			if cancelled {
				cache.cancel = cancel
			}
			exec := &shareExecutor{ctx: ctx, concurrency: NewConcurrencyService(cache)}
			res, waited := exec.waitForSlot(&Account{ID: 1}, 1, time.Millisecond, 3)
			cancel()
			require.Nil(t, res)
			require.Zero(t, waited)
			require.Equal(t, 1, cache.waiting[1])
		}
	}
}

type shareReviewSlowSlotCache struct {
	*shareTestConcurrency
	deadline time.Time
}

func (c *shareReviewSlowSlotCache) AcquireAccountSlot(ctx context.Context, _ int64, _ int, _ string) (bool, error) {
	c.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return false, ctx.Err()
}

func TestShareReviewWaitBudgetAlsoBoundsRedisSlotCall(t *testing.T) {
	cache := &shareReviewSlowSlotCache{shareTestConcurrency: newShareTestConcurrency()}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exec := &shareExecutor{ctx: parent, concurrency: NewConcurrencyService(cache)}
	start := time.Now()
	res, _ := exec.waitForSlot(&Account{ID: 1}, 1, 40*time.Millisecond, 3)
	require.Nil(t, res)
	require.False(t, cache.deadline.IsZero())
	require.WithinDuration(t, start.Add(40*time.Millisecond), cache.deadline, 10*time.Millisecond)
	require.Less(t, time.Since(start), 300*time.Millisecond, "slot IO cannot consume the request's longer deadline")
	require.Zero(t, cache.waiting[1])
}

func TestShareReviewBindingRaceReconcilesOrReportsSpill(t *testing.T) {
	for _, busy := range []bool{false, true} {
		cache := newShareTestCache()
		cache.bind("race", 1, time.Hour)
		conc := newShareTestConcurrency()
		if busy {
			conc.busy[1] = -1
		}
		p := twoAccountPolicy()
		var released bool
		exec := &shareExecutor{ctx: context.Background(), gp: &shareGroupPolicy{groupID: 7, policy: p}, cache: cache,
			concurrency: NewConcurrencyService(conc), key: "race", ttl: time.Hour,
			plan:     sharesched.Plan(&p, sharesched.Request{Key: "race"}, []sharesched.Candidate{{AccountID: 1, Eligible: true}, {AccountID: 2, Eligible: true}}),
			accounts: map[int64]*Account{1: {ID: 1}, 2: {ID: 2}}, limit: func(*Account) int { return 1 }, admit: func(*Account) bool { return true },
			newResult: func(a *Account, acquired bool, release func(), wait *AccountWaitPlan) (*AccountSelectionResult, error) {
				return &AccountSelectionResult{Account: a, Acquired: acquired, ReleaseFunc: release, WaitPlan: wait}, nil
			},
		}
		out := exec.finish(exec.accounts[2], true, func() { released = true }, nil, sharesched.ReasonNewSession, sharesched.WriteIfAbsent)
		require.NoError(t, out.err)
		owner, err := cache.GetSessionAccountID(context.Background(), 7, "race")
		require.NoError(t, err)
		require.Equal(t, int64(1), owner)
		if busy {
			require.Equal(t, sharesched.ReasonBindingConflictSpill, out.reason)
			require.False(t, released)
		} else {
			require.Equal(t, int64(1), out.served)
			require.True(t, released)
		}
		if out.result.ReleaseFunc != nil {
			out.result.ReleaseFunc()
		}
	}
}

type shareReviewFailingStore struct {
	shareAdminStore
	fail bool
	ids  []string
}

func (s *shareReviewFailingStore) WriteStats(ctx context.Context, b ShareSchedulingStatsBatch) error {
	s.ids = append(s.ids, b.ID)
	if s.fail {
		return errors.New("redis unavailable")
	}
	return s.shareAdminStore.WriteStats(ctx, b)
}
func TestShareReviewStatsFailureRetriesAndInvalidatesWindow(t *testing.T) {
	store := &shareReviewFailingStore{fail: true}
	s := NewShareSchedulingService(nil, store, nil)
	now := time.Now()
	s.stats.add(shareStatsKindShare, 7, now, shareStatsRequestsField, 3)
	s.flushStats(context.Background())
	require.NotNil(t, s.pendingStats)
	require.True(t, s.incompleteWindow(7, now.Add(-time.Minute), now.Add(time.Minute)))
	s.stats.add(shareStatsKindShare, 7, now, shareStatsRequestsField, 2)
	store.fail = false
	s.flushStats(context.Background())
	require.Nil(t, s.pendingStats)
	require.Equal(t, store.ids[0], store.ids[1])
	counters, err := store.ReadCounters(context.Background(), shareStatsKindShare, 7, now.Add(-time.Minute), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(5), counters[shareStatsRequestsField])
	require.Positive(t, counters[shareStatsIncompleteField])
}

type shareReviewReadFailureRepo struct{ shareMemoryRepo }

func (*shareReviewReadFailureRepo) GetPolicy(context.Context, int64) (*ShareSchedulingPolicyRow, error) {
	return nil, errors.New("post-commit read unavailable")
}
func TestShareReviewCommittedSaveNeedsNoPostCommitRead(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})
	repo := &shareReviewReadFailureRepo{}
	env.share.repo = repo
	view, err := env.save(t, 0, shareAdminPolicy)
	require.NoError(t, err)
	require.Equal(t, int64(1), view.Version)
	require.Equal(t, view.Version, view.AppliedVersion)
	require.Equal(t, view.Version, env.share.groupPolicy(shareTestGroupID).version)
	raw, _ := json.Marshal(twoAccountPolicy())
	env.share.installGroup(&ShareSchedulingPolicyRow{GroupID: shareTestGroupID, Version: 0, Policy: raw})
	require.Equal(t, int64(1), env.share.groupPolicy(shareTestGroupID).version)
}

func TestShareReviewDisabledExclusionsPreserveOtherGates(t *testing.T) {
	env := newShareTestEnv(t, shareTestPolicy(sharesched.SchedulerShare, sharesched.AccountRule{AccountID: 1, Tier: 1, Weight: 1, Disabled: true}), Account{ID: 1}, Account{ID: 2})
	gid := shareTestGroupID
	before := map[int64]struct{}{3: {}}
	after := env.share.excludeDisabled(&gid, before)
	require.Contains(t, after, int64(1))
	require.Contains(t, after, int64(3))
	require.NotContains(t, before, int64(1))
	result := env.selectAccount(t, context.Background(), "stopped", nil)
	require.Equal(t, int64(2), result.Account.ID)
	result.ReleaseFunc()
}

func TestShareReviewDisabledResponseOwnerFailsWithoutMovingProtocolState(t *testing.T) {
	p := twoAccountPolicy()
	p.Accounts[0].Disabled = true
	env := newOpenAIShareTestEnv(t, p, 1, 2)
	ctx := context.Background()
	gid := shareTestGroupID
	require.NoError(t, env.svc.getOpenAIWSStateStore().BindResponseAccount(ctx, gid, "resp_stopped", 1, time.Hour))
	result, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "resp_stopped", "chain", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, result)
}
