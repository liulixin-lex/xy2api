//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// shareMemoryRepo is an in-memory policy repository with the CAS semantics
// of the SQL one. The log-table reads return the configured figures and
// count their calls.
type shareMemoryRepo struct {
	mu        sync.Mutex
	rows      map[int64]ShareSchedulingPolicyRow
	usage     map[int64]ShareSchedulingUsage
	summaries map[int64]ShareSchedulingGroupSummary
	// metrics returns the database metrics of a window; zero when nil.
	metrics func(from, to time.Time) ShareSchedulingGroupMetrics

	usageCalls, summaryCalls int
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

func (r *shareMemoryRepo) AccountUsage(context.Context, int64, time.Time, time.Time) (map[int64]ShareSchedulingUsage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usageCalls++
	return r.usage, nil
}

func (r *shareMemoryRepo) GroupSummaries(context.Context, time.Time) (map[int64]ShareSchedulingGroupSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summaryCalls++
	return r.summaries, nil
}

func (r *shareMemoryRepo) GroupMetrics(_ context.Context, _ int64, from, to time.Time) (ShareSchedulingGroupMetrics, error) {
	if r.metrics == nil {
		return ShareSchedulingGroupMetrics{}, nil
	}
	return r.metrics(from, to), nil
}

// The memory store keeps counters, broadcasts and the switch anchor;
// bindings are read from the test gateway cache.
type shareAdminStore struct {
	shareMemoryStore
	mu        sync.Mutex
	counters  map[ShareSchedulingCounter]int64
	published []int64
	switched  map[int64]time.Time
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
		m.counters = map[ShareSchedulingCounter]int64{}
	}
	for k, v := range batch.Counters {
		m.counters[k] += v
	}
	return nil
}

func (m *shareAdminStore) ReadCounters(_ context.Context, kind string, groupID int64, from, to time.Time) (map[string]int64, error) {
	return m.sum(kind, groupID, from.Unix()/60, to.Unix()/60+1), nil
}

func (m *shareAdminStore) ReadHourCounters(_ context.Context, kind string, groupID int64, fromHour, toHour int64) (map[string]int64, error) {
	return m.sum(kind, groupID, fromHour*60, toHour*60), nil
}

// sum adds the counters of the minutes [from, to).
func (m *shareAdminStore) sum(kind string, groupID, from, to int64) map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int64{}
	for k, v := range m.counters {
		if k.Kind == kind && k.GroupID == groupID && k.Minute >= from && k.Minute < to {
			out[k.Field] += v
		}
	}
	return out
}

func (m *shareAdminStore) RecordSwitch(_ context.Context, groupID int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.switched == nil {
		m.switched = map[int64]time.Time{}
	}
	m.switched[groupID] = at
	return nil
}

func (m *shareAdminStore) SwitchedAt(_ context.Context, groupID int64) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.switched[groupID], nil
}

