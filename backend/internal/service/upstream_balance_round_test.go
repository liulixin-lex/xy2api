package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"

	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type upstreamBalanceRoundRepo struct {
	AccountRepository
	accounts                      []Account
	platform, accountType, status string
	listCalls                     int
	entered                       chan struct{}
	release                       chan struct{}
	listedDeadline                time.Time
}

func (r *upstreamBalanceRoundRepo) ListAllWithFilters(ctx context.Context, platform, accountType, status, search string, groupID int64, privacy string) ([]Account, error) {
	r.listCalls++
	r.platform, r.accountType, r.status = platform, accountType, status
	r.listedDeadline, _ = ctx.Deadline()
	if r.entered != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		<-r.release
	}
	return r.accounts, nil
}

type upstreamBalanceMemoryStore struct {
	UpstreamBalanceStore
	mu           sync.Mutex
	entries      *UpstreamBalanceEntries
	loads        [][]int64
	savedStates  map[int64]*UpstreamBalanceState
	savedRecords map[int64]*UpstreamBalanceRecord
}

type upstreamBalanceWriteStore struct {
	UpstreamBalanceStore
	deadline time.Time
	state    *UpstreamBalanceState
	record   *UpstreamBalanceRecord
}

type upstreamBalanceErrorStore struct {
	UpstreamBalanceStore
	loadEntries    *UpstreamBalanceEntries
	loadErr        error
	getSiteErr     error
	saveSiteErr    error
	saveKindErr    error
	deleteKindErr  error
	saveAttemptErr error
	saveSuccessErr error
	site           *UpstreamBalanceSite
	lockAcquired   bool
	lockErr        error
	lockTTL        time.Duration
	lockCallback   func()
	lockBudget     func()
}

func (s *upstreamBalanceErrorStore) Load(context.Context, []int64) (*UpstreamBalanceEntries, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if s.loadEntries != nil {
		return s.loadEntries, nil
	}
	return &UpstreamBalanceEntries{Records: map[int64]*UpstreamBalanceRecord{}, States: map[int64]*UpstreamBalanceState{}, Kinds: map[int64]*UpstreamBalanceKindRecord{}}, nil
}
func (s *upstreamBalanceErrorStore) GetSite(context.Context, string) (*UpstreamBalanceSite, error) {
	return s.site, s.getSiteErr
}
func (s *upstreamBalanceErrorStore) SaveSite(context.Context, string, *UpstreamBalanceSite, time.Duration) error {
	return s.saveSiteErr
}
func (s *upstreamBalanceErrorStore) SaveKind(context.Context, int64, *UpstreamBalanceKindRecord, time.Duration) error {
	return s.saveKindErr
}
func (s *upstreamBalanceErrorStore) DeleteKind(context.Context, int64) error { return s.deleteKindErr }
func (s *upstreamBalanceErrorStore) SaveAttempt(context.Context, int64, *UpstreamBalanceState) error {
	return s.saveAttemptErr
}
func (s *upstreamBalanceErrorStore) SaveSuccess(context.Context, int64, *UpstreamBalanceRecord, *UpstreamBalanceState) error {
	return s.saveSuccessErr
}
func (s *upstreamBalanceErrorStore) SaveCooldown(context.Context, string, time.Time) error {
	return nil
}
func (s *upstreamBalanceErrorStore) GetCooldown(context.Context, string) (*time.Time, error) {
	return nil, nil
}
func (s *upstreamBalanceErrorStore) TryLockOrigin(_ context.Context, _, _ string, ttl time.Duration) (bool, error) {
	s.lockTTL = ttl
	if s.lockBudget != nil {
		s.lockBudget()
	}
	if s.lockCallback != nil {
		s.lockCallback()
	}
	return s.lockAcquired, s.lockErr
}

func (s *upstreamBalanceWriteStore) SaveAttempt(ctx context.Context, _ int64, st *UpstreamBalanceState) error {
	s.deadline, _ = ctx.Deadline()
	s.state = st
	return nil
}
func (s *upstreamBalanceWriteStore) SaveSuccess(ctx context.Context, _ int64, rec *UpstreamBalanceRecord, st *UpstreamBalanceState) error {
	s.deadline, _ = ctx.Deadline()
	s.state, s.record = st, rec
	return nil
}

func (s *upstreamBalanceMemoryStore) Load(_ context.Context, ids []int64) (*UpstreamBalanceEntries, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads = append(s.loads, append([]int64(nil), ids...))
	if s.entries != nil {
		copyEntries := emptyUpstreamBalanceEntries()
		for id, rec := range s.entries.Records {
			copyEntries.Records[id] = rec
		}
		for id, state := range s.entries.States {
			copyEntries.States[id] = state
		}
		for id, kind := range s.entries.Kinds {
			copyEntries.Kinds[id] = kind
		}
		return copyEntries, nil
	}
	return emptyUpstreamBalanceEntries(), nil
}

