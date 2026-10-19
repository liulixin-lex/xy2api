//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// resilienceAccountRepo is an in-memory account store for penalty tests.
type resilienceAccountRepo struct {
	AccountRepository
	mu          sync.Mutex
	account     *Account
	errorCalls  int
	tempUntil   time.Time
	tempReason  string
	tempCalls   int
	clearCalls  int
	extraWrites int
	// afterGet runs after GetByID returned, to interleave a concurrent write.
	afterGet func()
}

func (r *resilienceAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	if r.afterGet != nil {
		defer r.afterGet()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *r.account
	cp.Extra = map[string]any{}
	for k, v := range r.account.Extra {
		cp.Extra[k] = v
	}
	if !r.tempUntil.IsZero() {
		until := r.tempUntil
		cp.TempUnschedulableUntil = &until
		cp.TempUnschedulableReason = r.tempReason
	}
	return &cp, nil
}

func (r *resilienceAccountRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account.Extra == nil {
		r.account.Extra = map[string]any{}
	}
	for k, v := range updates {
		r.account.Extra[k] = v
	}
	r.extraWrites++
	return nil
}

// SetTempUnschedulable mirrors the repository: by tier while failure
// handling is on, only extending otherwise.
func (r *resilienceAccountRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	if resilience.Current().FailureOn() {
		_, err := r.SetTempUnschedulableTiered(ctx, id, until, reason, resilience.TempUnschedTier(reason) == resilience.TierHard, false, resilience.SoftReasonPrefixes())
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tempCalls++
	if r.tempUntil.Before(until) {
		r.tempUntil = until
		r.tempReason = reason
	}
	return nil
}

// SetTempUnschedulableTiered mirrors the repository statement (review R01).
func (r *resilienceAccountRepo) SetTempUnschedulableTiered(_ context.Context, _ int64, until time.Time, reason string, hard, keepLaterEnd bool, softPrefixes []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tempCalls++
	now := time.Now()
	active := !r.tempUntil.IsZero() && r.tempUntil.After(now)
	curSoft := false
	for _, p := range softPrefixes {
		if strings.HasPrefix(strings.TrimSpace(r.tempReason), p) {
			curSoft = true
		}
	}
	if active && !(r.tempUntil.Before(until) && (hard || curSoft)) && !(hard && curSoft) {
		return false, nil
	}
	if !active || !keepLaterEnd || r.tempUntil.Before(until) {
		r.tempUntil = until
	}
	r.tempReason = reason
	return true, nil
}

// ClearTempUnschedulableIf mirrors the repository statement (review R02).
func (r *resilienceAccountRepo) ClearTempUnschedulableIf(_ context.Context, _ int64, reason string, until time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tempReason != reason || !r.tempUntil.Equal(until) {
		return false, nil
	}
	r.clearCalls++
	r.tempUntil = time.Time{}
	r.tempReason = ""
	return true, nil
}

func (r *resilienceAccountRepo) ClearTempUnschedulable(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearCalls++
	r.tempUntil = time.Time{}
	r.tempReason = ""
	return nil
}

func (r *resilienceAccountRepo) SetError(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errorCalls++
	return nil
}

func (r *resilienceAccountRepo) SetRateLimited(context.Context, int64, time.Time) error { return nil }

type resilience403Counter struct{ n int64 }

func (c *resilience403Counter) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	c.n++
	return c.n, nil
}

func (c *resilience403Counter) ResetOpenAI403Count(context.Context, int64) error { return nil }

func newPenaltyFixture(t *testing.T, platform string) (*RateLimitService, *resilienceAccountRepo, *Account, *ResilienceService) {
	t.Helper()
	withResilienceSettings(t, nil)
	account := &Account{ID: 254, Name: "relay", Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	repo := &resilienceAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(&resilience403Counter{})
	r := NewResilienceService(nil, nil)
	r.rateLimit = svc
	svc.SetResilienceService(r)
	return svc, repo, account, r
}

func newPenaltyFixtureKeepSettings(t *testing.T, platform string) (*RateLimitService, *resilienceAccountRepo, *Account, *ResilienceService) {
	t.Helper()
	account := &Account{ID: 255, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	repo := &resilienceAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	r := NewResilienceService(nil, nil)
	r.rateLimit = svc
	svc.SetResilienceService(r)
	return svc, repo, account, r
}

func TestUniform403CountedCooldown(t *testing.T) {
	// Spec: one 403 on an Anthropic relay -> 10 minute soft cooldown, active.
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	body := []byte(`{"error":{"type":"permission_error","message":"forbidden"}}`)
	require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body))
	require.Equal(t, 0, repo.errorCalls)
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.Reason403Cooldown), repo.tempReason)
	require.Equal(t, resilience.TierSoft, resilience.TempUnschedTier(repo.tempReason))
	require.WithinDuration(t, time.Now().Add(10*time.Minute), repo.tempUntil, 5*time.Second)

	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
	require.Equal(t, 0, repo.errorCalls)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
	require.Equal(t, 1, repo.errorCalls, "third strike sets the error state")
}

