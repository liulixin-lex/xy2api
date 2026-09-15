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
	rejectStart        bool
}

func (r *iqMonitorRepo) StartIQCheck(context.Context, IQCheckClaim, time.Time, map[string]IQQuotaPolicy) (bool, error) {
	r.started++
	return !r.rejectStart, nil
}
func (r *iqMonitorRepo) DeferIQCheck(_ context.Context, _ IQCheckClaim, reason string, _ time.Time) error {
	r.deferred = reason
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
	acquired, released int
}

func (c *iqMonitorConcurrency) GetAccountConcurrency(context.Context, int64) (int, error) {
	if c.fail {
		return 0, errors.New("offline")
	}
	return c.busy, nil
}
func (c *iqMonitorConcurrency) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	c.acquired++
	return true, nil
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
	}{{"available", 0, false, false}, {"busy", 1, false, false}, {"redis_down", 0, true, false}, {"manual_off", 0, false, true}} {
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
