package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/stretchr/testify/require"
)

type iqMonitorRepo struct {
	IQCheckRepository
	started, completed int
	deferred           string
	deferredNext       time.Time
	startedAt          time.Time
	rejectStart        bool
}

func (r *iqMonitorRepo) StartIQCheck(_ context.Context, _ IQCheckClaim, now time.Time, _ map[string]IQQuotaPolicy) (bool, error) {
	r.started++
	r.startedAt = now
	return !r.rejectStart, nil
}
func (r *iqMonitorRepo) DeferIQCheck(_ context.Context, _ IQCheckClaim, reason string, next time.Time) error {
	r.deferred = reason
	r.deferredNext = next
	return nil
}
func (r *iqMonitorRepo) CompleteIQCheck(context.Context, IQCheckClaim, iqcheck.Result, time.Time) error {
	r.completed++
	return nil
}

type iqMonitorConcurrency struct {
	ConcurrencyCache
	busy               int
	fail               bool
	failAcquire        bool
	denyAcquire        bool
	busyForAttempts    int
	slotLimit          int
	acquired, released int
	acquiredAt         time.Time
}

func (c *iqMonitorConcurrency) GetAccountConcurrency(context.Context, int64) (int, error) {
	if c.fail {
		return 0, errors.New("offline")
	}
	return c.busy, nil
}
func (c *iqMonitorConcurrency) AcquireAccountSlot(_ context.Context, _ int64, limit int, _ string) (bool, error) {
	c.acquired++
	c.acquiredAt = time.Now().UTC()
	c.slotLimit = limit
	if c.fail || c.failAcquire {
		return false, errors.New("offline")
	}
	return !c.denyAcquire && c.busy < limit && c.acquired > c.busyForAttempts, nil
}

func TestIQMonitoringAvoidsUnnecessaryDeferrals(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		busy, transientAttempts int
	}{{"last_free_slot", 1, 0}, {"brief_contention", 0, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "fixture"}, IQCheck: domain.DefaultIQCheck()}
			a.IQCheck.Enabled = true
			cache := &iqMonitorConcurrency{busy: tc.busy, busyForAttempts: tc.transientAttempts}
			repo := &iqMonitorRepo{}
			upstream := &iqProbeTransport{status: 200, body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"content":"21"}}]}`}
			s := &IQCheckService{repo: repo, accounts: &iqProbeAccounts{account: a}, concurrency: NewConcurrencyService(cache), tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}}
			s.execute(context.Background(), IQCheckClaim{AccountID: 1, Profile: a.IQCheck.Profile()})
			require.Empty(t, repo.deferred, "available capacity or brief contention should not defer the account")
			require.Len(t, upstream.requests, 1)
			require.Equal(t, 1, repo.started)
			require.Equal(t, 1, cache.released)
		})
	}
	now := time.Now().UTC()
	until := now.Add(5 * time.Second)
	reason, next := iqHealth(&Account{Status: StatusActive, Schedulable: true, RateLimitResetAt: &until}, now)
	require.Equal(t, "account_cooldown", reason)
	require.Equal(t, until, next, "a short cooldown must not be rounded up to one minute")
}

func TestIQMonitoringSlotWaitCancels(t *testing.T) {
	cache := &iqMonitorConcurrency{busy: 1}
	s := &IQCheckService{concurrency: NewConcurrencyService(cache)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	slot, err := s.acquireIQSlot(ctx, 1, 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, slot)
	require.Equal(t, 1, cache.acquired)
	require.Zero(t, cache.released)
}

func TestIQMonitoringSpareCapacity(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		capacity, busy                     int
		deferrals                          int
		failRead, failAcquire, denyAcquire bool
		reason                             string
	}{
		{name: "spare_capacity", capacity: 10, busy: 1},
		{name: "last_spare_slot", capacity: 10, busy: 8},
		{name: "last_available_slot", capacity: 10, busy: 9},
		{name: "no_wait_credit_required", capacity: 2, busy: 1, deferrals: 2},
		{name: "waiting_uses_last_free_slot", capacity: 2, busy: 1, deferrals: 3},
		{name: "waiting_still_respects_cap", capacity: 2, busy: 2, deferrals: 100, reason: "account_busy"},
		{name: "saturated", capacity: 10, busy: 10, reason: "account_busy"},
		{name: "single_idle", capacity: 1},
		{name: "single_busy", capacity: 1, busy: 1, reason: "account_busy"},
		{name: "unlimited", capacity: 0, busy: 8},
		{name: "cache_unavailable", capacity: 10, failRead: true, reason: "concurrency_unavailable"},
		{name: "acquire_unavailable", capacity: 10, failAcquire: true, reason: "concurrency_unavailable"},
		{name: "raced_with_traffic", capacity: 10, denyAcquire: true, reason: "account_busy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: tc.capacity, Credentials: map[string]any{"api_key": "fixture"}, IQCheck: domain.DefaultIQCheck()}
			a.IQCheck.Enabled = true
			a.IQCheck.BusyDeferrals = tc.deferrals
			cache := &iqMonitorConcurrency{busy: tc.busy, fail: tc.failRead, failAcquire: tc.failAcquire, denyAcquire: tc.denyAcquire}
			repo := &iqMonitorRepo{}
			upstream := &iqProbeTransport{status: 200, body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"content":"21"}}]}`}
			s := &IQCheckService{repo: repo, accounts: &iqProbeAccounts{account: a}, concurrency: NewConcurrencyService(cache), tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}}
			before := time.Now().UTC()
			s.execute(context.Background(), IQCheckClaim{AccountID: 1, Profile: a.IQCheck.Profile()})
			require.Equal(t, tc.reason, repo.deferred)
			if tc.reason == "account_busy" {
				require.False(t, repo.deferredNext.Before(before.Add(15*time.Second)))
				require.False(t, repo.deferredNext.After(time.Now().UTC().Add(30*time.Second)))
			}
			if tc.reason != "" {
				require.Zero(t, repo.started)
				require.Empty(t, upstream.requests)
				require.Zero(t, cache.released)
			} else {
				require.False(t, repo.startedAt.Before(cache.acquiredAt))
				require.Equal(t, 1, repo.completed)
				require.Len(t, upstream.requests, 1)
				if tc.capacity > 0 {
					require.Equal(t, tc.capacity, cache.slotLimit)
					require.Equal(t, 1, cache.released)
				}
			}
		})
	}
}