// add writes counters straight into the store at a time.
func (m *shareAdminStore) add(kind string, groupID int64, at time.Time, fields map[string]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counters == nil {
		m.counters = map[ShareSchedulingCounter]int64{}
	}
	for field, n := range fields {
		m.counters[ShareSchedulingCounter{Kind: kind, GroupID: groupID, Minute: at.Unix() / 60, Field: field}] += n
	}
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

// shareAdminAccountRepo lists every member of a group, whatever its status.
type shareAdminAccountRepo struct{ *mockAccountRepoForPlatform }

func (r shareAdminAccountRepo) ListAllWithFilters(_ context.Context, _, _, _, _ string, groupID int64, _ string) ([]Account, error) {
	var out []Account
	for _, acc := range r.accounts {
		for _, ag := range acc.AccountGroups {
			if ag.GroupID == groupID {
				out = append(out, acc)
			}
		}
	}
	return out, nil
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
	store := &shareAdminStore{}
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

var (
	shareUpdate   = ShareSchedulingPolicyUpdate{Scheduler: sharesched.SchedulerShare, SessionKeep: true}
	sub2apiUpdate = ShareSchedulingPolicyUpdate{Scheduler: sharesched.SchedulerSub2API, SessionKeep: true}
)

func (e *shareAdminEnv) save(t *testing.T, expected int64, update ShareSchedulingPolicyUpdate) (*ShareSchedulingPolicyView, error) {
	t.Helper()
	return e.share.SavePolicy(context.Background(), shareTestGroupID, expected, update, 9)
}

func (e *shareAdminEnv) member(t *testing.T, views []ShareSchedulingMemberView, id int64) ShareSchedulingMemberView {
	t.Helper()
	for _, v := range views {
		if v.AccountID == id {
			return v
		}
	}
	t.Fatalf("member %d not listed", id)
	return ShareSchedulingMemberView{}
}

func TestShareAdminParsePolicyUpdate(t *testing.T) {
	str := func(s string) *string { return &s }
	keep := func(b bool) *bool { return &b }
	for _, tc := range []struct {
		name        string
		scheduler   *string
		sessionKeep *bool
		legacy      string
		want        ShareSchedulingPolicyUpdate
		problem     string
	}{
		{name: "new share", scheduler: str("share"), sessionKeep: keep(false), want: ShareSchedulingPolicyUpdate{Scheduler: "share"}},
		{name: "new original", scheduler: str("sub2api"), sessionKeep: keep(true), want: sub2apiUpdate},
		{name: "new shadow", scheduler: str("shadow"), sessionKeep: keep(true), problem: "scheduler must be"},
		{name: "missing keep", scheduler: str("share"), problem: "required"},
		{name: "legacy shadow", legacy: `{"scheduler":"shadow","accounts":[{"account_id":1,"tier":1,"weight":3}]}`, want: sub2apiUpdate},
		{name: "legacy keep off", legacy: `{"scheduler":"share","affinity":{"enabled":true,"key":"none","ttl_seconds":60}}`,
			want: ShareSchedulingPolicyUpdate{Scheduler: "share"}},
		{name: "legacy unknown", legacy: `{"scheduler":"share","bogus":1}`, problem: "unknown field"},
		{name: "legacy key", legacy: `{"scheduler":"share","affinity":{"key":"sticky"}}`, problem: "affinity.key"},
		{name: "mixed", scheduler: str("share"), legacy: `{"scheduler":"share"}`, problem: "either"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSharePolicyUpdate(tc.scheduler, tc.sessionKeep, json.RawMessage(tc.legacy))
			if tc.problem == "" {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
				return
			}
			require.True(t, infraerrors.IsBadRequest(err), "%v", err)
			require.Contains(t, err.Error(), tc.problem)
			var appErr *infraerrors.ApplicationError
			require.True(t, errors.As(err, &appErr))
			require.Equal(t, "SHARE_SCHEDULING_POLICY_INVALID", appErr.Reason)
		})
	}
}

func TestShareAdminSavePolicyCASAndBroadcasts(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3})

	view, err := env.save(t, 0, shareUpdate)
	require.NoError(t, err)
	require.Equal(t, int64(1), view.Version)
	require.Equal(t, "share", view.Scheduler)
	require.True(t, view.SessionKeep)
	require.True(t, view.Effective)
	require.True(t, view.Enabled)
	require.Equal(t, int64(9), *view.UpdatedBy)
	require.Equal(t, []int64{shareTestGroupID}, env.store.published)
	require.Equal(t, int64(1), env.share.groupPolicy(shareTestGroupID).version, "the saving instance reloads at once")

	_, err = env.save(t, 0, shareUpdate)
	require.ErrorIs(t, err, ErrShareSchedulingVersionConflict)
	view, err = env.save(t, 1, ShareSchedulingPolicyUpdate{Scheduler: sharesched.SchedulerShare})
	require.NoError(t, err)
	require.Equal(t, int64(2), view.Version)
	require.False(t, view.SessionKeep)
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

	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.Equal(t, "sub2api", view.Scheduler, "a stored shadow policy is shown as the original scheduler")
	require.True(t, view.LegacyShadow)
	groups, err := env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.Equal(t, "sub2api", groups[0].Scheduler)
	require.True(t, groups[0].LegacyShadow)

	view, err = env.save(t, 4, ShareSchedulingPolicyUpdate{Scheduler: sharesched.SchedulerShare})
	require.NoError(t, err)
	require.Equal(t, "share", view.Scheduler)
	require.False(t, view.LegacyShadow)
	saved := env.policies.rows[shareTestGroupID]
	require.Equal(t, "share", saved.Scheduler)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(saved.Policy, &doc))
	require.JSONEq(t, `[{"account_id":1,"tier":2,"weight":33}]`, string(doc["accounts"]), "stored rules are kept")
	require.JSONEq(t, `{"tier":"account_priority","weight":"equal"}`, string(doc["defaults"]))
	require.JSONEq(t, `{"enabled":true,"key":"none","ttl_seconds":3600}`, string(doc["affinity"]))
}

