package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

type upstreamCostTrackingConcurrencyCache struct {
	ConcurrencyCache
	loadMap       map[int64]*AccountLoadInfo
	acquireLimits map[int64][]int
	releases      map[int64]int
	rejectAcquire bool
	afterAcquire  func(int64)
}

func (c *upstreamCostTrackingConcurrencyCache) AcquireAccountSlot(_ context.Context, accountID int64, maxConcurrency int, _ string) (bool, error) {
	if c.acquireLimits == nil {
		c.acquireLimits = make(map[int64][]int)
	}
	c.acquireLimits[accountID] = append(c.acquireLimits[accountID], maxConcurrency)
	if c.afterAcquire != nil {
		c.afterAcquire(accountID)
	}
	return !c.rejectAcquire, nil
}

func (c *upstreamCostTrackingConcurrencyCache) ReleaseAccountSlot(_ context.Context, accountID int64, _ string) error {
	if c.releases == nil {
		c.releases = make(map[int64]int)
	}
	c.releases[accountID]++
	return nil
}

func (c *upstreamCostTrackingConcurrencyCache) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	out := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		if load := c.loadMap[account.ID]; load != nil {
			copied := *load
			out[account.ID] = &copied
		}
	}
	return out, nil
}

func (c *upstreamCostTrackingConcurrencyCache) limits(accountID int64) []int {
	return append([]int(nil), c.acquireLimits[accountID]...)
}

func (c *upstreamCostTrackingConcurrencyCache) releaseCount(accountID int64) int {
	return c.releases[accountID]
}

func (c *upstreamCostTrackingConcurrencyCache) totalAcquires() int {
	total := 0
	for _, limits := range c.acquireLimits {
		total += len(limits)
	}
	return total
}

type upstreamCostCountingAccountRepo struct {
	AccountRepository
	accounts map[int64]*Account
	getCalls int
}

func (r *upstreamCostCountingAccountRepo) GetByID(_ context.Context, accountID int64) (*Account, error) {
	r.getCalls++
	account := r.accounts[accountID]
	if account == nil {
		return nil, errors.New("account not found")
	}
	cloned := *account
	return &cloned, nil
}

func (r *upstreamCostCountingAccountRepo) calls() int {
	return r.getCalls
}

func upstreamCostTestAccount(id int64, status string, rate float64, receivedAt time.Time, interval time.Duration) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			UpstreamBillingProbeExtraKey: map[string]any{
				"status": status,
				"data": map[string]any{
					"billing_scope":             "token",
					"resolved_rate_multiplier":  rate,
					"peak_rate_enabled":         false,
					"effective_rate_multiplier": rate,
				},
				"received_at":     receivedAt.UTC().Format(time.RFC3339Nano),
				"fresh_until":     receivedAt.Add(2 * interval).UTC().Format(time.RFC3339Nano),
				"last_attempt_at": receivedAt.UTC().Format(time.RFC3339Nano),
				"next_probe_at":   receivedAt.Add(interval).UTC().Format(time.RFC3339Nano),
			},
		},
	}
}

func upstreamCostTestOAuthAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func TestSelectionKnownFullPoolStillFindsAvailableAccount(t *testing.T) {
	accounts := make([]*Account, 0, 102)
	cache := &upstreamCostTrackingConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{}}
	for id := int64(1); id <= 102; id++ {
		accounts = append(accounts, &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1})
		if id < 102 {
			cache.loadMap[id] = &AccountLoadInfo{AccountID: id, CurrentConcurrency: 1, LoadRate: 100}
		}
	}
	svc, _ := newUpstreamCostSelectionFixture(accounts, cache)
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, int64(102), selection.Account.ID)
	require.Equal(t, 1, cache.totalAcquires())
	selection.ReleaseFunc()
}

func newUpstreamCostSelectionFixture(accounts []*Account, cache *upstreamCostTrackingConcurrencyCache) (*OpenAIGatewayService, *upstreamCostCountingAccountRepo) {
	byID := map[int64]*Account{}
	for _, a := range accounts {
		byID[a.ID] = a
	}
	repo := &upstreamCostCountingAccountRepo{accounts: byID}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: accounts, accountsByID: byID}}, concurrencyService: NewConcurrencyService(cache)}
	return svc, repo
}

func TestSelectionUnknownLoadFailsOpen(t *testing.T) {
	a := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	cache := &upstreamCostTrackingConcurrencyCache{}
	svc, _ := newUpstreamCostSelectionFixture([]*Account{a}, cache)
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []int{1}, cache.limits(a.ID))
	selection.ReleaseFunc()
}

