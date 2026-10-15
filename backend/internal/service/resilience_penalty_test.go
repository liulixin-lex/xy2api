//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
}

func (r *resilienceAccountRepo) GetByID(context.Context, int64) (*Account, error) {
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

func (r *resilienceAccountRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tempCalls++
	if r.tempUntil.Before(until) {
		r.tempUntil = until
		r.tempReason = reason
	}
	return nil
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
	withResilienceSettings(t, func(s *resilience.Settings) { s.Balance.JitterRatio = 0 })
	account := &Account{ID: 254, Name: "relay", Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	repo := &resilienceAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(&resilience403Counter{})
	r := NewResilienceService(nil, nil)
	r.rateLimit = svc
	svc.SetResilienceService(r)
	return svc, repo, account, r
}

func TestBalance403ExhaustsWithoutErrorState(t *testing.T) {
	// Spec: Claude relay runs out of credit.
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	disable := svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{},
		[]byte(`{"type":"error","error":{"type":"permission_error","message":"insufficient balance"}}`))
	require.True(t, disable)
	require.Equal(t, 0, repo.errorCalls, "status must stay active")
	require.True(t, strings.HasPrefix(repo.tempReason, resilience.ReasonPrefixBalanceExhausted), repo.tempReason)
	rec := resilience.BalanceRecordFrom(repo.account.Extra)
	require.Equal(t, resilience.BalanceExhausted, rec.State)
	require.WithinDuration(t, time.Now().Add(60*time.Second+10*time.Minute), repo.tempUntil, 5*time.Second)
}

func TestBalanceWrappedIn429(t *testing.T) {
	svc, repo, account, r := newPenaltyFixture(t, PlatformAnthropic)
	body := []byte(`{"error":{"type":"rate_limit_error","message":"Insufficient account balance"}}`)
	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)
	require.Equal(t, resilience.BalanceExhausted, resilience.BalanceRecordFrom(repo.account.Extra).State)

	// A recent success makes the same wording only a weak signal.
	svc2, repo2, account2, r2 := newPenaltyFixture(t, PlatformAnthropic)
	_ = r
	r2.NoteAccountSuccess(context.Background(), account2)
	svc2.HandleUpstreamError(context.Background(), account2, http.StatusTooManyRequests, http.Header{}, body)
	rec := resilience.BalanceRecordFrom(repo2.account.Extra)
	require.Equal(t, resilience.BalanceSuspect, rec.State)
	require.Equal(t, resilience.TierSoft, resilience.TempUnschedTier(repo2.tempReason))
}

func TestBalance402NoLongerSetsError(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformGemini)
	svc.HandleUpstreamError(context.Background(), account, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"payment required"}}`))
	require.Equal(t, 0, repo.errorCalls)
	require.Equal(t, resilience.BalanceExhausted, resilience.BalanceRecordFrom(repo.account.Extra).State)

	// Balance switch off: 0.3.0 behaviour.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Balance.Enabled = false })
	svc2, repo2, account2, _ := newPenaltyFixtureKeepSettings(t, PlatformGemini)
	svc2.HandleUpstreamError(context.Background(), account2, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"payment required"}}`))
	require.Equal(t, 1, repo2.errorCalls)
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
	require.Equal(t, resilience.BalanceExhausted, resilience.BalanceRecordFrom(repo.account.Extra).State)

	svc2, repo2, account2, _ := newPenaltyFixture(t, PlatformAnthropic)
	svc2.ApplyResilienceOutcome(context.Background(), account2, resilience.Result{Category: resilience.CategoryStall}, "stall")
	require.WithinDuration(t, time.Now().Add(30*time.Second), repo2.tempUntil, 3*time.Second)
}

func TestRecoveringSuccessesReturnToOK(t *testing.T) {
	svc, repo, account, r := newPenaltyFixture(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"insufficient balance"}}`))
	before := resilience.BalanceRecordFrom(repo.account.Extra)
	after := resilience.OnProbe(before, resilience.Current(), resilience.ProbePositive, "usage", "balance 10", time.Now())
	fresh, _ := repo.GetByID(context.Background(), account.ID)
	svc.persistBalanceRecord(context.Background(), fresh, before, after, "probe")
	require.Equal(t, 1, repo.clearCalls, "the balance pause is lifted")
	require.Equal(t, 1, r.RecoveringCap(account.ID, time.Now()))

	for i := 0; i < 3; i++ {
		r.NoteAccountSuccess(context.Background(), account)
	}
	require.Equal(t, resilience.BalanceOK, resilience.BalanceRecordFrom(repo.account.Extra).State)
	require.Equal(t, 0, r.RecoveringCap(account.ID, time.Now()))
}

func TestClearBalanceExclusionKeepsOtherPauses(t *testing.T) {
	svc, repo, account, _ := newPenaltyFixture(t, PlatformAnthropic)
	repo.tempUntil = time.Now().Add(time.Hour)
	repo.tempReason = "OAuth 401: expired"
	svc.clearBalanceExclusion(context.Background(), account)
	require.Equal(t, 0, repo.clearCalls)
}

func TestResilienceOffKeepsLegacyPenalties(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = false })
	svc, repo, account, _ := newPenaltyFixtureKeepSettings(t, PlatformAnthropic)
	svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`"retry_after":60`))
	require.Equal(t, 0, repo.tempCalls)
	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(`{"error":{"message":"insufficient balance"}}`))
	require.Equal(t, 1, repo.errorCalls)
}