func TestIQMonitoringHealthUsesLatestCooldown(t *testing.T) {
	now := time.Now().UTC()
	short, long := now.Add(2*time.Minute), now.Add(time.Hour)
	a := &Account{Status: StatusActive, Schedulable: true, OverloadUntil: &short, RateLimitResetAt: &long}
	reason, next := iqHealth(a, now)
	require.Equal(t, "account_cooldown", reason)
	require.Equal(t, long, next)
}

func TestIQMonitoringStatusIsReadOnlyAndHidesLease(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, IQCheck: domain.DefaultIQCheck()}
	a.IQCheck.LeaseToken, a.IQCheck.Revision = "private-token", "private-revision"
	s := &IQCheckService{accounts: &iqProbeAccounts{account: a}}
	status, err := s.Status(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, status.LeaseToken)
	require.Empty(t, status.Revision)
	require.Equal(t, "private-token", a.IQCheck.LeaseToken)
	a.Platform = PlatformAnthropic
	_, err = s.Status(context.Background(), 1)
	require.ErrorIs(t, err, ErrIQCheckInvalid)
}
func (c *iqMonitorConcurrency) ReleaseAccountSlot(context.Context, int64, string) error {
	c.released++
	return nil
}

func TestIQMonitoringBusinessSlots(t *testing.T) {
	for _, test := range []struct {
		name              string
		busy              int
		offline, disabled bool
	}{{"available", 0, false, false}, {"busy", 2, false, false}, {"redis_down", 0, true, false}, {"manual_off", 0, false, true}} {
		t.Run(test.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: !test.disabled, Concurrency: 2, Credentials: map[string]any{"api_key": "fixture"}, IQCheck: domain.DefaultIQCheck()}
			a.IQCheck.Enabled = true
			a.IQCheck.Status = "degraded"
			a.IQCheck.Revision = "fixture"
			cache := &iqMonitorConcurrency{busy: test.busy, fail: test.offline}
			repo := &iqMonitorRepo{}
			upstream := &iqProbeTransport{status: 200, body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"content":"21"}}]}`}
			s := &IQCheckService{repo: repo, accounts: &iqProbeAccounts{account: a}, concurrency: NewConcurrencyService(cache), tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}}
			s.execute(context.Background(), IQCheckClaim{AccountID: 1, Revision: "fixture", Token: "task", Profile: a.IQCheck.Profile()})
			if test.busy > 0 || test.offline || test.disabled {
				require.Zero(t, repo.started)
				require.Empty(t, upstream.requests)
				require.NotEmpty(t, repo.deferred)
				return
			}
			require.Equal(t, 1, repo.started)
			require.Equal(t, 1, repo.completed)
			require.Equal(t, 1, cache.acquired)
			require.Equal(t, 1, cache.released)
			require.Len(t, upstream.requests, 1)
			h := upstream.requests[0].Header
			require.Equal(t, "XY2API-IQ-Monitor/1", h.Get("User-Agent"))
			require.Empty(t, h.Get("Originator"))
			require.Empty(t, h.Get("X-Codex-Window-Id"))
		})
	}
}

func TestIQMonitoringRejectedStartDoesNotProbe(t *testing.T) {
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "fixture"}, IQCheck: domain.DefaultIQCheck()}
	a.IQCheck.Enabled = true
	a.IQCheck.Revision = "fixture"
	cache := &iqMonitorConcurrency{}
	repo := &iqMonitorRepo{rejectStart: true}
	upstream := &iqProbeTransport{status: 200, body: `{"choices":[{"index":0,"finish_reason":"stop","message":{"content":"21"}}]}`}
	s := &IQCheckService{repo: repo, accounts: &iqProbeAccounts{account: a}, concurrency: NewConcurrencyService(cache), tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: upstream}}
	s.execute(context.Background(), IQCheckClaim{AccountID: 1, Revision: "fixture", Token: "task", Profile: a.IQCheck.Profile()})
	require.Equal(t, 1, repo.started)
	require.Zero(t, repo.completed)
	require.Empty(t, repo.deferred, "a rejected start must not clear another running reservation")
	require.Empty(t, upstream.requests)
	require.Equal(t, 1, cache.acquired)
	require.Equal(t, 1, cache.released)
}
