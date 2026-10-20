//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// shareMemoryRepo is an in-memory policy repository with the CAS semantics
// of the SQL one.
type shareMemoryRepo struct {
	mu    sync.Mutex
	rows  map[int64]ShareSchedulingPolicyRow
	usage map[int64]ShareSchedulingUsage
}

func (r *shareMemoryRepo) ListPolicies(context.Context) ([]ShareSchedulingPolicyRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ShareSchedulingPolicyRow, 0, len(r.rows))
	for _, row := range r.rows {
		out = append(out, row)
	}
	return out, nil
}

func (r *shareMemoryRepo) GetPolicy(_ context.Context, groupID int64) (*ShareSchedulingPolicyRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[groupID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (r *shareMemoryRepo) SavePolicy(_ context.Context, groupID, expectedVersion int64, scheduler string, policy []byte, updatedBy int64) (*ShareSchedulingPolicyRow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rows == nil {
		r.rows = map[int64]ShareSchedulingPolicyRow{}
	}
	current, exists := r.rows[groupID]
	if (expectedVersion <= 0 && exists) || (expectedVersion > 0 && (!exists || current.Version != expectedVersion)) {
		return nil, ErrShareSchedulingVersionConflict
	}
	by := updatedBy
	row := ShareSchedulingPolicyRow{GroupID: groupID, Version: current.Version + 1, Scheduler: scheduler, Policy: policy,
		UpdatedBy: &by, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	r.rows[groupID] = row
	return &row, nil
}

func (r *shareMemoryRepo) AccountUsage(context.Context, int64, time.Time) (map[int64]ShareSchedulingUsage, error) {
	return r.usage, nil
}

// The memory store keeps counters, decisions and broadcasts; bindings are
// read from the test gateway cache.
type shareAdminStore struct {
	shareMemoryStore
	mu        sync.Mutex
	counters  map[ShareSchedulingCounter]int64
	decisions map[int64][][]byte
	published []int64
	cache     *shareTestCache
}

func (m *shareAdminStore) PublishChange(_ context.Context, groupID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, groupID)
	return nil
}

func (m *shareAdminStore) WriteStats(_ context.Context, batch ShareSchedulingStatsBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counters == nil {
		m.counters, m.decisions = map[ShareSchedulingCounter]int64{}, map[int64][][]byte{}
	}
	for k, v := range batch.Counters {
		m.counters[k] += v
	}
	for gid, samples := range batch.Decisions {
		m.decisions[gid] = append(samples, m.decisions[gid]...)
	}
	return nil
}

func (m *shareAdminStore) ReadCounters(_ context.Context, kind string, groupID int64, from, to time.Time) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for k, v := range m.counters {
		if k.Kind == kind && k.GroupID == groupID && k.Minute >= from.Unix()/60 && k.Minute <= to.Unix()/60 {
			out[k.Field] += v
		}
	}
	return out, nil
}

func (m *shareAdminStore) RecentDecisions(_ context.Context, groupID int64, limit int) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	samples := m.decisions[groupID]
	return samples[:min(limit, len(samples))], nil
}

func (m *shareAdminStore) ClearBindings(_ context.Context, _ int64, clear func(ShareSchedulingBinding) bool) (int, error) {
	m.cache.mu.Lock()
	defer m.cache.mu.Unlock()
	deleted := 0
	for key, id := range m.cache.sessionBindings {
		if !IsShareRebalanceableBinding(key) {
			continue
		}
		if clear(ShareSchedulingBinding{AccountID: id, Remaining: m.cache.remaining[key]}) {
			delete(m.cache.sessionBindings, key)
			deleted++
		}
	}
	return deleted, nil
}

type shareMemorySettings struct {
	SettingRepository
	values map[string]string
}

func (s *shareMemorySettings) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (s *shareMemorySettings) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

type shareAdminAccountRepo struct{ *mockAccountRepoForPlatform }

