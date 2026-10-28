package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type upstreamBalanceReadRepo struct {
	AccountRepository
	mu       sync.Mutex
	accounts []*Account
	reads    int
	writes   int
	outbox   int
}

func (r *upstreamBalanceReadRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.reads++
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	var result []*Account
	for _, a := range r.accounts {
		if _, ok := wanted[a.ID]; ok {
			result = append(result, a)
		}
	}
	return result, nil
}

func (r *upstreamBalanceReadRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	result := make([]Account, 0, len(r.accounts))
	for _, a := range r.accounts {
		result = append(result, *a)
	}
	return result, nil
}

// Mutators record both the account write and its scheduler outbox enqueue.
func (r *upstreamBalanceReadRepo) Create(ctx context.Context, account *Account) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) Update(ctx context.Context, account *Account) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) Delete(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) UpdateLastUsed(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) BatchUpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) ClearError(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) SetSchedulable(ctx context.Context, id int64, schedulable bool) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) AutoPauseExpiredAccounts(ctx context.Context, now time.Time) (int64, error) {
	r.recordWrite()
	return 0, nil
}
func (r *upstreamBalanceReadRepo) BindGroups(ctx context.Context, accountID int64, groupIDs []int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) SetModelRateLimit(ctx context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) SetOverloaded(ctx context.Context, id int64, until time.Time) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) ClearRateLimit(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) UpdateSessionWindow(ctx context.Context, id int64, start, end *time.Time, status string) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) UpdateSessionWindowEnd(ctx context.Context, id int64, end time.Time) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	r.recordWrite()
	return 0, nil
}
func (r *upstreamBalanceReadRepo) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) ResetQuotaUsedAndClearRateLimitCooldown(ctx context.Context, id int64) error {
	r.recordWrite()
	return nil
}
func (r *upstreamBalanceReadRepo) RevertProxyFallback(ctx context.Context, accountID int64) error {
	r.recordWrite()
	return nil
}

func TestUpstreamBalanceReadPathMakesNoUpstreamCallsOrAccountWrites(t *testing.T) {
	a := upstreamBalanceCandidate(1, StatusActive)
	b := upstreamBalanceCandidate(2, StatusError)
	repo := &upstreamBalanceReadRepo{accounts: []*Account{&a, &b}}
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	upstream := &upstreamBalanceTestHTTP{}
	svc := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
	items, err := svc.ReadBalances(context.Background(), []int64{2, 1, 2, 999, 1})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, int64(2), items[0].AccountID)
	require.Equal(t, int64(1), items[1].AccountID)
	require.Equal(t, 1, repo.reads)
	require.Equal(t, 0, repo.writes)
	require.Nil(t, upstream.req)
	require.Zero(t, repo.outboxEnqueues())
}