// TestShareAdminSaveRecordsSwitchAnchor: only a scheduler change records
// the anchor of the report baseline (design §7.7).
func TestShareAdminSaveRecordsSwitchAnchor(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	view, err := env.save(t, 0, sub2apiUpdate)
	require.NoError(t, err)
	require.Empty(t, env.store.switched, "a new group on the original scheduler did not switch")

	_, err = env.save(t, view.Version, shareUpdate)
	require.NoError(t, err)
	first := env.store.switched[shareTestGroupID]
	require.False(t, first.IsZero())
	require.True(t, first.Equal(env.policies.rows[shareTestGroupID].UpdatedAt), "the anchor is the committed update time")

	time.Sleep(2 * time.Millisecond)
	_, err = env.save(t, 2, ShareSchedulingPolicyUpdate{Scheduler: sharesched.SchedulerShare})
	require.NoError(t, err)
	require.True(t, env.store.switched[shareTestGroupID].Equal(first), "changing session keeping is no switch")

	_, err = env.save(t, 3, sub2apiUpdate)
	require.NoError(t, err)
	require.True(t, env.store.switched[shareTestGroupID].After(first), "switching back is a switch")
}

func TestShareSharesFollowPrioritiesWeightsAndHealth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		members   []shareMember
		expected  []float64
		ifHealthy []float64
		active    *int
	}{
		{name: "one tier", members: []shareMember{{1, 3, true}, {1, 1, true}},
			expected: []float64{0.75, 0.25}, ifHealthy: []float64{0.75, 0.25}, active: ptrInt(1)},
		{name: "overflow tier", members: []shareMember{{1, 3, true}, {2, 1, true}},
			expected: []float64{1, 0}, ifHealthy: []float64{1, 0}, active: ptrInt(1)},
		{name: "unhealthy peer", members: []shareMember{{1, 3, false}, {1, 1, true}},
			expected: []float64{0, 1}, ifHealthy: []float64{0.75, 1}, active: ptrInt(1)},
		{name: "unhealthy top", members: []shareMember{{1, 3, false}, {2, 1, true}},
			expected: []float64{0, 1}, ifHealthy: []float64{1, 1}, active: ptrInt(2)},
		{name: "unhealthy lower", members: []shareMember{{1, 3, true}, {2, 1, false}},
			expected: []float64{1, 0}, ifHealthy: []float64{1, 0}, active: ptrInt(1)},
		{name: "negative priorities", members: []shareMember{{-5, 1, true}, {-9, 1, false}, {0, 1, true}},
			expected: []float64{1, 0, 0}, ifHealthy: []float64{1, 1, 0}, active: ptrInt(-5)},
		{name: "none healthy", members: []shareMember{{1, 3, false}, {1, 1, false}},
			expected: []float64{0, 0}, ifHealthy: []float64{1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected, ifHealthy, active := shareShares(tc.members)
			require.InDeltaSlice(t, tc.expected, expected, 1e-9)
			require.InDeltaSlice(t, tc.ifHealthy, ifHealthy, 1e-9)
			require.Equal(t, tc.active, active)
		})
	}
}

