//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// memResilienceStore is an in-memory ResilienceStore for tests.
type memResilienceStore struct {
	mu         sync.Mutex
	locks      map[int64]bool
	gen        map[string]int64
	recovering map[int64]time.Time
	sf         map[string]map[int64]int64
	avoid      map[string]map[int64]time.Time
	stable     map[int64]time.Time
	undeliv    map[string]int64
	ratios     map[string]float64
	fbKeys     map[string]time.Time
}

func newMemResilienceStore() *memResilienceStore {
	return &memResilienceStore{locks: map[int64]bool{}, gen: map[string]int64{}, recovering: map[int64]time.Time{},
		sf: map[string]map[int64]int64{}, avoid: map[string]map[int64]time.Time{}, stable: map[int64]time.Time{},
		undeliv: map[string]int64{}, ratios: map[string]float64{}, fbKeys: map[string]time.Time{}}
}

func memSessionKey(g int64, h string) string { return h + "#" + time.Duration(g).String() }

func (m *memResilienceStore) IncrSessionFailure(_ context.Context, g int64, h string, a int64, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memSessionKey(g, h)
	if m.sf[k] == nil {
		m.sf[k] = map[int64]int64{}
	}
	m.sf[k][a]++
	return m.sf[k][a], nil
}
func (m *memResilienceStore) ClearSessionFailure(_ context.Context, g int64, h string, a int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sf[memSessionKey(g, h)], a)
	return nil
}
func (m *memResilienceStore) SessionFailures(_ context.Context, g int64, h string) (map[int64]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]int64{}
	for k, v := range m.sf[memSessionKey(g, h)] {
		out[k] = v
	}
	return out, nil
}
func (m *memResilienceStore) AddSessionAvoid(_ context.Context, g int64, h string, a int64, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memSessionKey(g, h)
	if m.avoid[k] == nil {
		m.avoid[k] = map[int64]time.Time{}
	}
	m.avoid[k][a] = time.Now().Add(ttl)
	return nil
}
func (m *memResilienceStore) SessionAvoid(_ context.Context, g int64, h string) (map[int64]struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]struct{}{}
	for a, until := range m.avoid[memSessionKey(g, h)] {
		if time.Now().Before(until) {
			out[a] = struct{}{}
		}
	}
	return out, nil
}
func (m *memResilienceStore) TryLockBalanceProbe(_ context.Context, a int64, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[a] {
		return false, nil
	}
	m.locks[a] = true
	return true, nil
}
func (m *memResilienceStore) UnlockBalanceProbe(_ context.Context, a int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, a)
	return nil
}
func (m *memResilienceStore) IncrGenerationProbe(_ context.Context, a int64, day string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := day + "#" + time.Duration(a).String()
	m.gen[k]++
	return m.gen[k], nil
}
func (m *memResilienceStore) SetRecovering(_ context.Context, a int64, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recovering[a] = until
	return nil
}
func (m *memResilienceStore) ClearRecovering(_ context.Context, a int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recovering, a)
	return nil
}
func (m *memResilienceStore) Recovering(_ context.Context, now time.Time) (map[int64]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]time.Time{}
	for a, u := range m.recovering {
		if now.Before(u) {
			out[a] = u
		}
	}
	return out, nil
}
func (m *memResilienceStore) MarkGroupStable(_ context.Context, g int64, now time.Time) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.stable[g]; ok {
		return t, nil
	}
	m.stable[g] = now
	return now, nil
}
func (m *memResilienceStore) ClearGroupStable(_ context.Context, g int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.stable, g)
	return nil
}
func (m *memResilienceStore) IncrUndelivered(_ context.Context, k int64, hour string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hour + "#" + time.Duration(k).String()
	m.undeliv[key]++
	return m.undeliv[key], nil
}
func (m *memResilienceStore) SetSessionCacheRatio(_ context.Context, key string, ratio float64, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ratios[key] = ratio
	return nil
}
func (m *memResilienceStore) SessionCacheRatio(_ context.Context, key string) (float64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.ratios[key]
	return v, ok, nil
}