func (r shareAdminAccountRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

type shareAdminGroupRepo struct{ *mockGroupRepoForGateway }

func (r shareAdminGroupRepo) ListActive(context.Context) ([]Group, error) {
	out := make([]Group, 0, len(r.groups))
	for _, g := range r.groups {
		out = append(out, *g)
	}
	return out, nil
}

type shareAdminEnv struct {
	*shareTestEnv
	policies *shareMemoryRepo
	store    *shareAdminStore
	settings *shareMemorySettings
}

func newShareAdminEnv(t *testing.T, accounts ...Account) *shareAdminEnv {
	t.Helper()
	env := newShareTestEnv(t, twoAccountPolicy(), accounts...)
	repo := &shareMemoryRepo{}
	store := &shareAdminStore{cache: env.cache}
	settings := &shareMemorySettings{values: map[string]string{}}
	env.share.repo, env.share.store, env.share.settings = repo, store, settings
	// An empty persistent repository has no applied policy yet. Keep the
	// fixture consistent so monotonic snapshot publication can be exercised.
	state := env.share.state.Load()
	env.share.state.Store(&shareSchedulingState{settings: state.settings, groups: map[int64]*shareGroupPolicy{}})
	env.gateway.accountRepo = shareAdminAccountRepo{env.repo}
	env.gateway.groupRepo = shareAdminGroupRepo{env.gateway.groupRepo.(*mockGroupRepoForGateway)}
	env.share.attachGateways(env.gateway, nil)
	env.gateway.groupRepo.(shareAdminGroupRepo).groups[shareTestGroupID].Name = "share-group"
	return &shareAdminEnv{shareTestEnv: env, policies: repo, store: store, settings: settings}
}

func (e *shareAdminEnv) save(t *testing.T, expected int64, policy string) (*ShareSchedulingPolicyView, error) {
	t.Helper()
	return e.share.SavePolicy(context.Background(), shareTestGroupID, expected, []byte(policy), 9)
}

const shareAdminPolicy = `{"scheduler":"share","accounts":[
	{"account_id":1,"tier":1,"weight":3},{"account_id":2,"tier":1,"weight":1},{"account_id":3,"tier":2,"weight":1}]}`

func TestShareAdminSavePolicyValidatesCASAndBroadcasts(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})

	_, err := env.save(t, 0, `{"scheduler":"share","unknown":1}`)
	require.True(t, infraerrors.IsBadRequest(err), "unknown fields are rejected: %v", err)

	_, err = env.save(t, 0, `{"scheduler":"share","affinity":{"key":"sticky"}}`)
	require.True(t, infraerrors.IsBadRequest(err))
	var appErr *infraerrors.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.Contains(t, appErr.Metadata["problems"], "affinity.key")

	view, err := env.save(t, 0, shareAdminPolicy)
	require.NoError(t, err)
	require.Equal(t, int64(1), view.Version)
	require.Equal(t, "share", view.Scheduler)
	require.Equal(t, int64(9), *view.UpdatedBy)
	require.Equal(t, []int64{shareTestGroupID}, env.store.published)
	require.Equal(t, int64(1), env.share.groupPolicy(shareTestGroupID).version, "the saving instance reloads at once")

	_, err = env.save(t, 0, shareAdminPolicy)
	require.ErrorIs(t, err, ErrShareSchedulingVersionConflict)
	view, err = env.save(t, 1, strings.Replace(shareAdminPolicy, `"weight":3`, `"weight":2`, 1))
	require.NoError(t, err)
	require.Equal(t, int64(2), view.Version)
}

// TestShareAdminSaveKeepsStoredRules: a save changes the scheduler and
// session keeping only; the stored rules and parameters stay for a rollback
// (design §3.2).
func TestShareAdminSaveKeepsStoredRules(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	stored := `{"schema":1,"scheduler":"shadow","accounts":[{"account_id":1,"tier":2,"weight":33}],` +
		`"defaults":{"tier":"account_priority","weight":"equal"},"affinity":{"enabled":true,"key":"auto","ttl_seconds":3600}}`
	env.policies.rows = map[int64]ShareSchedulingPolicyRow{shareTestGroupID: {GroupID: shareTestGroupID, Version: 4,
		Scheduler: "shadow", Policy: []byte(stored), UpdatedAt: time.Now()}}

	view, err := env.save(t, 4, `{"schema":1,"scheduler":"share","accounts":[{"account_id":2,"tier":1,"weight":1}],"affinity":{"key":"none"}}`)
	require.NoError(t, err)
	require.Equal(t, "share", view.Scheduler)
	saved := env.policies.rows[shareTestGroupID]
	require.Equal(t, "share", saved.Scheduler)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(saved.Policy, &doc))
	require.JSONEq(t, `[{"account_id":1,"tier":2,"weight":33}]`, string(doc["accounts"]), "submitted rules are ignored, stored ones kept")
	require.JSONEq(t, `{"tier":"account_priority","weight":"equal"}`, string(doc["defaults"]))
	require.JSONEq(t, `{"enabled":true,"key":"none","ttl_seconds":3600}`, string(doc["affinity"]))

	env.policies.rows[shareTestGroupID] = ShareSchedulingPolicyRow{GroupID: shareTestGroupID, Version: 5, Scheduler: "shadow",
		Policy: []byte(stored), UpdatedAt: time.Now()}
	view, err = env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.Equal(t, "sub2api", view.Scheduler, "a stored shadow policy is shown as the original scheduler")
	groups, err := env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.Equal(t, "sub2api", groups[0].Scheduler)

	view, err = env.save(t, 5, `{"schema":1,"scheduler":"shadow"}`)
	require.NoError(t, err)
	require.Equal(t, "sub2api", env.policies.rows[shareTestGroupID].Scheduler, "a submitted shadow is written as the original scheduler")
	require.Equal(t, "sub2api", view.Scheduler)
}