func (s *upstreamBalanceMemoryStore) SaveKind(_ context.Context, id int64, k *UpstreamBalanceKindRecord, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = emptyUpstreamBalanceEntries()
	}
	s.entries.Kinds[id] = k
	return nil
}
func (s *upstreamBalanceMemoryStore) SaveAttempt(_ context.Context, id int64, st *UpstreamBalanceState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.savedStates == nil {
		s.savedStates = make(map[int64]*UpstreamBalanceState)
	}
	s.savedStates[id] = st
	if s.entries != nil {
		s.entries.States[id] = st
	}
	return nil
}
func (s *upstreamBalanceMemoryStore) SaveSuccess(_ context.Context, id int64, rec *UpstreamBalanceRecord, st *UpstreamBalanceState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.savedRecords == nil {
		s.savedRecords = make(map[int64]*UpstreamBalanceRecord)
	}
	if s.savedStates == nil {
		s.savedStates = make(map[int64]*UpstreamBalanceState)
	}
	s.savedRecords[id], s.savedStates[id] = rec, st
	if s.entries != nil {
		s.entries.States[id], s.entries.Records[id] = st, rec
	}
	return nil
}
func (s *upstreamBalanceMemoryStore) SaveSite(context.Context, string, *UpstreamBalanceSite, time.Duration) error {
	return nil
}
func (s *upstreamBalanceMemoryStore) GetSite(context.Context, string) (*UpstreamBalanceSite, error) {
	return nil, nil
}
func (s *upstreamBalanceMemoryStore) DeleteKind(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries != nil {
		delete(s.entries.Kinds, id)
	}
	return nil
}
func (s *upstreamBalanceMemoryStore) TryLockOrigin(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *upstreamBalanceMemoryStore) SaveCooldown(context.Context, string, time.Time) error {
	return nil
}
func (s *upstreamBalanceMemoryStore) GetCooldown(context.Context, string) (*time.Time, error) {
	return nil, nil
}

func emptyUpstreamBalanceEntries() *UpstreamBalanceEntries {
	return &UpstreamBalanceEntries{Records: map[int64]*UpstreamBalanceRecord{}, States: map[int64]*UpstreamBalanceState{}, Kinds: map[int64]*UpstreamBalanceKindRecord{}}
}

type upstreamBalanceTestHTTP struct {
	mu          sync.Mutex
	client      *http.Client
	proxy       string
	accountID   int64
	concurrency int
	profile     *tlsfingerprint.Profile
	req         *http.Request
}

type upstreamBalanceLeaderLock struct {
	mu        sync.Mutex
	owner     string
	key       string
	ttl       time.Duration
	tries     int
	releases  int
	onAcquire func()
}

func (l *upstreamBalanceLeaderLock) TryAcquireLeaderLock(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tries++
	l.key, l.ttl = key, ttl
	if l.owner != "" {
		return false, nil
	}
	l.owner = owner
	if l.onAcquire != nil {
		l.onAcquire()
	}
	return true, nil
}
func (l *upstreamBalanceLeaderLock) ReleaseLeaderLock(context.Context, string, string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases++
	return nil
}

func upstreamBalanceTestConfig() *config.Config {
	return &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
}
func newUpstreamBalanceTestHTTP(server *httptest.Server) *upstreamBalanceTestHTTP {
	return &upstreamBalanceTestHTTP{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (u *upstreamBalanceTestHTTP) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	u.mu.Lock()
	u.req, u.proxy, u.accountID, u.concurrency = req, proxy, id, concurrency
	u.mu.Unlock()
	return u.client.Do(req)
}
func (u *upstreamBalanceTestHTTP) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	u.profile = profile
	u.mu.Unlock()
	return u.Do(req, proxy, id, concurrency)
}

type panicUpstreamBalanceHTTP struct {
	delegate HTTPUpstream
	panicID  int64
}

func (u panicUpstreamBalanceHTTP) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	if id == u.panicID {
		panic("test worker panic")
	}
	return u.delegate.Do(req, proxy, id, concurrency)
}
func (u panicUpstreamBalanceHTTP) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if id == u.panicID {
		panic("test worker panic")
	}
	return u.delegate.DoWithTLS(req, proxy, id, concurrency, profile)
}

func upstreamBalanceCandidate(id int64, status string) Account {
	return Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: status, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://relay.example"}}
}