func TestSelectionRejectsDBDisabledCandidateWithoutSlotLeak(t *testing.T) {
	stale := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	backup := *stale
	backup.ID = 32
	disabled := *stale
	disabled.Status = StatusDisabled
	cache := &upstreamCostTrackingConcurrencyCache{}
	svc, repo := newUpstreamCostSelectionFixture([]*Account{stale, &backup}, cache)
	repo.accounts = map[int64]*Account{31: &disabled, 32: &backup}
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, backup.ID, selection.Account.ID)
	require.Empty(t, cache.limits(stale.ID), "fresh state is checked before taking a slot")
	require.Zero(t, cache.releaseCount(stale.ID))
	selection.ReleaseFunc()
	require.Equal(t, cache.totalAcquires(), cache.releaseCount(backup.ID))
}

func TestSelectionUsesFreshDBConcurrency(t *testing.T) {
	stale := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10}
	latest := *stale
	latest.Concurrency = 1
	cache := &upstreamCostTrackingConcurrencyCache{}
	svc, repo := newUpstreamCostSelectionFixture([]*Account{stale}, cache)
	repo.accounts = map[int64]*Account{41: &latest}
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, []int{1}, cache.limits(stale.ID))
	require.Equal(t, 1, selection.Account.Concurrency)
	selection.ReleaseFunc()
	require.Equal(t, 1, cache.releaseCount(stale.ID))
}

func TestSelectionKnownFullPoolsWaitWithoutAcquiring(t *testing.T) {
	for _, size := range []int{100, 15000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			accounts := make([]*Account, 0, size)
			cache := &upstreamCostTrackingConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{}}
			for i := 1; i <= size; i++ {
				a := &Account{ID: int64(i), Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
				accounts = append(accounts, a)
				cache.loadMap[a.ID] = &AccountLoadInfo{AccountID: a.ID, CurrentConcurrency: 1, LoadRate: 100}
			}
			svc, repo := newUpstreamCostSelectionFixture(accounts, cache)
			selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "", nil)
			require.NoError(t, err)
			require.NotNil(t, selection.WaitPlan)
			require.Equal(t, int64(1), selection.WaitPlan.AccountID)
			require.Equal(t, 1, repo.calls(), "only the queued candidate requires a fresh check")
			require.Zero(t, cache.totalAcquires())
		})
	}
}

func TestOpenAIFreshUpstreamBillingRateRecomputesPeakAtSelectionTime(t *testing.T) {
	receivedAt := time.Date(2026, 7, 13, 17, 30, 0, 0, time.UTC)
	account := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.4, receivedAt, time.Hour)
	snapshot, ok := account.Extra[UpstreamBillingProbeExtraKey].(map[string]any)
	require.True(t, ok)
	snapshot["data"] = map[string]any{
		"billing_scope":             "token",
		"resolved_rate_multiplier":  0.4,
		"peak_rate_enabled":         true,
		"peak_start":                "09:00",
		"peak_end":                  "18:00",
		"peak_rate_multiplier":      2.0,
		"applied_peak_multiplier":   2.0,
		"effective_rate_multiplier": 0.8,
		"timezone":                  "UTC",
	}

	account.RateMultiplier = floatPtr(0.1)
	duringPeak, ok := openAISchedulingRate(account, time.Date(2026, 7, 13, 17, 59, 0, 0, time.UTC), nil)
	require.True(t, ok)
	require.Equal(t, 0.8, duringPeak)

	afterPeak, ok := openAISchedulingRate(account, time.Date(2026, 7, 13, 18, 1, 0, 0, time.UTC), nil)
	require.True(t, ok)
	require.Equal(t, 0.4, afterPeak)
}

func TestOpenAISchedulingRatePlacesOAuthAtConfiguredReference(t *testing.T) {
	now := time.Now()
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.02, now.Add(-time.Minute), 30*time.Minute)
	oauth := upstreamCostTestOAuthAccount(2)
	expensive := upstreamCostTestAccount(3, UpstreamBillingProbeStatusOK, 0.12, now.Add(-time.Minute), 30*time.Minute)

	cheapRate, cheapKnown := openAISchedulingRate(cheap, now, floatPtr(0.05))
	oauthRate, oauthKnown := openAISchedulingRate(oauth, now, floatPtr(0.05))
	expensiveRate, expensiveKnown := openAISchedulingRate(expensive, now, floatPtr(0.05))
	require.True(t, cheapKnown && oauthKnown && expensiveKnown)
	require.Less(t, cheapRate, oauthRate)
	require.Less(t, oauthRate, expensiveRate)
}

