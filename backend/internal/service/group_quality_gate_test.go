package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualityTestRepository struct {
	policies       GroupQualityGatePolicies
	members        map[int64][]Account
	observations   []GroupQualityObservation
	observedAt     time.Time
	usageWrites    []GroupQualityUsageWrite
	observationErr error
	froms          []time.Time
	memberLoads    int
	deadline       bool
	onLoad         func(context.Context)
}

func (r *qualityTestRepository) LoadPolicies(context.Context) (GroupQualityGatePolicies, error) {
	return cloneGroupQualityGatePolicies(r.policies), nil
}
func (r *qualityTestRepository) CompareAndSwapPolicies(_ context.Context, version int, next GroupQualityGatePolicies) (GroupQualityGatePolicies, error) {
	if version != r.policies.Version {
		return r.policies, ErrGroupQualityGateVersionConflict
	}
	r.policies = cloneGroupQualityGatePolicies(next)
	return next, nil
}
func (r *qualityTestRepository) LoadMembers(ctx context.Context, ids []int64) (map[int64][]Account, error) {
	if r.onLoad != nil {
		r.onLoad(ctx)
	}
	r.memberLoads++
	deadline, ok := ctx.Deadline()
	r.deadline = ok && time.Until(deadline) <= 30*time.Second
	out := map[int64][]Account{}
	for _, id := range ids {
		out[id] = r.members[id]
	}
	return out, nil
}
func (r *qualityTestRepository) LoadObservations(_ context.Context, _ []int64, from, to time.Time) ([]GroupQualityObservation, time.Time, error) {
	r.froms = append(r.froms, from)
	out := []GroupQualityObservation{}
	for _, o := range r.observations {
		if !o.At.Before(from) && o.At.Before(to) {
			out = append(out, o)
		}
	}
	observedAt := r.observedAt
	if observedAt.IsZero() {
		observedAt = to.Add(-time.Second)
	}
	return out, observedAt, r.observationErr
}
func (r *qualityTestRepository) LoadObservationsForReplay(_ context.Context, _ []int64, from, to time.Time) (GroupQualityReplayData, error) {
	out := []GroupQualityObservation{}
	for _, o := range r.observations {
		if !o.At.Before(from) && o.At.Before(to) {
			out = append(out, o)
		}
	}
	return GroupQualityReplayData{Observations: out, UsageWrites: slices.Clone(r.usageWrites)}, r.observationErr
}

type qualityTestAccounts struct {
	AccountRepository
	repo *qualityTestRepository
}

func (r *qualityTestAccounts) ListAllWithFilters(_ context.Context, _, _, _, _ string, id int64, _ string) ([]Account, error) {
	return r.repo.members[id], nil
}

type qualityTestGroups struct {
	GroupRepository
	groups []Group
}

func (r *qualityTestGroups) ListActive(context.Context) ([]Group, error) { return r.groups, nil }
func (r *qualityTestGroups) GetByID(_ context.Context, id int64) (*Group, error) {
	for i := range r.groups {
		if r.groups[i].ID == id {
			return &r.groups[i], nil
		}
	}
	return nil, ErrGroupNotFound
}
func (r *qualityTestGroups) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

type qualityTestLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *qualityTestLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *qualityTestLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type qualityTestEnv struct {
	svc       *GroupQualityGateService
	repo      *qualityTestRepository
	groups    *qualityTestGroups
	base, now time.Time
	logs      qualityTestLogBuffer
}

func newQualityTestEnv(t *testing.T) *qualityTestEnv {
	t.Helper()
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	policy := defaultGroupQualityGatePolicy()
	policy.V3.Enabled = true
	r := &qualityTestRepository{policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{"7": policy}}, members: map[int64][]Account{}}
	g := &qualityTestGroups{groups: []Group{{ID: 7, Platform: PlatformOpenAI, RateMultiplier: 1}}}
	e := &qualityTestEnv{repo: r, groups: g, base: base, now: base}
	e.svc = NewGroupQualityGateService(r, &qualityTestAccounts{repo: r}, g)
	e.svc.processStartedAt = base.Add(-6 * time.Hour)
	e.svc.now = func() time.Time { return e.now }
	e.accounts(1)
	oldLogger := slog.Default()
	oldWriter, oldFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&e.logs, nil)))
	log.SetOutput(oldWriter)
	log.SetFlags(oldFlags)
	t.Cleanup(func() { slog.SetDefault(oldLogger); log.SetOutput(oldWriter); log.SetFlags(oldFlags) })
	return e
}
func (e *qualityTestEnv) accounts(ids ...int64) {
	e.repo.members[7] = nil
	for _, id := range ids {
		e.repo.members[7] = append(e.repo.members[7], Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, RateMultiplier: qualityFloatPtr(1)})
	}
}
func (e *qualityTestEnv) events(id int64, at time.Time, count, failures int) {
	for i := range count {
		e.repo.observations = append(e.repo.observations, GroupQualityObservation{AccountID: id, GroupID: 7, RequestID: fmt.Sprintf("%d-%d-%d", id, at.UnixNano(), i), At: at.Add(time.Duration(i) * time.Millisecond), SoftFailure: i < failures, Success: i >= failures})
	}
}
func (e *qualityTestEnv) evaluate(t *testing.T, after time.Duration) {
	t.Helper()
	e.now = e.base.Add(after)
	e.svc.policyCache.Store(nil)
	loads := len(e.repo.froms)
	err := e.svc.evaluate(context.Background())
	if e.repo.observationErr != nil {
		require.ErrorIs(t, err, e.repo.observationErr)
	} else {
		require.NoError(t, err)
	}
	require.True(t, e.repo.deadline)
	if len(e.repo.froms) > loads {
		require.Equal(t, e.now.Add(-6*time.Hour), e.repo.froms[len(e.repo.froms)-1])
	}
}
func (e *qualityTestEnv) member(id int64) groupQualityMemberSnapshot {
	return e.svc.snapshot.Load().Members[7][id]
}