type fakeResilienceRepo struct {
	ids    []int64
	p99    map[string]int
	p99Err error
	states map[string]int64
}

func (f *fakeResilienceRepo) ListBalanceTrackedAccountIDs(context.Context) ([]int64, error) {
	return f.ids, nil
}

func (f *fakeResilienceRepo) OutputTokenP99ByModel(context.Context, time.Time, int) (map[string]int, error) {
	return f.p99, f.p99Err
}

func (f *fakeResilienceRepo) CountBalanceStates(context.Context) (map[string]int64, error) {
	return f.states, nil
}

type probeFixture struct {
	r      *ResilienceService
	rl     *RateLimitService
	repo   *resilienceAccountRepo
	store  *memResilienceStore
	calls  int
	answer resilience.ProbeVerdict
	auth   *balanceProbeAuthFailure
}

func newProbeFixture(t *testing.T) *probeFixture {
	t.Helper()
	withResilienceSettings(t, func(s *resilience.Settings) { s.Balance.JitterRatio = 0 })
	account := &Account{ID: 300, Name: "relay", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	repo := &resilienceAccountRepo{account: account}
	f := &probeFixture{repo: repo, store: newMemResilienceStore()}
	f.rl = NewRateLimitService(repo, nil, nil, nil, nil)
	f.r = NewResilienceService(nil, f.store)
	f.r.rateLimit = f.rl
	f.rl.SetResilienceService(f.r)
	f.r.ConfigureBalanceProbe(&fakeResilienceRepo{ids: []int64{account.ID}}, repo, nil)
	f.r.probe.probe = func(context.Context, *Account) (resilience.ProbeVerdict, string, string, *balanceProbeAuthFailure) {
		f.calls++
		return f.answer, "test", "evidence", f.auth
	}
	return f
}

func (f *probeFixture) exhaust(t *testing.T) {
	t.Helper()
	f.rl.HandleUpstreamError(context.Background(), f.repo.account, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"insufficient balance"}}`))
	require.Equal(t, resilience.BalanceExhausted, resilience.BalanceRecordFrom(f.repo.account.Extra).State)
}

// forceDue moves the next check into the past.
func (f *probeFixture) forceDue() {
	rec := resilience.BalanceRecordFrom(f.repo.account.Extra)
	rec.NextCheckAt = time.Now().Add(-time.Second)
	for k, v := range rec.Extra() {
		f.repo.account.Extra[k] = v
	}
}

func TestProbeNotDueDoesNothing(t *testing.T) {
	f := newProbeFixture(t)
	f.exhaust(t)
	f.r.runBalanceProbes(context.Background())
	require.Equal(t, 0, f.calls)
}

func TestProbePositiveEntersRecovering(t *testing.T) {
	// Spec: automatic recovery after top-up.
	f := newProbeFixture(t)
	f.exhaust(t)
	f.forceDue()
	f.answer = resilience.ProbePositive
	f.r.runBalanceProbes(context.Background())
	require.Equal(t, 1, f.calls)
	rec := resilience.BalanceRecordFrom(f.repo.account.Extra)
	require.Equal(t, resilience.BalanceRecovering, rec.State)
	require.True(t, f.repo.tempUntil.IsZero(), "balance pause lifted")
	require.Equal(t, 1, f.r.RecoveringCap(300, time.Now()))
	_, ok := f.store.recovering[300]
	require.True(t, ok)
}

func TestProbeNegativeBacksOff(t *testing.T) {
	f := newProbeFixture(t)
	f.exhaust(t)
	f.forceDue()
	f.answer = resilience.ProbeNegative
	f.r.runBalanceProbes(context.Background())
	rec := resilience.BalanceRecordFrom(f.repo.account.Extra)
	require.Equal(t, resilience.BalanceExhausted, rec.State)
	require.Equal(t, 1, rec.Level)
	require.WithinDuration(t, time.Now().Add(120*time.Second), rec.NextCheckAt, 3*time.Second)
	require.True(t, strings.HasPrefix(f.repo.tempReason, resilience.ReasonPrefixBalanceExhausted))
	require.WithinDuration(t, rec.NextCheckAt.Add(10*time.Minute), f.repo.tempUntil, 3*time.Second)
}

func TestProbeSingleFlight(t *testing.T) {
	// Spec: two instances schedule the same probe -> one probe request.
	f := newProbeFixture(t)
	f.exhaust(t)
	f.forceDue()
	f.store.locks[300] = true // another instance holds the lock
	f.r.runBalanceProbes(context.Background())
	require.Equal(t, 0, f.calls)
}

func TestProbeAuthFailureFollowsAuthPath(t *testing.T) {
	// Spec: probe finds invalid credentials.
	f := newProbeFixture(t)
	f.exhaust(t)
	f.forceDue()
	f.auth = &balanceProbeAuthFailure{status: http.StatusUnauthorized, body: []byte(`{"error":{"message":"invalid api key"}}`)}
	f.r.runBalanceProbes(context.Background())
	require.Equal(t, 1, f.repo.errorCalls)
}

func TestAdminOverrideIsRespected(t *testing.T) {
	f := newProbeFixture(t)
	f.exhaust(t)
	// The administrator cleared the pause (recover-state) before the next check.
	f.repo.tempUntil = time.Time{}
	f.repo.tempReason = ""
	f.r.runBalanceProbes(context.Background())
	require.Equal(t, 0, f.calls)
	require.Equal(t, resilience.BalanceOK, resilience.BalanceRecordFrom(f.repo.account.Extra).State)
}

func TestExpiredOtherPauseIsNoAdminOverride(t *testing.T) {
	// A longer 403 cooldown kept the balance pause from being written; when
	// it runs out the account is still empty and must stay excluded.
	f := newProbeFixture(t)
	f.exhaust(t)
	f.repo.tempUntil = time.Now().Add(-time.Second)
	f.repo.tempReason = "auth:403_cooldown"
	f.r.runBalanceProbes(context.Background())
	rec := resilience.BalanceRecordFrom(f.repo.account.Extra)
	require.Equal(t, resilience.BalanceExhausted, rec.State)
	require.True(t, strings.HasPrefix(f.repo.tempReason, resilience.ReasonPrefixBalanceExhausted), "balance pause re-applied")
	require.True(t, f.repo.tempUntil.After(time.Now()))
}

func TestRecheckBalanceForcesProbe(t *testing.T) {
	// Spec: administrator confirms a top-up.
	f := newProbeFixture(t)
	f.exhaust(t)
	f.answer = resilience.ProbePositive
	rec, err := f.r.RecheckBalance(context.Background(), 300)
	require.NoError(t, err)
	require.Equal(t, 1, f.calls)
	require.Equal(t, resilience.BalanceRecovering, rec.State)
}

func TestRecoveringWindowEndsInLoop(t *testing.T) {
	f := newProbeFixture(t)
	f.exhaust(t)
	f.forceDue()
	f.answer = resilience.ProbePositive
	f.r.runBalanceProbes(context.Background())
	rec := resilience.BalanceRecordFrom(f.repo.account.Extra)
	rec.RecoveringUntil = time.Now().Add(-time.Second)
	for k, v := range rec.Extra() {
		f.repo.account.Extra[k] = v
	}
	f.r.runBalanceProbes(context.Background())
	require.Equal(t, resilience.BalanceOK, resilience.BalanceRecordFrom(f.repo.account.Extra).State)
	require.Equal(t, 0, f.r.RecoveringCap(300, time.Now()))
}

func TestUnrecoveredNotification(t *testing.T) {
	f := newProbeFixture(t)
	f.exhaust(t)
	rec := resilience.BalanceRecordFrom(f.repo.account.Extra)
	rec.Since = time.Now().Add(-73 * time.Hour)
	for k, v := range rec.Extra() {
		f.repo.account.Extra[k] = v
	}
	f.r.runBalanceProbes(context.Background())
	require.True(t, resilience.BalanceRecordFrom(f.repo.account.Extra).Notified)
}

func TestVerdictFromUsageBody(t *testing.T) {
	v, _, ok := verdictFromUsageBody([]byte(`{"mode":"unrestricted","isValid":true,"remaining":12.5,"balance":12.5}`))
	require.True(t, ok)
	require.Equal(t, resilience.ProbePositive, v)
	v, _, ok = verdictFromUsageBody([]byte(`{"mode":"unrestricted","remaining":0}`))
	require.True(t, ok)
	require.Equal(t, resilience.ProbeNegative, v)
	v, _, ok = verdictFromUsageBody([]byte(`{"mode":"quota_limited","status":"quota_exhausted"}`))
	require.True(t, ok)
	require.Equal(t, resilience.ProbeNegative, v)
	_, _, ok = verdictFromUsageBody([]byte(`{"mode":"quota_limited","isValid":true}`))
	require.False(t, ok, "no amount: inconclusive")
	v, _, ok = verdictFromUsageBody([]byte(`{"mode":"unrestricted","isValid":true,"planName":"Pro","remaining":-1}`))
	require.True(t, ok)
	require.Equal(t, resilience.ProbePositive, v, "a subscription without limits reports -1")
	v, _, ok = verdictFromUsageBody([]byte(`{"mode":"unrestricted","isValid":true,"remaining":-1.5,"balance":-1.5}`))
	require.True(t, ok)
	require.Equal(t, resilience.ProbeNegative, v, "an overdrawn balance stays negative")
}

func TestVerdictFromNewAPI(t *testing.T) {
	v, _, ok := verdictFromNewAPI(10, 500) // $10 limit, $5 used
	require.True(t, ok)
	require.Equal(t, resilience.ProbePositive, v)
	v, _, ok = verdictFromNewAPI(10, 1000)
	require.True(t, ok)
	require.Equal(t, resilience.ProbeNegative, v)
	_, _, ok = verdictFromNewAPI(0, 0)
	require.False(t, ok)
}

func memFallbackKey(g, k int64) string {
	return time.Duration(g).String() + "#" + time.Duration(k).String()
}

func (m *memResilienceStore) MarkFallbackKey(_ context.Context, g, k int64, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fbKeys[memFallbackKey(g, k)] = time.Now().Add(ttl)
	return nil
}

func (m *memResilienceStore) FallbackKey(_ context.Context, g, k int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.fbKeys[memFallbackKey(g, k)]
	return ok && time.Now().Before(until), nil
}

func (m *memResilienceStore) ClearFallbackKey(_ context.Context, g, k int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fbKeys, memFallbackKey(g, k))
	return nil
}

func TestResilienceStatsReportsCountersAndBalanceStates(t *testing.T) {
	f := newProbeFixture(t)
	f.r.ConfigureBalanceProbe(&fakeResilienceRepo{states: map[string]int64{"OK": 5, "EXHAUSTED": 1}}, f.repo, nil)
	resilience.Count(resilience.MetricStall, 9101, 0, "stats_test")

	stats := f.r.Stats(context.Background(), 0)
	require.Equal(t, int64(resilience.MetricsRetention/time.Second), stats.WindowSeconds, "an unset window covers the retention")
	require.Equal(t, "instance", stats.Scope)
	require.Equal(t, map[string]int64{"OK": 5, "EXHAUSTED": 1}, stats.BalanceStates)
	found := false
	for _, row := range stats.Counters {
		if row.Name == resilience.MetricStall && row.GroupID == 9101 && row.Label == "stats_test" {
			found = row.Count >= 1
		}
	}
	require.True(t, found)

	var nilService *ResilienceService
	require.Empty(t, nilService.Stats(context.Background(), 15*time.Minute).BalanceStates)
}