func TestUpstreamBalanceCandidateListing(t *testing.T) {
	accounts := []Account{
		upstreamBalanceCandidate(1, StatusActive),
		upstreamBalanceCandidate(2, StatusError),
		upstreamBalanceCandidate(3, StatusDisabled),
		upstreamBalanceCandidate(4, "deleted"),
	}
	entries := &UpstreamBalanceEntries{Records: map[int64]*UpstreamBalanceRecord{}, States: map[int64]*UpstreamBalanceState{}, Kinds: map[int64]*UpstreamBalanceKindRecord{}}
	for _, a := range accounts[:2] {
		fp := upstreamBalanceFingerprint(&a, upstreamBalanceEffectiveBaseURL(&a))
		entries.Kinds[a.ID] = &UpstreamBalanceKindRecord{Fingerprint: fp, Kind: "unsupported"}
	}
	repo := &upstreamBalanceRoundRepo{accounts: accounts}
	store := &upstreamBalanceMemoryStore{entries: entries}
	up := &upstreamBalanceTestHTTP{}
	svc := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, store, nil)
	require.NoError(t, svc.RunRound(context.Background()))
	require.Equal(t, "", repo.status)
	require.Equal(t, AccountTypeAPIKey, repo.accountType)
	require.Equal(t, []int64{1, 2}, store.loads[0])
}