func TestShareAdminMemberViewListsEveryMemberWithShares(t *testing.T) {
	disabled := Account{ID: 4, Status: StatusDisabled, Priority: 1, Concurrency: 2}
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3, Priority: 2, Concurrency: 7}, disabled)
	accountIn(env.repo.accounts, 4).Status = StatusDisabled
	accountIn(env.repo.accounts, 1).GroupIDs = []int64{31, shareTestGroupID, 30}
	env.policies.usage = map[int64]ShareSchedulingUsage{1: {Requests: 30}, 2: {Requests: 10}}

	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.Equal(t, int64(0), view.Version)
	require.Equal(t, "sub2api", view.Scheduler)
	require.True(t, view.SessionKeep, "a group without a policy keeps sessions")
	require.False(t, view.Effective)
	require.Nil(t, view.Invalid)
	require.Equal(t, 1, *view.ActivePriority)
	require.Equal(t, []int64{1, 2, 4, 3}, []int64{view.Members[0].AccountID, view.Members[1].AccountID, view.Members[2].AccountID, view.Members[3].AccountID},
		"sorted by priority, then id")

	one := env.member(t, view.Members, 1)
	require.Equal(t, 3, one.EffectiveLoadFactor)
	require.Equal(t, 3, *one.LoadFactor)
	require.InDelta(t, 0.75, one.ExpectedShare, 1e-9, "shares do not depend on the group's scheduler")
	require.Equal(t, []int64{30, 31}, one.OtherGroupIDs)
	require.Equal(t, int64(30), one.ActualRequests1h)
	require.InDelta(t, 0.75, *one.ActualShare1h, 1e-9)

	three := env.member(t, view.Members, 3)
	require.Nil(t, three.LoadFactor)
	require.Equal(t, 7, three.EffectiveLoadFactor, "without a load factor the weight is the concurrency")
	require.Zero(t, three.ExpectedShare)
	require.Zero(t, three.ShareIfHealthy, "a lower priority takes no new sessions while priority 1 is healthy")
	require.Equal(t, []int64{}, three.OtherGroupIDs)

	four := env.member(t, view.Members, 4)
	require.False(t, four.Healthy)
	require.Equal(t, string(sharesched.ReasonDisabled), *four.Reason)
	require.InDelta(t, 2.0/6, four.ShareIfHealthy, 1e-9, "weight 2 next to 3 + 1")

	resetAt := time.Now().Add(time.Hour)
	accountIn(env.repo.accounts, 1).RateLimitResetAt = &resetAt
	view, err = env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	one = env.member(t, view.Members, 1)
	require.False(t, one.Healthy)
	require.Equal(t, string(sharesched.ReasonRateLimited), *one.Reason)
	require.NotNil(t, one.RecoverAt)
	require.InDelta(t, 0.75, one.ShareIfHealthy, 1e-9)
	require.InDelta(t, 1, env.member(t, view.Members, 2).ExpectedShare, 1e-9)
}

// TestShareAdminMemberRequestsAreCachedNotTheirShares: the one-hour counts
// are reused for 30 seconds, while load factors and health are read fresh.
func TestShareAdminMemberRequestsAreCachedNotTheirShares(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	env.policies.usage = map[int64]ShareSchedulingUsage{1: {Requests: 4}}
	_, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)

	lf := 1
	accountIn(env.repo.accounts, 1).LoadFactor = &lf
	env.policies.usage = map[int64]ShareSchedulingUsage{1: {Requests: 99}}
	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.Equal(t, 1, env.policies.usageCalls, "the counts come from the cache")
	one := env.member(t, view.Members, 1)
	require.Equal(t, int64(4), one.ActualRequests1h)
	require.InDelta(t, 0.5, one.ExpectedShare, 1e-9, "a changed load factor shows at once")

	env.share.admin.mu.Lock()
	entry := env.share.admin.requests[shareTestGroupID]
	entry.at = entry.at.Add(-shareAdminSummaryTTL)
	env.share.admin.requests[shareTestGroupID] = entry
	env.share.admin.mu.Unlock()
	view, err = env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.Equal(t, int64(99), env.member(t, view.Members, 1).ActualRequests1h)
}

func TestShareAdminInvalidStoredPolicyShowsRunningDefaults(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	env.policies.rows = map[int64]ShareSchedulingPolicyRow{shareTestGroupID: {GroupID: shareTestGroupID, Version: 4,
		Scheduler: "share", Policy: []byte(`{"scheduler":"share","retired":true}`), UpdatedAt: time.Now()}}

	view, err := env.share.GetPolicy(context.Background(), shareTestGroupID)
	require.NoError(t, err)
	require.NotNil(t, view.Invalid)
	require.Equal(t, "sub2api", view.Scheduler, "the request path skips an undecodable row")
	require.Equal(t, int64(4), view.Version, "saving must still replace the stored row")

	groups, err := env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.True(t, groups[0].Invalid)
	require.True(t, groups[0].Configured)
	require.Equal(t, "sub2api", groups[0].Scheduler)

	_, err = env.save(t, 4, shareUpdate)
	require.NoError(t, err, "saving replaces an undecodable row")
	_, err = sharesched.DecodePolicy(env.policies.rows[shareTestGroupID].Policy)
	require.NoError(t, err)
}