func TestUpstreamBalanceReadStateSemantics(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	accounts := []*Account{
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key", "base_url": "https://one.example"}},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key", "base_url": "https://two.example"}},
		{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key", "base_url": "https://three.example"}},
		{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key", "base_url": "https://four.example"}},
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}
	entries := emptyUpstreamBalanceEntries()
	for i := range accounts {
		if accounts[i].ID == 5 {
			continue
		}
		base, applicable := upstreamBalanceApplicable(accounts[i])
		require.True(t, applicable)
		fp := upstreamBalanceFingerprint(accounts[i], base)
		observed := now.Add(-time.Minute)
		switch accounts[i].ID {
		case 1:
			amount := 1.25
			entries.Records[1] = &UpstreamBalanceRecord{Fingerprint: fp, Kind: "sub2api", AmountUSD: &amount, Basis: "wallet", ObservedAt: observed}
			entries.States[1] = &UpstreamBalanceState{Fingerprint: fp, LastAttemptAt: now, LastReason: "upstream_error"}
		case 2:
			amount := 2.5
			entries.Records[2] = &UpstreamBalanceRecord{Fingerprint: "old-fingerprint", Kind: "sub2api", AmountUSD: &amount, ObservedAt: observed}
			entries.States[2] = &UpstreamBalanceState{Fingerprint: "old-fingerprint", LastAttemptAt: now, LastReason: "auth_failed"}
			entries.Kinds[2] = &UpstreamBalanceKindRecord{Fingerprint: "old-fingerprint", Kind: "unsupported"}
		case 3:
			entries.States[3] = &UpstreamBalanceState{Fingerprint: fp, LastAttemptAt: now, LastReason: "no_data"}
			entries.Kinds[3] = &UpstreamBalanceKindRecord{Fingerprint: fp, Kind: "newapi"}
		}
	}
	repo := &upstreamBalanceReadRepo{accounts: accounts}
	svc := NewUpstreamBalanceService(repo, nil, &upstreamBalanceMemoryStore{entries: entries}, nil)
	svc.now = func() time.Time { return now }
	items, err := svc.ReadBalances(context.Background(), []int64{1, 2, 3, 4, 5})
	require.NoError(t, err)
	require.Len(t, items, 5)
	require.Equal(t, UpstreamBalanceItem{AccountID: 1, State: "ok", AmountUSD: entries.Records[1].AmountUSD, Basis: ubStrPtr("wallet"), UpstreamKind: ubStrPtr("sub2api"), Reason: ubStrPtr("upstream_error"), ObservedAt: ubTimePtr(now.Add(-time.Minute)), LastAttemptAt: ubTimePtr(now)}, items[0])
	require.Equal(t, UpstreamBalanceItem{AccountID: 5, State: "not_applicable"}, items[4])
	require.Equal(t, "ok", items[0].State)
	require.Equal(t, 1.25, *items[0].AmountUSD)
	require.Equal(t, "upstream_error", *items[0].Reason)
	require.Equal(t, "unavailable", items[1].State)
	require.Nil(t, items[1].Reason, "fingerprint mismatch hides state, kind and record")
	require.Nil(t, items[1].UpstreamKind)
	require.Equal(t, "unavailable", items[2].State)
	require.Equal(t, "no_data", *items[2].Reason)
	require.Equal(t, "newapi", *items[2].UpstreamKind)
	require.Equal(t, "unavailable", items[3].State)
	require.Nil(t, items[3].Reason)

	entries.Records[4] = &UpstreamBalanceRecord{Fingerprint: upstreamBalanceFingerprint(accounts[3], "https://four.example"), Kind: "sub2api", ObservedAt: now.Add(-24 * time.Hour)}
	items, err = svc.ReadBalances(context.Background(), []int64{4})
	require.NoError(t, err)
	require.Equal(t, "unavailable", items[0].State)
	require.Equal(t, UpstreamBalanceItem{AccountID: 4, State: "unavailable"}, items[0])
	entries.States[1].LastAttemptAt = now.Add(-2 * time.Minute)
	items, err = svc.ReadBalances(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Nil(t, items[0].Reason, "a failure before the last success is hidden")
	require.Equal(t, now.Add(-time.Minute), *items[0].LastAttemptAt)
	entries.States[1].LastAttemptAt, entries.States[1].LastReason = now, "no_data"
	items, err = svc.ReadBalances(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, "ok", items[0].State)
	require.Equal(t, "no_data", *items[0].Reason)
	entries.Records[1].AmountUSD, entries.Records[1].Unlimited = nil, true
	items, err = svc.ReadBalances(context.Background(), []int64{1})
	require.NoError(t, err)
	require.True(t, items[0].Unlimited)
	require.Nil(t, items[0].AmountUSD)
}

func TestUpstreamBalanceRedisDownPausesAndReadReportsUnavailable(t *testing.T) {
	a := upstreamBalanceCandidate(1, StatusActive)
	repo := &upstreamBalanceReadRepo{accounts: []*Account{&a}}
	svc := NewUpstreamBalanceService(repo, nil, &upstreamBalanceErrorStore{loadErr: errors.New("redis down")}, nil)
	items, err := svc.ReadBalances(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Equal(t, "unavailable", items[0].State)
	require.Equal(t, "redis_unavailable", *items[0].Reason)
}

func TestUpstreamBalanceFingerprintChangeHidesOldValue(t *testing.T) {
	for _, edit := range []string{"proxy", "header"} {
		t.Run(edit, func(t *testing.T) {
			a := upstreamBalanceCandidate(17, StatusActive)
			proxyID := int64(4)
			a.ProxyID = &proxyID
			a.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 8080}
			a.Credentials["header_override_enabled"] = true
			a.Credentials["header_overrides"] = map[string]any{"x-region": "one"}
			fp := upstreamBalanceFingerprint(&a, upstreamBalanceEffectiveBaseURL(&a))
			entries := emptyUpstreamBalanceEntries()
			amount := 4.5
			entries.Records[a.ID] = &UpstreamBalanceRecord{Fingerprint: fp, Kind: "sub2api", AmountUSD: &amount, ObservedAt: time.Now().UTC()}
			entries.States[a.ID] = &UpstreamBalanceState{Fingerprint: fp, LastReason: "auth_failed", LastAttemptAt: time.Now().UTC(), TokenShortcutRejected: true}
			entries.Kinds[a.ID] = &UpstreamBalanceKindRecord{Fingerprint: fp, Kind: "unsupported"}
			svc := NewUpstreamBalanceService(&upstreamBalanceReadRepo{accounts: []*Account{&a}}, nil, &upstreamBalanceMemoryStore{entries: entries}, nil)
			before, err := svc.ReadBalances(context.Background(), []int64{a.ID})
			require.NoError(t, err)
			require.Equal(t, "ok", before[0].State)
			if edit == "proxy" {
				a.Proxy.Host = "edited-proxy.example"
			} else {
				a.Credentials["header_overrides"] = map[string]any{"x-region": "two"}
			}
			after, err := svc.ReadBalances(context.Background(), []int64{a.ID})
			require.NoError(t, err)
			require.Equal(t, UpstreamBalanceItem{AccountID: a.ID, State: "unavailable"}, after[0])
			require.Nil(t, svc.kindForAccount(context.Background(), a.ID, upstreamBalanceFingerprint(&a, upstreamBalanceEffectiveBaseURL(&a)), &upstreamBalanceRoundMemo{}))
			// A matching state must not revive a mismatched unsupported kind.
			entries.States[a.ID] = &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, upstreamBalanceEffectiveBaseURL(&a)), LastReason: "no_data", LastAttemptAt: time.Now().UTC()}
			after, err = svc.ReadBalances(context.Background(), []int64{a.ID})
			require.NoError(t, err)
			require.Equal(t, "no_data", *after[0].Reason)
			require.Nil(t, after[0].UpstreamKind)
		})
	}
}

func TestUpstreamBalanceNilDependencies(t *testing.T) {
	var absent *UpstreamBalanceService
	require.NoError(t, absent.RunRound(context.Background()))
	repo := &upstreamBalanceRoundRepo{}
	ats := &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: &upstreamBalanceTestHTTP{}}
	store := &upstreamBalanceMemoryStore{}
	for _, svc := range []*UpstreamBalanceService{NewUpstreamBalanceService(nil, ats, store, nil), NewUpstreamBalanceService(repo, nil, store, nil), NewUpstreamBalanceService(repo, ats, nil, nil), NewUpstreamBalanceService(repo, &AccountTestService{}, store, nil)} {
		require.NoError(t, svc.RunRound(context.Background()))
	}
	require.Zero(t, repo.listCalls)
	require.Empty(t, store.loads)
}

func TestUpstreamBalanceRunRoundNeverWritesAccounts(t *testing.T) {
	a := upstreamBalanceCandidate(12, StatusActive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"unrestricted","isValid":true,"balance":1.25}`)
	}))
	defer server.Close()
	a.Credentials["base_url"] = server.URL
	repo := &upstreamBalanceReadRepo{accounts: []*Account{&a}}
	store := &upstreamBalanceMemoryStore{entries: emptyUpstreamBalanceEntries()}
	upstream := newUpstreamBalanceTestHTTP(server)
	svc := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: upstream}, store, nil)
	require.NoError(t, svc.RunRound(context.Background()))
	require.NotNil(t, upstream.req)
	require.Contains(t, store.savedRecords, a.ID)
	require.Equal(t, 0, repo.writes)
	require.Equal(t, 0, repo.outboxEnqueues())
}

func (r *upstreamBalanceReadRepo) outboxEnqueues() int { return r.outbox }

func (r *upstreamBalanceReadRepo) recordWrite() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	r.outbox++
}