func TestShareAdminMemberSharesFollowTiersAndHealth(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})
	_, err := env.save(t, 0, shareAdminPolicy)
	require.NoError(t, err)

	// Priority 2 for account 3: tiers are priorities, weights load factors.
	accountIn(env.repo.accounts, 3).Priority = 2

	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.NotNil(t, view.ActiveTier)
	require.Equal(t, 1, *view.ActiveTier)
	shares := map[int64]ShareSchedulingMemberView{}
	for _, m := range view.Members {
		shares[m.AccountID] = m
	}
	require.InDelta(t, 0.75, shares[1].TargetShare, 1e-9)
	require.InDelta(t, 0.25, shares[2].EffectiveShare, 1e-9)
	require.InDelta(t, 1, shares[3].TargetShare, 1e-9, "tier 2 shares are within tier 2")
	require.Zero(t, shares[3].EffectiveShare, "an overflow tier takes no new sessions while tier 1 is healthy")

	resetAt := time.Now().Add(time.Hour)
	for i := range env.repo.accounts {
		if env.repo.accounts[i].ID == 1 {
			env.repo.accounts[i].RateLimitResetAt = &resetAt
		}
	}
	view, err = env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	for _, m := range view.Members {
		switch m.AccountID {
		case 1:
			require.False(t, m.Healthy)
			require.Equal(t, string(sharesched.ReasonRateLimited), m.Reason)
			require.NotNil(t, m.RecoverAt)
		case 2:
			require.InDelta(t, 1, m.EffectiveShare, 1e-9)
		}
	}
}

func TestShareAdminInvalidStoredPolicyShowsRunningDefaults(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	env.policies.rows = map[int64]ShareSchedulingPolicyRow{shareTestGroupID: {GroupID: shareTestGroupID, Version: 4,
		Scheduler: "share", Policy: []byte(`{"scheduler":"share","retired":true}`), UpdatedAt: time.Now()}}

	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.NotEmpty(t, view.Invalid)
	require.Equal(t, "sub2api", view.Scheduler, "the request path skips an undecodable row")
	require.Equal(t, int64(4), view.Version, "saving must still replace the stored row")
	_, err = sharesched.DecodePolicy(view.Policy)
	require.NoError(t, err, "the editor receives a decodable policy")

	groups, err := env.share.ListGroups(context.Background())
	require.NoError(t, err)
	for _, g := range groups {
		if g.GroupID == shareTestGroupID {
			require.True(t, g.Invalid)
			require.Equal(t, "sub2api", g.Scheduler)
		}
	}

	_, err = env.save(t, 4, `{"scheduler":"share"}`)
	require.NoError(t, err, "saving replaces an undecodable row")
	_, err = sharesched.DecodePolicy(env.policies.rows[shareTestGroupID].Policy)
	require.NoError(t, err)
}

