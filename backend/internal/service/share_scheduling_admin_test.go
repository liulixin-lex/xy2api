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

	_, err = env.save(t, 0, `{"scheduler":"share","accounts":[{"account_id":99,"tier":1,"weight":1}]}`)
	require.True(t, infraerrors.IsBadRequest(err))
	var appErr *infraerrors.ApplicationError
	require.True(t, errors.As(err, &appErr))
	require.Contains(t, appErr.Metadata["problems"], "account 99 is not a member")

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

func TestShareAdminMemberSharesFollowTiersAndHealth(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})
	_, err := env.save(t, 0, shareAdminPolicy)
	require.NoError(t, err)

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

	_, err = env.save(t, 4, shareAdminPolicy)
	require.Error(t, err, "members 3 is not in this group")
	_, err = env.save(t, 4, `{"scheduler":"shadow"}`)
	require.NoError(t, err)
}

func TestShareAdminActiveTierHandlesNegativeTiers(t *testing.T) {
	p := sharesched.DefaultPolicy()
	p.Accounts = []sharesched.AccountRule{{AccountID: 1, Tier: -5, Weight: 1}, {AccountID: 2, Tier: -9, Weight: 1}, {AccountID: 3, Tier: 0, Weight: 1}}
	resetAt := time.Now().Add(time.Hour)
	accounts := []Account{
		{ID: 1, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 2, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &resetAt},
		{ID: 3, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	views, active := shareMemberViews(&p, accounts, time.Now())
	require.NotNil(t, active)
	require.Equal(t, -5, *active, "the lowest tier with a healthy open member wins, even below zero")
	for _, v := range views {
		if v.AccountID == 1 {
			require.InDelta(t, 1, v.EffectiveShare, 1e-9)
		}
	}

	accounts[0].RateLimitResetAt, accounts[2].RateLimitResetAt = &resetAt, &resetAt
	_, active = shareMemberViews(&p, accounts, time.Now())
	require.Nil(t, active, "no healthy open member means no active tier")
}

func TestShareAdminPreviewReportsChangesAndRisks(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1, Concurrency: 1}, Account{ID: 2, Concurrency: 9})
	_, err := env.save(t, 0, `{"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1},{"account_id":2,"tier":1,"weight":1}]}`)
	require.NoError(t, err)
	env.cache.bind("s-a", 1, time.Hour)
	env.cache.bind("s-b", 1, time.Hour)
	env.cache.bind("s-c", 2, time.Hour)

	deadline := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	preview, err := env.share.Preview(context.Background(), shareTestGroupID, []byte(`{"scheduler":"share","accounts":[
		{"account_id":1,"tier":1,"weight":3,"drain":true,"drain_until":"`+deadline+`"},{"account_id":2,"tier":1,"weight":1}]}`))
	require.NoError(t, err)
	require.Greater(t, preview.PlacementChange, 0.3, "draining account 1 moves its new sessions")
	require.Equal(t, []int64{1}, preview.DeadlineAccounts)
	require.Equal(t, 2, preview.EstimatedMigrations)
	require.Len(t, env.cache.sessionBindings, 3, "preview never deletes bindings")

	preview, err = env.share.Preview(context.Background(), shareTestGroupID,
		[]byte(`{"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":9},{"account_id":2,"tier":1,"weight":1}]}`))
	require.NoError(t, err)
	codes := map[string]int64{}
	for _, w := range preview.Warnings {
		codes[w.Code] = w.AccountID
	}
	require.Equal(t, int64(1), codes["capacity_mismatch"], "90%% of sessions on 10%% of the capacity")

	_, err = env.share.Preview(context.Background(), shareTestGroupID,
		[]byte(`{"scheduler":"share","accounts":[{"account_id":1,"tier":1,"weight":1,"drain":true},{"account_id":2,"tier":1,"weight":1,"drain":true}]}`))
	require.True(t, infraerrors.IsBadRequest(err), "a policy draining every member is rejected")
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

func TestShareAdminRebalanceClearsOnlyColdPlacements(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.save(t, 0, `{"scheduler":"share","cache":{"ttl_seconds":300},"accounts":[{"account_id":1,"tier":1,"weight":1},{"account_id":2,"tier":1,"weight":1}]}`)
	require.NoError(t, err)
	env.cache.bind("warm", 1, time.Hour-time.Minute)
	env.cache.bind("cold", 2, 30*time.Minute)
	env.cache.bind("openai:response:resp_1", 1, 30*time.Minute)

	result, err := env.share.Rebalance(context.Background(), shareTestGroupID, "")
	require.NoError(t, err)
	require.Equal(t, ShareRebalanceCold, result.Mode)
	require.Equal(t, 1, result.Cleared)
	require.Contains(t, env.cache.sessionBindings, "warm")
	require.NotContains(t, env.cache.sessionBindings, "cold")

	result, err = env.share.Rebalance(context.Background(), shareTestGroupID, ShareRebalanceAll)
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