func TestUpstreamBalanceRequestShape(t *testing.T) {
	var gotMethod string
	var gotAccept, gotAuth, gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotAccept, gotAuth, gotCustom = r.Method, r.Header.Get("Accept"), r.Header.Get("Authorization"), r.Header.Get("X-Probe")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	up := newUpstreamBalanceTestHTTP(server)
	a := upstreamBalanceCandidate(8, StatusActive)
	a.Concurrency = 3
	a.Credentials["header_override_enabled"] = true
	a.Credentials["header_overrides"] = map[string]any{"Accept": "text/plain", "Authorization": "override", "X-Probe": "set"}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, nil, nil)
	resp, body, err := svc.ubRequest(context.Background(), &a, server.URL, "Bearer authoritative", 128)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"ok":true}`, string(body))
	require.Equal(t, http.MethodGet, gotMethod)
	require.Equal(t, "text/plain", gotAccept)
	require.Equal(t, "Bearer authoritative", gotAuth)
	require.Equal(t, "set", gotCustom)
	require.True(t, HTTPUpstreamSingleAttempt(up.req.Context()))
	require.True(t, HTTPUpstreamRedirectsDisabled(up.req.Context()))
	require.Equal(t, HTTPUpstreamProfileDefault, HTTPUpstreamProfileFromContext(up.req.Context()))
	require.Equal(t, int64(8), up.accountID)
	require.Equal(t, 3, up.concurrency)
}

func TestUpstreamBalanceRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	up := newUpstreamBalanceTestHTTP(server)
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, nil, nil)
	_, _, err := svc.ubRequest(context.Background(), ptrAccount(upstreamBalanceCandidate(9, StatusActive)), server.URL, "", 10)
	require.NoError(t, err)
	deadline, ok := up.req.Context().Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(upstreamBalanceRequestTimeout), deadline, 100*time.Millisecond)
}

func TestUpstreamBalanceRejectsOversizedRedirectAndMalformed(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/other", http.StatusFound) }))
		defer server.Close()
		up := newUpstreamBalanceTestHTTP(server)
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, nil, nil)
		resp, _, err := svc.ubRequest(context.Background(), ptrAccount(upstreamBalanceCandidate(10, StatusActive)), server.URL, "Bearer x", 20)
		require.Error(t, err)
		require.Equal(t, http.StatusFound, resp.StatusCode)
		require.True(t, HTTPUpstreamRedirectsDisabled(up.req.Context()))
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 65*1024)) }))
		defer server.Close()
		up := newUpstreamBalanceTestHTTP(server)
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, nil, nil)
		_, _, err := svc.ubRequest(context.Background(), ptrAccount(upstreamBalanceCandidate(11, StatusActive)), server.URL, "", upstreamBalanceMaxBodyBytes)
		require.Error(t, err)
	})
}

func TestUpstreamBalanceStatusBodyLimit(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Repeat("s", upstreamBalanceStatusMaxBodyBytes+1)))}
	_, err := readUBBody(resp, upstreamBalanceStatusMaxBodyBytes)
	require.Error(t, err)
}

func TestUpstreamBalanceNilResponse(t *testing.T) {
	_, err := readUBBody(nil, upstreamBalanceMaxBodyBytes)
	require.Error(t, err)
	_, err = readUBBody(&http.Response{}, upstreamBalanceMaxBodyBytes)
	require.Error(t, err)
}

func TestUpstreamBalanceProxyMissingNeverGoesDirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	up := newUpstreamBalanceTestHTTP(server)
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, nil, nil)
	a := upstreamBalanceCandidate(12, StatusActive)
	proxyID := int64(5)
	a.ProxyID = &proxyID
	_, _, err := svc.ubRequest(context.Background(), &a, server.URL, "", 10)
	require.Error(t, err)
	require.Nil(t, up.req)
}

func TestUpstreamBalanceOpenAIProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	up := newUpstreamBalanceTestHTTP(server)
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, nil, nil)
	a := upstreamBalanceCandidate(13, StatusActive)
	a.Platform = PlatformOpenAI
	_, _, err := svc.ubRequest(context.Background(), &a, server.URL, "", 10)
	require.NoError(t, err)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(up.req.Context()))
}

func TestUpstreamBalanceRequestTLSProfilePropagation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	up := newUpstreamBalanceTestHTTP(server)
	a := upstreamBalanceCandidate(14, StatusActive)
	a.Type = AccountTypeOAuth
	a.Extra = map[string]any{"enable_tls_fingerprint": true}
	svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up, tlsFPProfileService: &TLSFingerprintProfileService{}}, nil, nil)
	_, _, err := svc.ubRequest(context.Background(), &a, server.URL, "", 10)
	require.NoError(t, err)
	require.NotNil(t, up.profile)
	require.Equal(t, "Built-in Default (Node.js 24.x)", up.profile.Name)
}

func TestUpstreamBalanceSingleRoundAcrossInstances(t *testing.T) {
	lock := &upstreamBalanceLeaderLock{}
	store := &upstreamBalanceMemoryStore{}
	repo := &upstreamBalanceRoundRepo{}
	httpUpstream := &upstreamBalanceTestHTTP{}
	serviceA := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: httpUpstream}, store, lock)
	serviceB := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: httpUpstream}, store, lock)
	require.NoError(t, serviceA.RunRound(context.Background()))
	require.NoError(t, serviceB.RunRound(context.Background()))
	require.Equal(t, upstreamBalanceRoundLockKey, lock.key)
	require.Equal(t, upstreamBalanceRoundLockTTL, lock.ttl)
	require.Equal(t, 2, lock.tries)
	require.Zero(t, lock.releases)
	require.Len(t, store.loads, 1)
}

func TestUpstreamBalanceRoundMutexPreventsOverlap(t *testing.T) {
	repo := &upstreamBalanceRoundRepo{entered: make(chan struct{}, 1), release: make(chan struct{})}
	store := &upstreamBalanceMemoryStore{}
	svc := NewUpstreamBalanceService(repo, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: &upstreamBalanceTestHTTP{}}, store, nil)
	done := make(chan error, 1)
	go func() { done <- svc.RunRound(context.Background()) }()
	<-repo.entered
	require.NoError(t, svc.RunRound(context.Background()))
	repo.release <- struct{}{}
	require.NoError(t, <-done)
	require.Equal(t, 1, repo.listCalls)
}

func TestUpstreamBalanceCadenceAnchoredToRoundStart(t *testing.T) {
	roundStart := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	completed := roundStart.Add(11*time.Second + 780*time.Millisecond)
	store := &upstreamBalanceWriteStore{}
	svc := NewUpstreamBalanceService(nil, nil, store, nil)
	svc.now = func() time.Time { return completed }
	st := &UpstreamBalanceState{Version: 1, Fingerprint: "fp"}
	svc.persistOutcome(context.Background(), roundStart, 22, st, ubOutcome{success: true, amount: ubTestFloatPtr(2), kind: "sub2api"})
	require.Equal(t, roundStart.Add(time.Minute), st.NextDueAt)
	require.Equal(t, completed.Truncate(time.Second), st.LastAttemptAt)
	require.Equal(t, completed.Truncate(time.Second), store.record.ObservedAt)
}

func TestUpstreamBalanceDueSlack(t *testing.T) {
	start := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	st := &UpstreamBalanceState{Fingerprint: "fp", NextDueAt: start.Add(time.Minute)}
	require.False(t, upstreamBalanceDue(st, "fp", start.Add(54*time.Second), start.Add(54*time.Second)))
	require.True(t, upstreamBalanceDue(st, "fp", start.Add(55*time.Second), start.Add(55*time.Second)))
	require.True(t, upstreamBalanceDue(nil, "fp", start, start))
}

func TestUpstreamBalanceRetryNotBeforeHasNoDueSlack(t *testing.T) {
	start := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	retryAt := start.Add(2 * time.Minute)
	st := &UpstreamBalanceState{Fingerprint: "fp", NextDueAt: start, RetryNotBefore: &retryAt}
	require.False(t, upstreamBalanceDue(st, "fp", start.Add(2*time.Minute-time.Second), start.Add(2*time.Minute-time.Second)))
	require.True(t, upstreamBalanceDue(st, "fp", start.Add(2*time.Minute), start.Add(2*time.Minute)))
}

func TestUpstreamBalanceStoreTimeout(t *testing.T) {
	now := time.Now()
	store := &upstreamBalanceWriteStore{}
	svc := NewUpstreamBalanceService(nil, nil, store, nil)
	svc.now = func() time.Time { return now }
	svc.persistOutcome(context.Background(), now, 23, &UpstreamBalanceState{Fingerprint: "fp"}, ubOutcome{reason: "upstream_error"})
	require.WithinDuration(t, time.Now().Add(upstreamBalanceStoreTimeout), store.deadline, 100*time.Millisecond)
}

func TestUpstreamBalanceStoreErrorDropsOutcome(t *testing.T) {
	t.Run("get site", func(t *testing.T) {
		memo := &upstreamBalanceRoundMemo{}
		store := &upstreamBalanceErrorStore{getSiteErr: context.DeadlineExceeded}
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: &upstreamBalanceTestHTTP{}}, store, nil)
		out := svc.collectNewAPI(context.Background(), ptrAccount(upstreamBalanceCandidate(31, StatusActive)), "https://relay.example", &UpstreamBalanceState{}, memo)
		require.Equal(t, "redis_unavailable", out.reason)
		require.True(t, memo.failed())
	})
	t.Run("save site", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"success":true,"data":{"quota_per_unit":1}}`)
		}))
		defer server.Close()
		memo := &upstreamBalanceRoundMemo{}
		store := &upstreamBalanceErrorStore{saveSiteErr: context.DeadlineExceeded}
		up := newUpstreamBalanceTestHTTP(server)
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, store, nil)
		out := svc.collectNewAPI(context.Background(), ptrAccount(upstreamBalanceCandidate(32, StatusActive)), server.URL, &UpstreamBalanceState{}, memo)
		require.Equal(t, "redis_unavailable", out.reason)
		require.True(t, memo.failed())
	})
	t.Run("save kind", func(t *testing.T) {
		memo := &upstreamBalanceRoundMemo{}
		store := &upstreamBalanceErrorStore{saveKindErr: context.DeadlineExceeded}
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: &upstreamBalanceTestHTTP{}}, store, nil)
		a := upstreamBalanceCandidate(33, StatusActive)
		a.Credentials["newapi_access_token"] = "configured"
		svc.pollOne(context.Background(), time.Now(), a.ID, &a, nil, memo)
		require.True(t, memo.failed())
	})
	t.Run("delete kind", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "subscription") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		memo := &upstreamBalanceRoundMemo{}
		store := &upstreamBalanceErrorStore{site: &UpstreamBalanceSite{QuotaPerUnit: 1, DisplayType: "USD"}, deleteKindErr: context.DeadlineExceeded}
		up := newUpstreamBalanceTestHTTP(server)
		svc := NewUpstreamBalanceService(nil, &AccountTestService{cfg: upstreamBalanceTestConfig(), httpUpstream: up}, store, nil)
		a := upstreamBalanceCandidate(34, StatusActive)
		a.Credentials["base_url"] = server.URL
		base, _ := upstreamBalanceApplicable(&a)
		st := &UpstreamBalanceState{Fingerprint: upstreamBalanceFingerprint(&a, base)}
		svc.pollKnownNewAPI(context.Background(), time.Now(), a.ID, &a, base, st, memo, false)
		require.True(t, memo.failed())
	})
	t.Run("save attempt and success", func(t *testing.T) {
		for _, success := range []bool{false, true} {
			memo := &upstreamBalanceRoundMemo{}
			store := &upstreamBalanceErrorStore{saveAttemptErr: context.DeadlineExceeded, saveSuccessErr: context.DeadlineExceeded}
			svc := NewUpstreamBalanceService(nil, nil, store, nil)
			out := ubOutcome{reason: "upstream_error"}
			if success {
				out = ubOutcome{success: true, amount: ubTestFloatPtr(1), kind: "sub2api"}
			}
			svc.persistOutcome(context.Background(), time.Now(), 35, &UpstreamBalanceState{Fingerprint: "fp"}, out, memo)
			require.True(t, memo.failed())
		}
	})
}

func ubTestFloatPtr(v float64) *float64 { return &v }

func ptrAccount(a Account) *Account { return &a }