func TestShareAdminActiveTierHandlesNegativeTiers(t *testing.T) {
	resetAt := time.Now().Add(time.Hour)
	accounts := []Account{
		{ID: 1, Priority: -5, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 2, Priority: -9, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &resetAt},
		{ID: 3, Priority: 0, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	views, active := shareMemberViews(accounts, time.Now())
	require.NotNil(t, active)
	require.Equal(t, -5, *active, "the lowest tier with a healthy open member wins, even below zero")
	for _, v := range views {
		if v.AccountID == 1 {
			require.InDelta(t, 1, v.EffectiveShare, 1e-9)
		}
	}

	accounts[0].RateLimitResetAt, accounts[2].RateLimitResetAt = &resetAt, &resetAt
	_, active = shareMemberViews(accounts, time.Now())
	require.Nil(t, active, "no healthy open member means no active tier")
}

func TestShareAdminPreviewReportsRisks(t *testing.T) {
	lf := func(n int) *int { return &n }
	env := newShareAdminEnv(t, Account{ID: 1, Concurrency: 1}, Account{ID: 2, Concurrency: 9})
	accountIn(env.repo.accounts, 1).LoadFactor = lf(9)
	accountIn(env.repo.accounts, 2).LoadFactor = lf(1)
	env.cache.bind("s-a", 1, time.Hour)

	preview, err := env.share.Preview(context.Background(), shareTestGroupID, []byte(`{"scheduler":"share"}`))
	require.NoError(t, err)
	codes := map[string]int64{}
	for _, w := range preview.Warnings {
		codes[w.Code] = w.AccountID
	}
	require.Equal(t, int64(1), codes["capacity_mismatch"], "90%% of sessions on 10%% of the capacity")
	require.Len(t, env.cache.sessionBindings, 1, "preview never deletes bindings")
}

func TestShareAdminExplainHasNoSideEffects(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.save(t, 0, `{"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":3},{"account_id":2,"tier":1,"weight":1}]}`)
	require.NoError(t, err)
	env.cache.bind("explained", 2, time.Hour-time.Minute)

	out, err := env.share.Explain(context.Background(), shareTestGroupID, ShareSchedulingExplainInput{
		Model: "claude-sonnet-4-5", SessionHash: "explained", EstimatedTokens: 20_000})
	require.NoError(t, err)
	require.Equal(t, string(sharesched.ReasonAffinityHit), out.Reason)
	require.Equal(t, int64(2), out.BoundAccountID)
	require.Equal(t, int64(2), out.Steps[0])
	require.True(t, out.Warm)
	require.Positive(t, out.WaitBudgetMs)
	require.Len(t, out.Entries, 2)
	require.NotContains(t, out.Session, "explained")
	require.Empty(t, env.cache.created)
	require.Empty(t, env.cache.refreshed)
	require.Empty(t, env.conc.attempts, "explain never takes a slot")
}

func TestShareAdminRebalanceClearsPlacements(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.save(t, 0, `{"scheduler":"share"}`)
	require.NoError(t, err)
	env.cache.bind("warm", 1, time.Hour-time.Minute)
	env.cache.bind("openai:response:resp_1", 1, 30*time.Minute)

	result, err := env.share.Rebalance(context.Background(), shareTestGroupID, ShareRebalanceAll)
	require.NoError(t, err)
	require.Equal(t, 1, result.Cleared)
	require.Contains(t, env.cache.sessionBindings, "openai:response:resp_1", "continuation ownership is never cleared")

	_, err = env.share.Rebalance(context.Background(), shareTestGroupID, "everything")
	require.True(t, infraerrors.IsBadRequest(err))
}

func TestShareAdminReportListAndDecisions(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.save(t, 0, `{"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":3},{"account_id":2,"tier":1,"weight":1}]}`)
	require.NoError(t, err)
	env.policies.usage = map[int64]ShareSchedulingUsage{1: {Requests: 30, Tokens: 3000, StandardCost: 3}, 2: {Requests: 10, Tokens: 1000, StandardCost: 1}}
	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true, CacheAttribution: true, AttributionSampleRate: 1, DecisionSamples: true},
		groups: env.share.state.Load().groups})

	ttft := 900
	for i := range 20 {
		ctx := context.WithValue(cacheableContext(), ctxkey.RequestID, "rep-"+string(rune('a'+i)))
		result := env.selectAccount(t, ctx, "", nil)
		env.share.observeUsage(ctx, &UsageLog{AccountID: result.Account.ID, Model: "claude-sonnet-4-5", InputTokens: 10_000, FirstTokenMs: &ttft})
	}
	env.share.flushStats(context.Background())

	report, err := env.share.Report(context.Background(), shareTestGroupID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(20), report.Decisions.Requests)
	require.Equal(t, int64(20), report.Decisions.Reasons[string(sharesched.ReasonSessionless)])
	require.Equal(t, int64(20), report.Cache.Classes[shareCacheMissSessionless])
	require.Equal(t, 1000, *report.Cache.TTFT[string(sharesched.ReasonSessionless)].P50Ms)
	var placed int64
	for _, a := range report.Actual {
		placed += a.Placed
		if a.AccountID == 1 {
			require.InDelta(t, 0.75, a.RequestShare, 1e-9)
		}
	}
	require.Equal(t, int64(20), placed)

	groups, err := env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Equal(t, "share", groups[0].Scheduler)
	require.True(t, groups[0].Effective)
	require.Equal(t, int64(20), groups[0].Requests1h)

	decisions, err := env.share.RecentDecisions(context.Background(), shareTestGroupID, 5)
	require.NoError(t, err)
	require.Len(t, decisions, 5)
	var sample map[string]any
	require.NoError(t, json.Unmarshal(decisions[0], &sample))
	require.Equal(t, "sessionless", sample["reason"])
}