func TestShareAdminListGroupsSummaries(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	g := env.gateway.groupRepo.(shareAdminGroupRepo).groups[shareTestGroupID]
	g.AccountCount, g.ActiveAccountCount = 5, 3
	env.policies.summaries = map[int64]ShareSchedulingGroupSummary{shareTestGroupID: {Requests: 40, CacheReadTokens: 300, PromptTokens: 1000}}
	now := time.Now()
	env.store.add(shareStatsKindCache, shareTestGroupID, now, map[string]int64{
		shareStatsRequestsField: 10, shareStatsPromptTokensField: 1000,
		shareStatsClassTokensPrefix + shareCacheMissSwitchSaturate: 150, shareStatsClassTokensPrefix + shareCacheHit: 850})

	groups, err := env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, groups, 1)
	v := groups[0]
	require.Equal(t, "sub2api", v.Scheduler)
	require.True(t, v.SessionKeep)
	require.False(t, v.Configured)
	require.Nil(t, v.UpdatedAt)
	require.Equal(t, int64(5), v.MemberCount)
	require.Equal(t, int64(3), v.AvailableCount)
	require.Equal(t, int64(40), v.Requests1h)
	require.InDelta(t, 0.3, *v.CacheHitRatio1h, 1e-9)
	require.InDelta(t, 0.15, *v.SwitchLossRatio1h, 1e-9)

	env.policies.summaries = nil
	groups, err = env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(40), groups[0].Requests1h, "summaries are reused for 30 seconds")
	require.Equal(t, 1, env.policies.summaryCalls)

	_, err = env.save(t, 0, shareUpdate)
	require.NoError(t, err)
	groups, err = env.share.ListGroups(context.Background())
	require.NoError(t, err)
	require.Equal(t, "share", groups[0].Scheduler, "policy fields are read fresh")
	require.True(t, groups[0].Effective)
	require.Equal(t, int64(1), groups[0].Version)
}

func TestShareAdminPreviewWarnings(t *testing.T) {
	codes := func(warnings []ShareSchedulingWarning) map[string]ShareSchedulingWarning {
		out := map[string]ShareSchedulingWarning{}
		for _, w := range warnings {
			out[w.Code] = w
		}
		return out
	}
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3, Priority: 0, Concurrency: 1})
	lf := 999
	accountIn(env.repo.accounts, 2).LoadFactor = &lf
	resetAt := time.Now().Add(time.Hour)
	accountIn(env.repo.accounts, 3).RateLimitResetAt = &resetAt
	env.cache.bind("s-a", 1, time.Hour)

	preview, err := env.share.Preview(context.Background(), shareTestGroupID, shareUpdate)
	require.NoError(t, err)
	require.True(t, preview.EffectiveAfter)
	require.Equal(t, 1, *preview.ActivePriority)
	got := codes(preview.Warnings)
	require.NotContains(t, got, ShareWarningGlobalSwitchOff)
	require.Equal(t, 0, *got[ShareWarningTopPriorityUnhealthy].Priority)
	require.InDelta(t, 1, *got[ShareWarningTopPriorityUnhealthy].Value, 1e-9)
	require.Equal(t, int64(2), *got[ShareWarningSingleAccountDominant].AccountID, "999 next to 3")
	require.InDelta(t, 999.0/1002, *got[ShareWarningSingleAccountDominant].Value, 1e-9)
	require.Equal(t, int64(2), *got[ShareWarningHighLoadFactor].AccountID)
	require.InDelta(t, 999, *got[ShareWarningHighLoadFactor].Value, 1e-9)
	require.Len(t, env.cache.sessionBindings, 1, "preview writes nothing")
	require.Empty(t, env.policies.rows)

	preview, err = env.share.Preview(context.Background(), shareTestGroupID, sub2apiUpdate)
	require.NoError(t, err)
	require.False(t, preview.EffectiveAfter)
	require.Empty(t, preview.Warnings, "switching back to the original scheduler has no share risk")
	require.NotNil(t, preview.Warnings)

	for _, id := range []int64{1, 2} {
		accountIn(env.repo.accounts, id).RateLimitResetAt = &resetAt
	}
	env.share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{}, groups: env.share.state.Load().groups})
	preview, err = env.share.Preview(context.Background(), shareTestGroupID, shareUpdate)
	require.NoError(t, err)
	require.False(t, preview.EffectiveAfter)
	require.Nil(t, preview.ActivePriority)
	got = codes(preview.Warnings)
	require.Contains(t, got, ShareWarningGlobalSwitchOff)
	require.Contains(t, got, ShareWarningNoHealthyAccount)
	require.NotContains(t, got, ShareWarningTopPriorityUnhealthy)
	require.NotContains(t, got, ShareWarningSingleAccountDominant)
}

