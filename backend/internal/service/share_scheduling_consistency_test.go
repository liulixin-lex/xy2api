//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// TestShareSchedulingWeightsAreLoadFactorsEverywhere: both adapters and the
// admin view weigh an account by its effective load factor (the load factor,
// else the concurrency) and tier it by its priority.
func TestShareSchedulingWeightsAreLoadFactorsEverywhere(t *testing.T) {
	for _, load := range []int{0, 1, 50} {
		t.Run(fmt.Sprint(load), func(t *testing.T) {
			want := map[int64]int{1: load, 2: 10}
			if load == 0 {
				want[1] = 10
			}
			p := shareTestPolicy(sharesched.SchedulerShare)
			env := newShareTestEnv(t, p, Account{ID: 1, Priority: 2, Concurrency: 10, LoadFactor: &load}, Account{ID: 2, Priority: 3, Concurrency: 10})
			gid := shareTestGroupID
			_, inputs, err := env.gateway.shareGatewayCandidates(context.Background(), nil, &gid, "", nil, 0)
			require.NoError(t, err)
			views, _ := shareMemberViews(env.repo.accounts, time.Now())
			for _, v := range views {
				require.Equal(t, want[v.AccountID], v.Weight)
			}
			d := sharesched.Plan(sharesched.Request{GroupID: gid, Key: "weight"}, inputs.cands)
			for _, entry := range d.Entries {
				require.Equal(t, want[entry.AccountID], entry.LoadFactor)
				require.Equal(t, int(entry.AccountID)+1, entry.Priority)
			}
			openai := newOpenAIShareTestEnv(t, p, 1, 2)
			openai.account(1).Concurrency, openai.account(2).Concurrency = 10, 10
			openai.account(1).LoadFactor = &load
			cands, _, _, err := openai.svc.shareOpenAICandidates(context.Background(), openAIShareSelection{groupID: &gid, platform: PlatformOpenAI}, 0)
			require.NoError(t, err)
			d = sharesched.Plan(sharesched.Request{GroupID: gid, Key: "weight"}, cands)
			for _, entry := range d.Entries {
				require.Equal(t, want[entry.AccountID], entry.LoadFactor)
			}
		})
	}
}

// shareSlowSlotCache blocks slot requests until their context ends.
type shareSlowSlotCache struct {
	*shareTestConcurrency
	deadline time.Time
}

func (c *shareSlowSlotCache) AcquireAccountSlot(ctx context.Context, _ int64, _ int, _ string) (bool, error) {
	c.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return false, ctx.Err()
}

func TestShareSchedulingWaitBudgetBoundsSlowSlotCalls(t *testing.T) {
	cache := &shareSlowSlotCache{shareTestConcurrency: newShareTestConcurrency()}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exec := &shareExecutor{ctx: parent, concurrency: NewConcurrencyService(cache)}
	start := time.Now()
	res, _ := exec.waitForSlot(&Account{ID: 1}, 1, 40*time.Millisecond, 3)
	require.Nil(t, res)
	require.WithinDuration(t, start.Add(40*time.Millisecond), cache.deadline, 10*time.Millisecond)
	require.Less(t, time.Since(start), 300*time.Millisecond, "a slow slot call cannot use the request's longer deadline")
	require.Zero(t, cache.waiting[1], "the wait count is released")
}

// shareReadFailureRepo commits saves but cannot read policies after one.
type shareReadFailureRepo struct {
	shareMemoryRepo
	saved bool
}

func (r *shareReadFailureRepo) GetPolicy(ctx context.Context, groupID int64) (*ShareSchedulingPolicyRow, error) {
	if r.saved {
		return nil, errors.New("post-commit read unavailable")
	}
	return r.shareMemoryRepo.GetPolicy(ctx, groupID)
}

func (r *shareReadFailureRepo) SavePolicy(ctx context.Context, groupID, expectedVersion int64, scheduler string, policy []byte, updatedBy int64) (*ShareSchedulingPolicyRow, error) {
	row, err := r.shareMemoryRepo.SavePolicy(ctx, groupID, expectedVersion, scheduler, policy, updatedBy)
	r.saved = r.saved || err == nil
	return row, err
}

func TestShareSchedulingCommittedSaveNeedsNoReadBack(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})
	env.share.repo = &shareReadFailureRepo{}
	view, err := env.save(t, 0, shareAdminPolicy)
	require.NoError(t, err, "a committed save is reported as saved")
	require.Equal(t, int64(1), view.Version)
	require.Equal(t, view.Version, env.share.groupPolicy(shareTestGroupID).version, "this node runs the saved policy at once")

	raw, _ := json.Marshal(twoAccountPolicy().policy)
	env.share.installGroup(&ShareSchedulingPolicyRow{GroupID: shareTestGroupID, Version: 0, Policy: raw})
	require.Equal(t, int64(1), env.share.groupPolicy(shareTestGroupID).version, "a late older row never replaces a newer one")
}

// shareHeldSettings returns its value, then holds the read until released.
type shareHeldSettings struct {
	shareMemorySettings
	read, release chan struct{}
}

func (s *shareHeldSettings) GetValue(ctx context.Context, key string) (string, error) {
	v, err := s.shareMemorySettings.GetValue(ctx, key)
	s.read <- struct{}{}
	<-s.release
	return v, err
}

func TestShareSchedulingSettingsReloadNeverRevertsAWrite(t *testing.T) {
	settings := &shareHeldSettings{shareMemorySettings: shareMemorySettings{values: map[string]string{}},
		read: make(chan struct{}), release: make(chan struct{})}
	share := NewShareSchedulingService(nil, nil, settings)
	done := make(chan error, 1)
	go func() { done <- share.reloadSettings(context.Background()) }()
	<-settings.read

	want := DefaultShareSchedulingSettings()
	want.Enabled = true
	_, err := share.UpdateSettings(context.Background(), want)
	require.NoError(t, err)
	close(settings.release)
	require.NoError(t, <-done)
	require.True(t, share.Settings().Enabled, "a reload that read before the write keeps the written settings")
}

// shareHeldListRepo lists fixed rows, holding the listing until released.
type shareHeldListRepo struct {
	shareMemoryRepo
	rows            []ShareSchedulingPolicyRow
	listed, release chan struct{}
}

func (r *shareHeldListRepo) ListPolicies(context.Context) ([]ShareSchedulingPolicyRow, error) {
	r.listed <- struct{}{}
	<-r.release
	return r.rows, nil
}

func TestShareSchedulingFullReloadKeepsConcurrentSaves(t *testing.T) {
	raw, _ := json.Marshal(twoAccountPolicy().policy)
	repo := &shareHeldListRepo{rows: []ShareSchedulingPolicyRow{{GroupID: 9, Version: 1, Policy: raw}},
		listed: make(chan struct{}), release: make(chan struct{})}
	share := NewShareSchedulingService(repo, nil, nil)
	done := make(chan error, 1)
	go func() { done <- share.reloadAll(context.Background()) }()
	<-repo.listed

	share.installGroup(&ShareSchedulingPolicyRow{GroupID: 9, Version: 2, Policy: raw})
	share.installGroup(&ShareSchedulingPolicyRow{GroupID: 10, Version: 1, Policy: raw})
	close(repo.release)
	require.NoError(t, <-done)
	loaded := share.state.Load().groups
	require.Equal(t, int64(2), loaded[9].version, "the listing's older version does not replace a save")
	require.NotNil(t, loaded[10], "a group saved after the listing stays loaded")
}