func TestShareAdminReportShowsAccountSkipsAndLatency(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.save(t, 0, `{"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":3},{"account_id":2,"tier":1,"weight":1}]}`)
	require.NoError(t, err)
	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true, CacheAttribution: true, AttributionSampleRate: 1},
		groups: env.share.state.Load().groups})
	env.conc.busy[1] = -1

	ttft := 2500
	for i := range 20 {
		ctx := context.WithValue(cacheableContext(), ctxkey.RequestID, fmt.Sprintf("acct-%d", i))
		result := env.selectAccount(t, ctx, "", nil)
		require.Equal(t, int64(2), result.Account.ID)
		env.share.observeUsage(ctx, &UsageLog{AccountID: 2, Model: "claude-sonnet-4-5", InputTokens: 10_000, FirstTokenMs: &ttft})
	}
	env.share.flushStats(context.Background())

	report, err := env.share.Report(context.Background(), shareTestGroupID, time.Hour)
	require.NoError(t, err)
	actual := map[int64]ShareSchedulingAccountActual{}
	for _, a := range report.Actual {
		actual[a.AccountID] = a
	}
	require.Positive(t, actual[1].Skipped[string(sharesched.ReasonSaturated)], "the busy account is reported as saturated")
	require.Nil(t, actual[1].TTFT)
	require.Empty(t, actual[2].Skipped)
	require.Equal(t, int64(20), actual[2].Placed)
	require.Equal(t, int64(20), actual[2].TTFT.Count)
	require.Equal(t, 3000, *actual[2].TTFT.P95Ms)
}

func TestShareAdminAccountRulesShowPriorityAndLoadFactor(t *testing.T) {
	// Accounts 1 and 2 have load factors 3 and 1 in priority 1
	// (twoAccountPolicy); account 3 has neither.
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3, Priority: 4, Concurrency: 7}, Account{ID: 4})
	ctx := context.Background()
	rules, err := env.share.AccountRules(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, rules, "no group stores a policy yet")

	_, err = env.save(t, 0, `{"scheduler":"share","accounts":[{"account_id":1,"tier":2,"weight":33}]}`)
	require.NoError(t, err)
	rules, err = env.share.AccountRules(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, []ShareSchedulingAccountRule{{GroupID: shareTestGroupID, GroupName: "share-group", Scheduler: "share",
		Effective: true, Tier: 1, Weight: 3}}, rules, "the stored rule is ignored")

	rules, err = env.share.AccountRules(ctx, 3)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, 4, rules[0].Tier, "the tier is the priority")
	require.Equal(t, 7, rules[0].Weight, "without a load factor the weight is the concurrency")

	env.repo.accountsByID[4].AccountGroups = nil
	rules, err = env.share.AccountRules(ctx, 4)
	require.NoError(t, err)
	require.Empty(t, rules)

	_, err = env.share.AccountRules(ctx, 99)
	require.Error(t, err)
}

func TestShareAdminSettingsRoundTrip(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.share.UpdateSettings(context.Background(), ShareSchedulingSettings{Enabled: true, AttributionSampleRate: 0})
	require.True(t, infraerrors.IsBadRequest(err))

	updated, err := env.share.UpdateSettings(context.Background(), ShareSchedulingSettings{Enabled: true, CacheAttribution: true, AttributionSampleRate: 8})
	require.NoError(t, err)
	require.Equal(t, 8, updated.AttributionSampleRate)
	require.Contains(t, env.settings.values[shareSchedulingSettingsKey], `"attribution_sample_rate":8`)
	require.Equal(t, []int64{shareSchedulingSettingsChange}, env.store.published)
}