func TestShareAdminExplainHasNoSideEffects(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	_, err := env.save(t, 0, shareUpdate)
	require.NoError(t, err)
	env.cache.bind("explained", 2, time.Hour-time.Minute)
	env.conc.mockConcurrencyCache.loadMap = map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 120}}

	out, err := env.share.Explain(context.Background(), shareTestGroupID, ShareSchedulingExplainInput{
		Model: "claude-sonnet-4-5", SessionHash: "explained", EstimatedTokens: 20_000})
	require.NoError(t, err)
	require.False(t, out.SimulatedNewSession)
	require.Equal(t, "share", out.Scheduler)
	require.Equal(t, string(sharesched.ReasonAffinityHit), out.Reason)
	require.Equal(t, int64(2), out.BoundAccountID)
	require.Equal(t, int64(2), out.Steps[0])
	require.True(t, out.Warm)
	require.Positive(t, out.WaitBudgetMs)
	require.Equal(t, 1, *out.ActivePriority)
	require.Len(t, out.Entries, 2)
	for _, e := range out.Entries {
		require.NotNil(t, e.LoadRate)
		require.Equal(t, e.AccountID == 1, e.Saturated)
		require.Equal(t, 1, e.Priority)
	}
	require.NotContains(t, out.Session, "explained")
	require.Empty(t, env.cache.created)
	require.Empty(t, env.cache.refreshed)
	require.Empty(t, env.conc.attempts, "explain never takes a slot")
}

// TestShareAdminExplainSimulatesNewSessionsWithoutModel: without a session
// the plan is that of a new session; an empty model filters nothing.
func TestShareAdminExplainSimulatesNewSessionsWithoutModel(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	out, err := env.share.Explain(context.Background(), shareTestGroupID, ShareSchedulingExplainInput{})
	require.NoError(t, err)
	require.True(t, out.SimulatedNewSession)
	require.Equal(t, "sub2api", out.Scheduler, "explain also works for a group on the original scheduler")
	require.Equal(t, string(sharesched.ReasonNewSession), out.Reason)
	require.Zero(t, out.BoundAccountID)
	require.Len(t, out.Steps, 2)
	for _, e := range out.Entries {
		require.True(t, e.Eligible, "no model gate applies: %+v", e.Entry)
	}
	require.Empty(t, env.cache.created)
}

func shareReportEnv(t *testing.T) *shareAdminEnv {
	t.Helper()
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	env.policies.metrics = func(from, to time.Time) ShareSchedulingGroupMetrics {
		p50, p95 := 800, 2400
		return ShareSchedulingGroupMetrics{ShareSchedulingGroupSummary: ShareSchedulingGroupSummary{Requests: 90, CacheReadTokens: 600, PromptTokens: 1000},
			TTFTP50Ms: &p50, TTFTP95Ms: &p95, TTFTSamples: 40, Errors: 10}
	}
	env.policies.usage = map[int64]ShareSchedulingUsage{1: {Requests: 30, Tokens: 3000, StandardCost: 3}, 2: {Requests: 10, Tokens: 1000, StandardCost: 1}}
	return env
}