func TestTransientCooldownHonoursRetryAfter(t *testing.T) {
	// Spec: relay asks for 60 seconds.
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{},
		[]byte(`<html>error code: 502 "retry_after":60</html>`))
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixTransient), repo.tempReason)
	require.WithinDuration(t, time.Now().Add(60*time.Second), repo.tempUntil, 3*time.Second)

	// No retry_after: the default 30 s.
	svc2, repo2, account2, _ := newPenaltyFixture(t, PlatformAnthropic)
	svc2.HandleUpstreamError(context.Background(), account2, 524, http.Header{}, []byte(`gateway timeout`))
	require.WithinDuration(t, time.Now().Add(30*time.Second), repo2.tempUntil, 3*time.Second)

	// retry_after above the cap is capped at 120 s.
	svc3, repo3, account3, _ := newPenaltyFixture(t, PlatformGemini)
	svc3.HandleUpstreamError(context.Background(), account3, 503, http.Header{"Retry-After": {"600"}}, nil)
	require.WithinDuration(t, time.Now().Add(120*time.Second), repo3.tempUntil, 3*time.Second)
}

func TestTransientCooldownRefreshesGroupSnapshot(t *testing.T) {
	// Staging FB-2: the fallback decision and Retry-After of the next request
	// read a 2 s group snapshot taken before the cooldown, so the request
	// stayed in the primary group with "retry in 2 s".
	svc, _, account, r := newPenaltyFixture(t, PlatformAnthropic)
	account.GroupIDs = []int64{77}
	r.lastResortGroups.entries = map[int64]lastResortGroupEntry{
		77: {accounts: []Account{*account}, expires: time.Now().Add(time.Minute)},
		78: {accounts: []Account{}, expires: time.Now().Add(time.Minute)},
	}
	svc.HandleUpstreamError(context.Background(), account, 524, http.Header{}, []byte(`gateway timeout`))
	_, cached := r.lastResortGroups.entries[77]
	require.False(t, cached, "the cooled-down account's group is re-read")
	_, other := r.lastResortGroups.entries[78]
	require.True(t, other, "other groups keep their snapshot")
}

func TestTransientCooldownOpenAIOnlyWithRetryAfter(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"}}`))
	require.Equal(t, 0, repo.tempCalls, "model-scoped transient state handles OpenAI 5xx")
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"},"retry_after":120}`))
	require.Equal(t, 1, repo.tempCalls)
	require.WithinDuration(t, time.Now().Add(120*time.Second), repo.tempUntil, 3*time.Second)
}

func TestApplyResilienceOutcomeStreamBalanceAndStall(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	res := resilience.Classify(resilience.Outcome{Source: resilience.SourceStreamEvent, StatusCode: 200, Code: "insufficient_quota"}, nil)
	svc.ApplyResilienceOutcome(context.Background(), account, res, "stream insufficient_quota")
	requireBalancePause(t, repo, 300*time.Second)

	svc2, repo2, account2, _ := newPenaltyFixture(t, PlatformAnthropic)
	svc2.ApplyResilienceOutcome(context.Background(), account2, resilience.Result{Category: resilience.CategoryStall}, "stall")
	require.WithinDuration(t, time.Now().Add(30*time.Second), repo2.tempUntil, 3*time.Second)
}

func TestBrokenStreamUsageLiftsNoCooldown(t *testing.T) {
	// The partial usage of a stream that broke on a last-resort account is
	// recorded, but it is no success: the transient cooldown stays.
	svc, repo, account, r := newPenaltyFixture(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"bad gateway"}}`))
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixTransient), repo.tempReason)
	r.lastResortUsed.Store(account.ID, time.Now())

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	st := resilience.NewRequestState()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(resilience.WithRequestState(context.Background(), st))
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{AccountID: account.ID, Kind: "stream_error", Message: "upstream stream broke"})
	r.NoteAccountSuccess(c.Request.Context(), account)
	require.Zero(t, repo.clearCalls, "a broken stream keeps the cooldown")

	r.NoteAccountSuccess(context.Background(), account)
	require.Equal(t, 1, repo.clearCalls, "a real success of the last-resort account lifts it")
}

func TestResilienceOffKeepsLegacyPenalties(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	svc, repo, account, _ := newPenaltyFixtureKeepSettings(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`"retry_after":60`))
	require.Equal(t, 0, repo.tempCalls)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"insufficient balance"}}`))
	require.Equal(t, 1, repo.errorCalls)
}