func TestGroupQualityV3TierEventLifecycle(t *testing.T) {
	e := newQualityTestEnv(t)
	e.accounts(1, 2, 3)
	policy := e.repo.policies.Groups["7"]
	policy.V3.Strict = true
	e.repo.policies.Groups["7"] = policy
	e.evaluate(t, 0)
	for _, accountID := range []int64{1, 2, 3} {
		require.Equal(t, GroupQualityGateTierRetest, e.svc.OrderTier(7, accountID))
	}
	require.Empty(t, e.logs.String(), "first evaluation must not invent a tier transition")

	e.events(1, e.base.Add(10*time.Second), 20, 5)
	e.evaluate(t, time.Minute)
	require.Equal(t, GroupQualityGateTierRetest, e.svc.OrderTier(7, 1))
	require.Empty(t, e.logs.String(), "one failing vote does not change the tier")
	e.evaluate(t, 2*time.Minute)
	require.Equal(t, GroupQualityGateTierFail, e.svc.OrderTier(7, 1))
	lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
	require.Len(t, lines, 1, "one established tier change emits one event")
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
	require.Equal(t, "WARN", event["level"])
	require.Equal(t, "group_quality_gate.tier", event["msg"])
	require.Equal(t, float64(1), event["account_id"])
	require.Equal(t, float64(7), event["group_id"])
	for _, field := range []string{"from_tier", "to_tier", "dimension", "value"} {
		require.Contains(t, event, field)
	}
	require.Equal(t, float64(GroupQualityGateTierRetest), event["from_tier"])
	require.Equal(t, float64(GroupQualityGateTierFail), event["to_tier"])
	require.Equal(t, "error", event["dimension"])
	require.Equal(t, .25, event["value"])
	logged := e.logs.String()
	e.evaluate(t, 3*time.Minute)
	require.Equal(t, logged, e.logs.String(), "an unchanged tier must not repeat the event")
}