func TestShareAdminReportOnOriginalScheduler(t *testing.T) {
	env := shareReportEnv(t)
	now := time.Now()
	env.store.add(shareStatsKindCache, shareTestGroupID, now, map[string]int64{
		shareStatsRequestsField: 8, shareStatsPromptTokensField: 1000,
		shareStatsClassPrefix + shareCacheMissSwitchFailover: 2, shareStatsClassTokensPrefix + shareCacheMissSwitchFailover: 250,
		shareStatsClassPrefix + shareCacheHit: 6, shareStatsClassTokensPrefix + shareCacheHit: 750})
	env.store.add(shareStatsKindShare, shareTestGroupID, now, map[string]int64{shareStatsReasonPrefix + "new_session": 5})

	report, err := env.share.Report(context.Background(), shareTestGroupID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, "sub2api", report.Scheduler)
	require.Equal(t, int64(3600), report.WindowSeconds)
	require.Equal(t, ShareBaselineNotShare, *report.BaselineUnavailable)
	require.Nil(t, report.Baseline)
	require.Nil(t, report.SwitchedAt)
	require.Equal(t, shareCounterPrecisionMinute, report.CounterPrecision)
	m := report.Current
	require.Equal(t, int64(90), m.Requests)
	require.InDelta(t, 0.6, *m.CacheHitRatio, 1e-9)
	require.Equal(t, 800, *m.TTFTP50Ms)
	require.Equal(t, int64(10), m.Errors)
	require.InDelta(t, 0.1, *m.ErrorRate, 1e-9)
	require.Equal(t, int64(8), m.AttributionSamples)
	require.InDelta(t, 0.25, *m.SwitchRate, 1e-9)
	require.InDelta(t, 0.25, *m.SwitchLossRatio, 1e-9)
	require.Equal(t, ShareSchedulingCacheClass{Requests: 6, Tokens: 750}, report.Details.CacheClasses[shareCacheHit])
	require.Nil(t, report.Details.Reasons, "decision details are share mode only")
	require.Nil(t, report.Details.Waits)
	require.Len(t, report.Accounts, 2)
	require.InDelta(t, 0.75, report.Accounts[0].RequestShare, 1e-9)
	require.Nil(t, report.Accounts[0].Skipped)
}

func TestShareAdminReportComparesWithTheWindowBeforeTheSwitch(t *testing.T) {
	env := shareReportEnv(t)
	_, err := env.save(t, 0, shareUpdate)
	require.NoError(t, err)
	env.share.attributionRate = 1
	env.conc.busy[1] = -1
	for i := range 6 {
		ctx := context.WithValue(cacheableContext(), ctxkey.RequestID, "rep-"+string(rune('a'+i)))
		result := env.selectAccount(t, ctx, "", nil)
		env.share.observeUsage(ctx, &UsageLog{AccountID: result.Account.ID, Model: "claude-sonnet-4-5", InputTokens: 10_000})
	}
	env.share.flushStats(context.Background())

	// The switch happened 20 minutes ago: the current window starts there.
	anchor := time.Now().Add(-20 * time.Minute)
	env.store.switched[shareTestGroupID] = anchor
	before := anchor.Add(-30 * time.Minute)
	env.store.add(shareStatsKindCache, shareTestGroupID, before, map[string]int64{
		shareStatsRequestsField: 4, shareStatsPromptTokensField: 400,
		shareStatsClassPrefix + shareCacheMissSwitchPolicy: 1, shareStatsClassTokensPrefix + shareCacheMissSwitchPolicy: 100})
	var windows [][2]time.Time
	metrics := env.policies.metrics
	env.policies.metrics = func(from, to time.Time) ShareSchedulingGroupMetrics {
		windows = append(windows, [2]time.Time{from, to})
		return metrics(from, to)
	}

	report, err := env.share.Report(context.Background(), shareTestGroupID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, "share", report.Scheduler)
	require.Equal(t, ShareAnchorSwitchRecord, *report.AnchorSource)
	require.True(t, report.SwitchedAt.Equal(anchor))
	require.True(t, report.CurrentFrom.Equal(anchor), "data from before the switch is not mixed in")
	require.True(t, report.BaselineFrom.Equal(anchor.Add(-time.Hour)))
	require.True(t, report.BaselineTo.Equal(anchor))
	require.Nil(t, report.BaselineUnavailable)
	require.Equal(t, shareCounterPrecisionHour, report.CounterPrecision)
	require.Equal(t, [][2]time.Time{{anchor, report.CurrentTo}, {anchor.Add(-time.Hour), anchor}}, windows)
	require.NotNil(t, report.Baseline)
	require.Equal(t, int64(90), report.Baseline.Requests)
	require.Equal(t, int64(6), report.Current.AttributionSamples)
	require.Equal(t, int64(6), *report.Details.ShareRequests)
	require.Equal(t, int64(6), report.Details.Reasons[string(sharesched.ReasonSessionless)])
	require.Positive(t, report.Accounts[0].Skipped[string(sharesched.ReasonSaturated)], "the busy account is reported as saturated")
	require.Nil(t, report.CountersError)
}