func TestOpenAIGatewayServiceSelectionUsesConfiguredOAuthReference(t *testing.T) {
	prior := openAISchedulingRateCache.Load()
	resetOpenAISchedulingRateTestCache()
	t.Cleanup(func() { resetOpenAISchedulingRateTestCache(); openAISchedulingRateCache.Store(prior) })

	now := time.Now()
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.02, now.Add(-time.Minute), 30*time.Minute)
	oauth := upstreamCostTestOAuthAccount(2)
	expensive := upstreamCostTestAccount(3, UpstreamBillingProbeStatusOK, 0.12, now.Add(-time.Minute), 30*time.Minute)
	for _, account := range []*Account{cheap, oauth, expensive} {
		account.Status = StatusActive
		account.Schedulable = true
		account.Concurrency = 1
	}
	cheap.Priority, oauth.Priority, expensive.Priority = 0, 0, 0

	settings := &selectionSettingRepoStub{values: map[string]string{
		SettingKeyOpenAIOAuthSchedulingRateMultiplier: "0.05",
	}}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{
		accountRepo:      schedulerTestOpenAIAccountRepo{accounts: []Account{*cheap, *oauth, *expensive}},
		cache:            &schedulerTestGatewayCache{},
		cfg:              cfg,
		rateLimitService: &RateLimitService{settingService: NewSettingService(settings, cfg)},
	}
	groupID := int64(1)

	first, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, cheap.ID, first.Account.ID)

	second, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-test", map[int64]struct{}{cheap.ID: {}}, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, oauth.ID, second.Account.ID)
}

func TestOpenAIModelsSelectionIgnoresTokenCostSignal(t *testing.T) {

	now := time.Now()
	cheap := upstreamCostTestAccount(51, UpstreamBillingProbeStatusOK, 0.02, now.Add(-time.Minute), 30*time.Minute)
	expensive := upstreamCostTestAccount(52, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)
	for _, account := range []*Account{cheap, expensive} {
		account.Status = StatusActive
		account.Schedulable = true
		account.Concurrency = 1
	}
	cheap.Priority = 10
	expensive.Priority = 0
	settings := &selectionSettingRepoStub{values: map[string]string{}}
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{
		accountRepo:      schedulerTestOpenAIAccountRepo{accounts: []Account{*cheap, *expensive}},
		cfg:              cfg,
		rateLimitService: &RateLimitService{settingService: NewSettingService(settings, cfg)},
	}

	account, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "", nil)
	require.NoError(t, err)
	require.Equal(t, expensive.ID, account.ID)
}

func TestOpenAIGatewayServiceSelectionSkipsCooledDownAccount(t *testing.T) {

	now := time.Now()
	cheap := upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.03, now.Add(-time.Minute), 30*time.Minute)
	cheap.Status, cheap.Schedulable, cheap.Concurrency, cheap.Priority = StatusActive, true, 1, 10
	cooldownUntil := now.Add(time.Minute)
	cheap.TempUnschedulableUntil = &cooldownUntil
	expensive := upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, 0.8, now.Add(-time.Minute), 30*time.Minute)
	expensive.Status, expensive.Schedulable, expensive.Concurrency, expensive.Priority = StatusActive, true, 1, 0
	settings := &selectionSettingRepoStub{values: map[string]string{}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &OpenAIGatewayService{
		accountRepo:      schedulerTestOpenAIAccountRepo{accounts: []Account{*cheap, *expensive}},
		cache:            &schedulerTestGatewayCache{},
		cfg:              cfg,
		rateLimitService: &RateLimitService{settingService: NewSettingService(settings, cfg)},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{
			1: {AccountID: 1},
			2: {AccountID: 2},
		}}),
	}
	groupID := int64(1)

	selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIFreshUpstreamBillingRateUsesFreshCachedSuccessOnly(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		account *Account
		wantOK  bool
	}{
		{name: "fresh", account: upstreamCostTestAccount(1, UpstreamBillingProbeStatusOK, 0.3, now.Add(-time.Minute), 30*time.Minute), wantOK: true},
		{name: "zero rate", account: upstreamCostTestAccount(2, UpstreamBillingProbeStatusOK, 0, now.Add(-time.Minute), 30*time.Minute), wantOK: true},
		{name: "transient failure with fresh cache", account: upstreamCostTestAccount(3, UpstreamBillingProbeStatusFailed, 0.3, now.Add(-time.Minute), 30*time.Minute), wantOK: true},
		{name: "stale", account: upstreamCostTestAccount(4, UpstreamBillingProbeStatusOK, 0.3, now.Add(-61*time.Minute), 30*time.Minute)},
		{name: "future", account: upstreamCostTestAccount(5, UpstreamBillingProbeStatusOK, 0.3, now.Add(time.Minute), 30*time.Minute)},
		{name: "unsupported", account: upstreamCostTestAccount(6, UpstreamBillingProbeStatusUnsupported, 0.3, now.Add(-time.Minute), 30*time.Minute)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := openAIFreshUpstreamBillingRate(tt.account, now)
			require.Equal(t, tt.wantOK, ok)
		})
	}
}