func TestGroupQualityV3PolicyCompatibility(t *testing.T) {
	e := newQualityTestEnv(t)
	raw := `{"version":9,"groups":{"7":{"enabled":true,"max_error_rate":0.1700,"max_warm_ttft_p90_ms":31000,"fast_warm_ttft_p90_ms":9000,"pinned_account_ids":[9,1,9],"extension":null},"8":{"enabled":false,"pinned_account_ids":null}}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &e.repo.policies))
	before := map[string]map[string]json.RawMessage{}
	for id, p := range e.repo.policies.Groups {
		b, err := json.Marshal(p)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(b, &fields))
		before[id] = fields
	}
	effective, err := e.svc.Policies(context.Background())
	require.NoError(t, err)
	require.True(t, effective.Groups["7"].V3.Enabled)
	require.Equal(t, 15, effective.Groups["7"].V3.MaxTTFTP90Seconds)
	require.Nil(t, e.repo.policies.Groups["7"].V3, "GET must not persist inheritance")
	v3 := *effective.Groups["7"].V3
	v3.Enabled = false
	saved, err := e.svc.UpdatePolicy(context.Background(), 7, 9, GroupQualityGatePolicy{Enabled: false, MaxErrorRate: .5, PinnedAccountIDs: []int64{999}, V3: &v3})
	require.NoError(t, err)
	require.False(t, saved.Groups["7"].V3.Enabled)
	require.Equal(t, 10, saved.Version)
	for id, p := range e.repo.policies.Groups {
		b, err := json.Marshal(p)
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(b, &fields))
		delete(fields, "v3")
		require.Equal(t, before[id], fields, "all legacy JSON field bytes must survive: %s", id)
	}
	require.Nil(t, e.repo.policies.Groups["8"].V3, "saving one group must not materialize another group's defaults")
	_, err = e.svc.UpdatePolicy(context.Background(), 7, 9, GroupQualityGatePolicy{V3: &v3})
	require.ErrorIs(t, err, ErrGroupQualityGateVersionConflict)
	delete(e.repo.policies.Groups, "7")
	v3.Enabled = true
	_, err = e.svc.UpdatePolicy(context.Background(), 7, 10, GroupQualityGatePolicy{Enabled: true, V3: &v3})
	require.NoError(t, err)
	data, err := json.Marshal(e.repo.policies.Groups["7"])
	require.NoError(t, err)
	var old struct {
		Enabled      bool    `json:"enabled"`
		MaxErrorRate float64 `json:"max_error_rate"`
	}
	require.NoError(t, json.Unmarshal(data, &old))
	require.False(t, old.Enabled, "0.5.1 ignores the v3 subobject")
}

func TestGroupQualityV3PolicyValidation(t *testing.T) {
	base := *defaultGroupQualityGatePolicy().V3
	base.Enabled = true
	for _, tc := range []struct {
		name     string
		edit     func(*GroupQualityGateV3Policy)
		platform string
		valid    bool
	}{
		{"defaults", func(*GroupQualityGateV3Policy) {}, PlatformOpenAI, true},
		{"minimums", func(v *GroupQualityGateV3Policy) {
			v.MaxTTFTP90Seconds = 5
			v.MaxErrorRate = .05
			v.MinCacheKeepRate = .5
		}, PlatformOpenAI, true},
		{"maximums", func(v *GroupQualityGateV3Policy) {
			v.MaxTTFTP90Seconds = 60
			v.MaxErrorRate = .5
			v.MinCacheKeepRate = .7
		}, PlatformOpenAI, true},
		{"ttft low", func(v *GroupQualityGateV3Policy) { v.MaxTTFTP90Seconds = 4 }, PlatformOpenAI, false},
		{"ttft high", func(v *GroupQualityGateV3Policy) { v.MaxTTFTP90Seconds = 61 }, PlatformOpenAI, false},
		{"error low", func(v *GroupQualityGateV3Policy) { v.MaxErrorRate = .049 }, PlatformOpenAI, false},
		{"error high", func(v *GroupQualityGateV3Policy) { v.MaxErrorRate = .51 }, PlatformOpenAI, false},
		{"cache low", func(v *GroupQualityGateV3Policy) { v.MinCacheKeepRate = .49 }, PlatformOpenAI, false},
		{"cache high", func(v *GroupQualityGateV3Policy) { v.MinCacheKeepRate = .71 }, PlatformOpenAI, false},
		{"nan", func(v *GroupQualityGateV3Policy) { v.MaxErrorRate = math.NaN() }, PlatformOpenAI, false},
		{"infinite", func(v *GroupQualityGateV3Policy) { v.MinCacheKeepRate = math.Inf(1) }, PlatformOpenAI, false},
		{"other platform", func(*GroupQualityGateV3Policy) {}, PlatformAnthropic, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			tc.edit(&v)
			err := ValidateGroupQualityGatePolicy(GroupQualityGatePolicy{V3: &v}, &Group{Platform: tc.platform}, nil)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInvalidGroupQualityGatePolicy)
			}
		})
	}
	require.Error(t, ValidateGroupQualityGatePolicy(GroupQualityGatePolicy{}, &Group{Platform: PlatformOpenAI}, nil))
	explicit := GroupQualityGatePolicy{Enabled: true, V3: &GroupQualityGateV3Policy{}}
	require.False(t, normalizeGroupQualityGatePolicy(explicit).V3.Enabled)
	require.Zero(t, normalizeGroupQualityGatePolicy(explicit).V3.MaxTTFTP90Seconds, "present zeros must not become defaults")
}

func TestGroupQualityV3DimensionVotesAndMemory(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	state := groupQualityDimensionState{}
	vote := func(valid, fail, pass, recovered bool) {
		state = voteGroupQualityDimension(state, now, valid, fail, pass, recovered)
		now = now.Add(time.Minute)
	}
	vote(true, true, false, false)
	require.Equal(t, "none", state.state())
	frozen := state
	state = voteGroupQualityDimension(state, now.Add(-30*time.Second), true, true, false, false)
	require.Equal(t, frozen, state, "one vote per minute")
	vote(true, true, false, false)
	require.Equal(t, "fail", state.state())
	require.True(t, state.hasBad(now))
	vote(true, false, true, false)
	require.Equal(t, "fail", state.state(), "recovery band requires the recovery line")
	vote(true, false, true, true)
	require.Equal(t, "fail", state.state())
	vote(true, false, false, false)
	require.Equal(t, 0, state.Votes, "middle band breaks the streak")
	vote(true, false, true, true)
	require.Equal(t, "fail", state.state())
	vote(false, false, false, false)
	require.Equal(t, "none", state.state())
	require.True(t, state.hasBad(now))
	require.Zero(t, state.Votes)
	vote(true, false, true, true)
	require.True(t, state.hasBad(now))
	vote(true, false, true, true)
	require.Equal(t, "pass", state.state())
	require.True(t, state.LastFail.IsZero())
	state.LastFail = now
	require.True(t, state.hasBad(now.Add(2*time.Hour)))
	require.False(t, state.hasBad(now.Add(2*time.Hour+time.Nanosecond)))
}

func TestGroupQualityV3TierAndRecoveryRules(t *testing.T) {
	now := time.Now()
	p := *defaultGroupQualityGatePolicy().V3
	p.Enabled = true
	bad := groupQualityDimensionState{State: "none", LastFail: now}
	for _, tc := range []struct {
		name           string
		s              groupQualityAccountState
		strict, pinned bool
		want           int
		status         string
	}{
		{name: "never measured", want: 0, status: "unknown"},
		{name: "strict unknown", strict: true, want: 1, status: "retest"},
		{name: "bad memory", s: groupQualityAccountState{Error: bad}, want: 1, status: "retest"},
		{name: "other dimension pass cannot clear", s: groupQualityAccountState{Error: bad, Cache: groupQualityDimensionState{State: "pass"}}, want: 1, status: "retest"},
		{name: "expired memory", s: groupQualityAccountState{Error: groupQualityDimensionState{LastFail: now.Add(-2*time.Hour - time.Second)}}, want: 0, status: "unknown"},
		{name: "current fail", s: groupQualityAccountState{Cache: groupQualityDimensionState{State: "fail"}}, want: 2, status: "failing"},
		{name: "fixed", s: groupQualityAccountState{Cache: groupQualityDimensionState{State: "fail"}}, pinned: true, want: 0, status: "pinned"},
		{name: "all passing", s: groupQualityAccountState{TTFT: groupQualityDimensionState{State: "pass"}, Error: groupQualityDimensionState{State: "pass"}, Cache: groupQualityDimensionState{State: "pass"}}, want: 0, status: "passing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := p
			policy.Strict = tc.strict
			tier := groupQualityTier(tc.s, policy, tc.pinned, now)
			require.Equal(t, tc.want, tier)
			require.Equal(t, tc.status, groupQualityStatus(tc.s, tier, tc.pinned))
		})
	}
	state := groupQualityAccountState{TTFT: groupQualityDimensionState{State: "fail"}, Error: groupQualityDimensionState{State: "fail"}, Cache: groupQualityDimensionState{State: "fail"}}
	metric := groupQualityMetrics{ErrorSamples: 20, Failures: 1, ErrorRate: qualityFloatPtr(.09), CacheSamples: 20, CacheRate: qualityFloatPtr(.5), CacheHigh: qualityFloatPtr(.7)}
	for i := range 2 {
		state = evaluateGroupQualityMember(now.Add(time.Duration(i)*time.Minute), p, metric, qualityFloatPtr(14000), state)
	}
	require.Equal(t, "fail", state.TTFT.state())
	require.Equal(t, "fail", state.Error.state())
	require.Equal(t, "pass", state.Cache.state())
	metric.ErrorRate = qualityFloatPtr(.08)
	for i := 2; i < 4; i++ {
		state = evaluateGroupQualityMember(now.Add(time.Duration(i)*time.Minute), p, metric, qualityFloatPtr(13500), state)
	}
	require.Equal(t, "pass", state.TTFT.state())
	require.Equal(t, "pass", state.Error.state())
}

func qualitySpeedObservation(id, account int64, at time.Time, ms int64) GroupQualityObservation {
	return GroupQualityObservation{ID: id, AccountID: account, APIKeyID: 1, GroupID: 7, RequestID: fmt.Sprint(id), At: at, PreviousAt: at.Add(-time.Minute), Success: true, StrictContinuation: true, TTFTEligible: true, SessionID: fmt.Sprintf("session-%d", id/3), Model: "model", ReasoningEffort: "high", PromptTokens: 10000, PreviousPromptTokens: 10000, CacheReadTokens: 6000, AttemptFirstOutputMs: &ms}
}
func qualityCompute(obs []GroupQualityObservation, now time.Time) (map[int64]groupQualityMetrics, map[int64]groupQualityComposition, groupQualityBaselines) {
	obs, failed, softFailed := prepareGroupQualityObservations(obs, now, now.Add(-6*time.Hour), nil)
	b := buildGroupQualityBaselines(obs, failed, now)
	m, c := calculateGroupQualityMetrics(obs, now, b, failed, softFailed, map[int64]bool{7: true})
	return m, c, b
}
func TestGroupQualityV3BaselineAndGM30(t *testing.T) {
	now := time.Now()
	var obs []GroupQualityObservation
	for acc := int64(1); acc <= 3; acc++ {
		for i := range 20 {
			obs = append(obs, qualitySpeedObservation(acc*100+int64(i), acc, now.Add(-time.Duration(i+1)*time.Minute), acc*1000))
		}
	}
	m, c, b := qualityCompute(obs, now)
	expected, ok := b.expected(obs[0])
	require.True(t, ok)
	require.Equal(t, 2000.0, expected, "median of per-account medians")
	require.InDelta(t, .5, *m[1].Factor, 1e-12)
	require.InDelta(t, 1, *m[2].Factor, 1e-12)
	require.InDelta(t, 1.5, *m[3].Factor, 1e-12)
	require.Equal(t, 60, c[7].Samples)
	require.Equal(t, 2000.0, *c[7].P90)
	// A high-volume outlier account does not turn the baseline into a pooled median.
	for i := range 45 {
		obs = append(obs, qualitySpeedObservation(1000+int64(i), 1, now.Add(-time.Duration(i+1)*time.Second), 9000))
	}
	m, c, b = qualityCompute(obs, now)
	expected, _ = b.expected(obs[0])
	require.Equal(t, 3000.0, expected)
	require.LessOrEqual(t, m[1].TTFTSamples, 50)
	require.Equal(t, 100, c[7].Samples)
	// Session caps are applied after baseline matching and before GM validity.
	for i := range obs {
		obs[i].SessionID = "one"
	}
	m, _, _ = qualityCompute(obs, now)
	require.Equal(t, 3, m[1].TTFTSamples)
	require.Nil(t, m[1].Factor)
}
func TestGroupQualityV3BaselineFallbacks(t *testing.T) {
	sample := qualitySpeedObservation(1, 1, time.Now(), 1000)
	keys := groupQualityCells(sample)
	for level := range 4 {
		b := groupQualityBaselines{keys[level]: float64(level + 1)}
		value, ok := b.expected(sample)
		require.True(t, ok)
		require.Equal(t, float64(level+1), value)
	}
	b := groupQualityBaselines{}
	_, ok := b.expected(sample)
	require.False(t, ok)
	for _, tc := range []struct {
		tokens     int64
		band, pair int
	}{{19999, 0, 0}, {20000, 1, 0}, {59999, 1, 0}, {60000, 2, 1}, {119999, 2, 1}, {120000, 3, 1}} {
		sample.PromptTokens = tc.tokens
		cells := groupQualityCells(sample)
		require.Equal(t, tc.band, cells[0].Band)
		require.Equal(t, tc.pair, cells[2].Band)
	}
	now := time.Now()
	var obs []GroupQualityObservation
	for i := range 30 {
		sample = qualitySpeedObservation(int64(i), int64(i%3+1), now.Add(-time.Minute), 1000)
		sample.CacheReadTokens = int64(i%2) * 6000
		sample.PromptTokens = 10000 + int64(i%2)*20000
		obs = append(obs, sample)
	}
	_, _, b = qualityCompute(obs, now)
	require.Len(t, b, 2, "only paired-band and all-band levels meet 30 samples")
	keys = groupQualityCells(obs[0])
	for _, key := range keys[:2] {
		_, ok = b[key]
		require.False(t, ok)
	}
	obs = obs[:29]
	_, _, b = qualityCompute(obs, now)
	require.Empty(t, b)
	for i := range obs {
		obs[i].AccountID = int64(i%2 + 1)
	}
	obs = append(obs, obs[0])
	_, _, b = qualityCompute(obs, now)
	require.Empty(t, b, "at least three accounts")
}

func TestGroupQualityV3StatisticsBoundaries(t *testing.T) {
	now := time.Now()
	var obs []GroupQualityObservation
	for i := range 60 {
		obs = append(obs, qualitySpeedObservation(int64(i), int64(i%3+1), now.Add(-time.Duration(i+1)*time.Second), 1000))
	}
	// Explicit baseline isolates cap and geometric-mean checks from fitting.
	base := groupQualityBaselines{groupQualityCells(obs[0])[3]: 1000}
	for i := range 12 {
		obs = append(obs, qualitySpeedObservation(1000+int64(i), 9, now.Add(-time.Duration(i+1)*time.Second), 10000))
	}
	prepared, failed, soft := prepareGroupQualityObservations(obs, now, now.Add(-6*time.Hour), nil)
	m, c := calculateGroupQualityMetrics(prepared, now, base, failed, soft, map[int64]bool{7: true})
	require.Equal(t, 12, m[9].TTFTSamples)
	require.InDelta(t, 4, *m[9].Factor, 1e-12)
	require.NotNil(t, c[7].P90)
	// The composition samples need a matching current baseline, not their own TTFT value.
	for i := range obs {
		obs[i].AttemptFirstOutputMs = nil
		obs[i].At = now.Add(-3 * time.Hour)
	}
	prepared, failed, soft = prepareGroupQualityObservations(obs, now, now.Add(-6*time.Hour), nil)
	m, c = calculateGroupQualityMetrics(prepared, now, base, failed, soft, map[int64]bool{7: true})
	require.Empty(t, m)
	require.Equal(t, 72, c[7].Samples)
	require.NotNil(t, c[7].P90)
	_, c = calculateGroupQualityMetrics(prepared[:49], now, base, failed, soft, map[int64]bool{7: true})
	require.Nil(t, c[7].P90)
	_, c = calculateGroupQualityMetrics(prepared, now, base, failed, soft, nil)
	require.Empty(t, c, "never borrow composition from another group")
}

func TestGroupQualityV3ErrorsCacheAndFailureAttribution(t *testing.T) {
	now := time.Now()
	var obs []GroupQualityObservation
	// Duplicates and usage rows of broken streams must be removed before the cap.
	for i := range 110 {
		o := qualitySpeedObservation(int64(i), 1, now.Add(-time.Duration(i+1)*time.Second), 1000)
		obs = append(obs, o)
		if i < 5 {
			fail := o
			fail.Success = false
			fail.SoftFailure = true
			fail.Kind = "stream_failed"
			obs = append(obs, fail, fail)
		}
	}
	cutoff := GroupQualityObservation{AccountID: 1, At: now.Add(-time.Second), RequestID: "cutoff", SoftFailure: true, Kind: "first_output_timeout", Category: "FIRST_OUTPUT_TIMEOUT"}
	obs = append(obs, cutoff)
	obs = append(obs, GroupQualityObservation{AccountID: 1, At: now.Add(-2 * time.Second), SoftFailure: true}, GroupQualityObservation{AccountID: 1, At: now.Add(-2 * time.Second), SoftFailure: true})
	m, _, _ := qualityCompute(obs, now)
	require.Equal(t, 100, m[1].ErrorSamples)
	require.Equal(t, 7, m[1].Failures)
	require.Equal(t, 40, m[1].CacheSamples)
	require.Equal(t, 1.0, *m[1].CacheRate)
	// The failed final account is excluded from speed; another account's rescued usage survives.
	good := qualitySpeedObservation(999, 2, now.Add(-time.Second), 1000)
	good.RequestID = "rescued"
	bad := GroupQualityObservation{AccountID: 1, RequestID: "rescued", At: now.Add(-2 * time.Second), SoftFailure: true}
	obs, failed, _ := prepareGroupQualityObservations([]GroupQualityObservation{good, bad}, now, now.Add(-time.Hour), nil)
	require.True(t, groupQualitySpeedSample(obs[0], failed))
	good.AccountID = 1
	require.False(t, groupQualitySpeedSample(good, failed))
	good.RequestID = ""
	require.True(t, groupQualitySpeedSample(good, failed))
	require.True(t, IsGroupQualitySoftFailure(cutoff.Kind, 504, cutoff.Category, ""), "recent health keeps timeouts")
	partial := qualitySpeedObservation(1001, 1, now.Add(-time.Second), 1000)
	partial.RequestID = "partial"
	partialFailure := GroupQualityObservation{AccountID: 1, RequestID: "partial", At: now.Add(-time.Second), SoftFailure: true}
	eligible, failed, softFailed := prepareGroupQualityObservations([]GroupQualityObservation{partial, partialFailure}, now, now.Add(-time.Hour), nil)
	base := groupQualityBaselines{groupQualityCells(partial)[3]: 1000}
	partialMetrics, composition := calculateGroupQualityMetrics(eligible, now, base, failed, softFailed, map[int64]bool{7: true})
	require.Zero(t, partialMetrics[1].TTFTSamples)
	require.Equal(t, 1, partialMetrics[1].CacheSamples, "failure attribution excludes only speed, not cache samples")
	require.Equal(t, 1, composition[7].Samples, "composition includes eligible partial usage")
	// Cache eligibility rejects small prompts and contraction, and uses the previous prompt.
	obs = nil
	for i := range 24 {
		o := qualitySpeedObservation(int64(i), 1, now.Add(-time.Duration(i+1)*time.Second), 1000)
		o.CacheReadTokens = 0
		if i == 0 {
			o.PromptTokens = 4999
		}
		if i == 1 {
			o.PreviousPromptTokens = 2047
		}
		if i == 2 {
			o.PromptTokens = 2047
		}
		if i >= 14 {
			o.CacheReadTokens = 5000
		}
		obs = append(obs, o)
	}
	m, _, _ = qualityCompute(obs, now)
	require.Equal(t, 21, m[1].CacheSamples)
	require.InDelta(t, 10.0/21, *m[1].CacheRate, 1e-12)
	p := *defaultGroupQualityGatePolicy().V3
	p.Enabled = true
	state := evaluateGroupQualityMember(now, p, m[1], nil, groupQualityAccountState{})
	require.Empty(t, state.Cache.Vote, "Wilson upper >= floor and point < floor is the middle band")
}

func TestGroupQualityV3EvaluationLifecycle(t *testing.T) {
	t.Run("two votes stale freeze reset and disabled", func(t *testing.T) {
		e := newQualityTestEnv(t)
		e.events(1, e.base.Add(-time.Minute), 20, 5)
		e.evaluate(t, 0)
		require.Equal(t, 0, e.svc.OrderTier(7, 1))
		e.evaluate(t, time.Minute)
		require.Equal(t, 2, e.svc.OrderTier(7, 1))
		require.Contains(t, e.logs.String(), `"msg":"group_quality_gate.tier"`)
		frozen := e.member(1)
		e.repo.observedAt = e.base.Add(-10 * time.Minute)
		e.evaluate(t, 3*time.Hour)
		require.Equal(t, frozen, e.member(1))
		e.evaluate(t, 3*time.Hour+time.Minute)
		require.Equal(t, 1, strings.Count(e.logs.String(), "group_quality_gate.evaluate_failed"))
		reset := e.now.Add(-time.Second)
		e.repo.members[7][0].Extra = map[string]any{"group_quality_reset_at": reset.Format(time.RFC3339Nano)}
		e.evaluate(t, 3*time.Hour+2*time.Minute)
		require.Equal(t, 0, e.svc.OrderTier(7, 1))
		require.True(t, e.member(1).State.Error.LastFail.IsZero())
		e.repo.observedAt = time.Time{}
		e.evaluate(t, 3*time.Hour+3*time.Minute)
		require.Zero(t, e.member(1).Metric.ErrorSamples)
		policy := *e.repo.policies.Groups["7"].V3
		policy.Enabled = false
		_, err := e.svc.UpdatePolicy(context.Background(), 7, 0, GroupQualityGatePolicy{V3: &policy})
		require.NoError(t, err)
		require.False(t, e.svc.Enabled(7))
		require.Equal(t, 0, e.svc.OrderTier(7, 1))
		e.evaluate(t, 3*time.Hour+4*time.Minute)
		require.Empty(t, e.svc.snapshot.Load().Members)
	})
	t.Run("leaving retains for two hours from departure", func(t *testing.T) {
		e := newQualityTestEnv(t)
		e.events(1, e.base.Add(-time.Minute), 20, 5)
		e.evaluate(t, 0)
		e.evaluate(t, time.Minute)
		e.accounts()
		e.evaluate(t, 2*time.Minute)
		require.Contains(t, e.svc.snapshot.Load().Departed[7], int64(1))
		e.accounts(1)
		e.evaluate(t, 31*time.Minute)
		require.Equal(t, 1, e.svc.OrderTier(7, 1), "missing evidence retains bad memory after rejoin")
		e.accounts()
		e.evaluate(t, 32*time.Minute)
		e.evaluate(t, 152*time.Minute)
		require.Empty(t, e.svc.snapshot.Load().Departed)
	})
	t.Run("restart keeps error state and process boundary for ttft", func(t *testing.T) {
		e := newQualityTestEnv(t)
		e.events(1, e.base.Add(-time.Minute), 20, 5)
		e.evaluate(t, 0)
		e.evaluate(t, time.Minute)
		e.svc = NewGroupQualityGateService(e.repo, &qualityTestAccounts{repo: e.repo}, e.groups)
		e.svc.now = func() time.Time { return e.now }
		e.svc.processStartedAt = e.base.Add(time.Minute)
		e.evaluate(t, 2*time.Minute)
		require.Equal(t, 0, e.svc.OrderTier(7, 1))
		require.Equal(t, 20, e.member(1).Metric.ErrorSamples)
		require.Zero(t, e.member(1).Metric.TTFTSamples)
	})
	t.Run("startup evaluates and cancellation stops", func(t *testing.T) {
		e := newQualityTestEnv(t)
		started := make(chan struct{})
		e.repo.onLoad = func(ctx context.Context) { close(started); <-ctx.Done() }
		e.svc.Start()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("initial evaluation was not started")
		}
		e.svc.Stop()
	})
	t.Run("query errors freeze", func(t *testing.T) {
		e := newQualityTestEnv(t)
		e.events(1, e.base.Add(-time.Minute), 20, 5)
		e.evaluate(t, 0)
		e.evaluate(t, time.Minute)
		frozen := e.member(1)
		e.repo.observationErr = errors.New("unavailable")
		e.evaluate(t, 2*time.Minute)
		require.Equal(t, frozen, e.member(1))
	})
}

func TestGroupQualityV3MigrationTargetAndStatus(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			e := newQualityTestEnv(t)
			p := e.repo.policies.Groups["7"]
			p.V3.Strict = strict
			e.repo.policies.Groups["7"] = p
			e.evaluate(t, 0)
			require.Equal(t, map[bool]int{false: 0, true: 1}[strict], e.svc.OrderTier(7, 99))
			require.False(t, e.svc.MigrationTarget(7, 1))
			member := groupQualityMemberSnapshot{AccountID: 1, ExpectedP90Ms: qualityFloatPtr(13000), Metric: groupQualityMetrics{ErrorSamples: 20, ErrorRate: qualityFloatPtr(.08), CacheSamples: 20, CacheRate: qualityFloatPtr(.5)}}
			snap := *e.svc.snapshot.Load()
			snap.Members = map[int64]map[int64]groupQualityMemberSnapshot{7: {1: member}}
			e.svc.snapshot.Store(&snap)
			require.True(t, e.svc.MigrationTarget(7, 1), "valid values need not yet have two pass votes")
			ms, ok := e.svc.ExpectedP90Ms(7, 1)
			require.True(t, ok)
			require.Equal(t, 13000, ms)
			member.State.Error = groupQualityDimensionState{State: "fail", LastFail: e.now}
			member.Pinned = true
			member.Tier = 0
			snap.Members[7][1] = member
			require.False(t, e.svc.MigrationTarget(7, 1), "numeric recovery without state recovery is not a target, even pinned")
			member.State.Error = groupQualityDimensionState{State: "none", LastFail: e.now}
			snap.Members[7][1] = member
			require.Equal(t, strict, e.svc.MigrationTarget(7, 1), "ordinary target cannot have error bad memory; strict uses effective values and current fails")
		})
	}
	e := newQualityTestEnv(t)
	e.accounts(1, 2, 3)
	e.evaluate(t, 0)
	p := e.repo.policies.Groups["7"]
	p.PinnedAccountIDs = []int64{2}
	e.repo.policies.Groups["7"] = p
	e.evaluate(t, time.Minute)
	e.svc.SetLatencyHoldLookup(func(id int64, now time.Time) (time.Time, []string) {
		if id == 1 {
			return now.Add(time.Minute), []string{"model"}
		}
		return now, []string{"expired"}
	})
	status, err := e.svc.Status(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, GroupQualitySummaryView{Unknown: 2, Pinned: 1}, status.Groups[0].Summary)
	data, err := json.Marshal(status)
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(data, &obj))
	require.ElementsMatch(t, []string{"generated_at", "stale", "groups"}, qualityMapKeys(obj))
	groups, ok := obj["groups"].([]any)
	require.True(t, ok)
	require.Len(t, groups, 1)
	group, ok := groups[0].(map[string]any)
	require.True(t, ok)
	rows, ok := group["accounts"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 3)
	row, ok := rows[0].(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"account_id", "status", "tier", "pinned", "latency_hold", "ttft", "error", "cache"}, qualityMapKeys(row))
	require.NotNil(t, row["latency_hold"])
	pinnedRow, ok := rows[1].(map[string]any)
	require.True(t, ok)
	require.Nil(t, pinnedRow["latency_hold"])
	ttft, ok := row["ttft"].(map[string]any)
	require.True(t, ok)
	require.Nil(t, ttft["value"])
	require.Equal(t, "none", ttft["state"])
}
func qualityMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func qualityFloatPtr(v float64) *float64 { return &v }

func TestGroupQualityV3MinimumSamplesAndFullEvaluation(t *testing.T) {
	now := time.Now()
	for _, count := range []int{9, 10} {
		var observations []GroupQualityObservation
		for i := range count {
			observations = append(observations, qualitySpeedObservation(int64(i), 1, now.Add(-time.Duration(i+1)*time.Second), 1000))
		}
		observations, failed, soft := prepareGroupQualityObservations(observations, now, now.Add(-time.Hour), nil)
		baseline := groupQualityBaselines{groupQualityCells(observations[0])[3]: 1000}
		metrics, _ := calculateGroupQualityMetrics(observations, now, baseline, failed, soft, nil)
		require.Equal(t, count, metrics[1].TTFTSamples)
		require.Equal(t, count >= 10, metrics[1].Factor != nil)
	}
	p := *defaultGroupQualityGatePolicy().V3
	p.Enabled = true
	for _, tc := range []struct {
		samples, failures int
		want              string
	}{{19, 5, "none"}, {20, 4, "none"}, {20, 5, "fail"}, {20, 2, "pass"}} {
		metric := groupQualityMetrics{ErrorSamples: tc.samples, Failures: tc.failures, ErrorRate: qualityFloatPtr(float64(tc.failures) / float64(tc.samples))}
		state := groupQualityAccountState{}
		for i := range 2 {
			state = evaluateGroupQualityMember(now.Add(time.Duration(i)*time.Minute), p, metric, nil, state)
		}
		require.Equal(t, tc.want, state.Error.state())
	}
	low, high := wilsonInterval(0, 20)
	metric := groupQualityMetrics{CacheSamples: 20, CacheRate: qualityFloatPtr(0), CacheLow: &low, CacheHigh: &high}
	state := evaluateGroupQualityMember(now, p, metric, nil, groupQualityAccountState{})
	state = evaluateGroupQualityMember(now.Add(time.Minute), p, metric, nil, state)
	require.Equal(t, "fail", state.Cache.state())
	e := newQualityTestEnv(t)
	e.accounts(1, 2, 3)
	for account := int64(1); account <= 3; account++ {
		for i := range 20 {
			e.repo.observations = append(e.repo.observations, qualitySpeedObservation(account*100+int64(i), account, e.base.Add(-time.Duration(i+1)*time.Minute), account*1000))
		}
	}
	e.evaluate(t, 0)
	e.evaluate(t, time.Minute)
	status, err := e.svc.Status(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, GroupQualitySummaryView{Passing: 3}, status.Groups[0].Summary)
	require.InDelta(t, 2000, *status.Groups[0].ExpectedP90Ms, 1e-9)
	require.True(t, e.svc.MigrationTarget(7, 1))
	require.Equal(t, 0, e.svc.OrderTier(999, 1))
}

func TestGroupQualityV3ResetAllGroupsAndContributorBounds(t *testing.T) {
	e := newQualityTestEnv(t)
	e.groups.groups = append(e.groups.groups, Group{ID: 8, Platform: PlatformOpenAI})
	e.repo.policies.Groups["8"] = e.repo.policies.Groups["7"]
	e.repo.members[8] = e.repo.members[7]
	e.events(1, e.base.Add(-time.Minute), 20, 5)
	e.evaluate(t, 0)
	e.evaluate(t, time.Minute)
	require.Equal(t, 2, e.svc.OrderTier(7, 1))
	require.Equal(t, 2, e.svc.OrderTier(8, 1))
	reset := e.base.Add(90 * time.Second)
	e.repo.members[7][0].Extra = map[string]any{"group_quality_reset_at": reset.Format(time.RFC3339Nano)}
	e.evaluate(t, 2*time.Minute)
	for _, group := range []int64{7, 8} {
		member := e.svc.snapshot.Load().Members[group][1]
		require.Equal(t, 0, member.Tier)
		require.True(t, member.State.Error.LastFail.IsZero())
		require.Zero(t, member.Metric.ErrorSamples)
	}
	sample := qualitySpeedObservation(1, 99, e.now.Add(-time.Second), 1000)
	sample.ResetAt = e.now.Add(-30 * time.Second)
	prepared, _, _ := prepareGroupQualityObservations([]GroupQualityObservation{sample}, e.now, e.now.Add(-time.Hour), nil)
	require.Len(t, prepared, 1)
	require.False(t, prepared[0].StrictContinuation, "baseline contributors outside member set still obey their reset")
	sample.At = sample.ResetAt.Add(-time.Millisecond)
	prepared, _, _ = prepareGroupQualityObservations([]GroupQualityObservation{sample}, e.now, e.now.Add(-time.Hour), nil)
	require.Empty(t, prepared)
}