func TestShareAdminReportBaselineFallbacks(t *testing.T) {
	env := shareReportEnv(t)
	_, err := env.save(t, 0, shareUpdate)
	require.NoError(t, err)
	delete(env.store.switched, shareTestGroupID)

	report, err := env.share.Report(context.Background(), shareTestGroupID, time.Hour)
	require.NoError(t, err)
	require.Equal(t, ShareAnchorPolicyUpdatedAt, *report.AnchorSource, "without a switch record the policy's update time anchors")
	require.True(t, report.SwitchedAt.Equal(env.policies.rows[shareTestGroupID].UpdatedAt))
	require.NotNil(t, report.Baseline)

	env.store.switched[shareTestGroupID] = time.Now().Add(-ShareCacheCounterRetention)
	report, err = env.share.Report(context.Background(), shareTestGroupID, 2*time.Hour)
	require.NoError(t, err)
	require.Equal(t, ShareBaselineOutOfRetention, *report.BaselineUnavailable)
	require.NotNil(t, report.Baseline, "database metrics are still given")
	require.Equal(t, int64(90), report.Baseline.Requests)
	require.Nil(t, report.Baseline.SwitchRate)
	require.Zero(t, report.Baseline.AttributionSamples)
	require.True(t, report.CurrentFrom.Equal(report.CurrentTo.Add(-2*time.Hour)), "an old switch does not shorten the window")
}

func TestShareAdminAccountRulesShowExpectedShares(t *testing.T) {
	// Accounts 1 and 2 have load factors 3 and 1 in priority 1
	// (twoAccountPolicy); account 3 has neither.
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2}, Account{ID: 3, Priority: 4, Concurrency: 7}, Account{ID: 4, Priority: 4})
	ctx := context.Background()
	rules, err := env.share.AccountRules(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, rules, "no group stores the share scheduler yet")

	view, err := env.save(t, 0, sub2apiUpdate)
	require.NoError(t, err)
	rules, err = env.share.AccountRules(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, rules, "groups on the original scheduler are left out")

	_, err = env.save(t, view.Version, shareUpdate)
	require.NoError(t, err)
	rules, err = env.share.AccountRules(ctx, 1)
	require.NoError(t, err)
	lf := 3
	require.Equal(t, []ShareSchedulingAccountRule{{GroupID: shareTestGroupID, GroupName: "share-group", Scheduler: "share",
		Effective: true, Priority: 1, LoadFactor: &lf, EffectiveLoadFactor: 3, Healthy: true, ExpectedShare: 0.75, ShareIfHealthy: 0.75}}, rules)

	rules, err = env.share.AccountRules(ctx, 3)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, 4, rules[0].Priority)
	require.Equal(t, 7, rules[0].EffectiveLoadFactor)
	require.Zero(t, rules[0].ExpectedShare)

	env.repo.accountsByID[4].AccountGroups = nil
	rules, err = env.share.AccountRules(ctx, 4)
	require.NoError(t, err)
	require.Empty(t, rules)

	_, err = env.share.AccountRules(ctx, 99)
	require.Error(t, err)
}

// TestShareAdminSettingsKeepRetiredFields: saving the master switch keeps
// the switches of earlier versions in the stored document for a rollback.
func TestShareAdminSettingsKeepRetiredFields(t *testing.T) {
	env := newShareAdminEnv(t, Account{ID: 1}, Account{ID: 2})
	env.settings.values[shareSchedulingSettingsKey] = `{"enabled":false,"cache_attribution":true,"attribution_sample_rate":8,"decision_samples":false}`
	require.NoError(t, env.share.reloadSettings(context.Background()))
	require.False(t, env.share.Settings().Enabled)

	updated, err := env.share.UpdateSettings(context.Background(), true)
	require.NoError(t, err)
	require.True(t, updated.Enabled)
	require.JSONEq(t, `{"enabled":true,"cache_attribution":true,"attribution_sample_rate":8,"decision_samples":false}`,
		env.settings.values[shareSchedulingSettingsKey])
	require.Equal(t, []int64{shareSchedulingSettingsChange}, env.store.published)

	env.settings.values[shareSchedulingSettingsKey] = `not json`
	_, err = env.share.UpdateSettings(context.Background(), false)
	require.NoError(t, err)
	require.JSONEq(t, `{"enabled":false}`, env.settings.values[shareSchedulingSettingsKey])
}
