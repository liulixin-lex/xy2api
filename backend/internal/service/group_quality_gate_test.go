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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/stretchr/testify/require"
)

type qualityTestRepository struct {
	policies       GroupQualityGatePolicies
	members        map[int64][]Account
	observations   []GroupQualityObservation
	observedAt     time.Time
	observationErr error
	froms          []time.Time
	memberLoads    int
	deadline       bool
	onLoad         func(context.Context)
}

func (r *qualityTestRepository) LoadPolicies(context.Context) (GroupQualityGatePolicies, error) {
	return r.policies, nil
}
func (r *qualityTestRepository) CompareAndSwapPolicies(_ context.Context, version int, next GroupQualityGatePolicies) (GroupQualityGatePolicies, error) {
	if version != r.policies.Version {
		return r.policies, ErrGroupQualityGateVersionConflict
	}
	r.policies = next
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
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	policy := defaultGroupQualityGatePolicy()
	policy.Enabled = true
	r := &qualityTestRepository{policies: GroupQualityGatePolicies{Groups: map[string]GroupQualityGatePolicy{"7": policy}}, members: map[int64][]Account{}}
	g := &qualityTestGroups{groups: []Group{{ID: 7, Platform: PlatformOpenAI}}}
	e := &qualityTestEnv{repo: r, groups: g, base: base, now: base}
	e.svc = NewGroupQualityGateService(r, &qualityTestAccounts{repo: r}, g)
	e.svc.processStartedAt = base.Add(-time.Hour)
	e.svc.now = func() time.Time { return e.now }
	e.accounts(1)
	oldLogger := slog.Default()
	oldWriter, oldFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&e.logs, nil)))
	// SetDefault also redirects the legacy log package; leave its output alone.
	log.SetOutput(oldWriter)
	log.SetFlags(oldFlags)
	t.Cleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return e
}
func (e *qualityTestEnv) accounts(ids ...int64) {
	e.repo.members[7] = nil
	for _, id := range ids {
		e.repo.members[7] = append(e.repo.members[7], Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1)})
	}
}
func (e *qualityTestEnv) calls(id int64, at time.Time, count, failures, ttft int, zeroCache bool) {
	for i := range count {
		o := GroupQualityObservation{AccountID: id, RequestID: fmt.Sprintf("%d-%d-%d", id, at.UnixNano(), i), At: at.Add(time.Duration(i) * time.Millisecond), SoftFailure: i < failures, Success: i >= failures}
		if ttft > 0 {
			o.Streaming, o.Warm, o.FirstTokenMs, o.PromptTokens = true, true, ttft, 4096
			if !zeroCache {
				o.CacheReadTokens = 2048
			}
		}
		e.repo.observations = append(e.repo.observations, o)
	}
}
func (e *qualityTestEnv) evaluate(t *testing.T, after time.Duration) {
	t.Helper()
	e.now = e.base.Add(after)
	e.svc.policyCache.Store(nil)
	err := e.svc.evaluate(context.Background())
	if e.repo.observationErr != nil {
		require.ErrorIs(t, err, e.repo.observationErr)
	} else {
		require.NoError(t, err)
	}
	require.True(t, e.repo.deadline, "every round must have an overall deadline")
	require.Equal(t, e.now.Add(-125*time.Minute), e.repo.froms[len(e.repo.froms)-1], "history must remain bounded")
}
func (e *qualityTestEnv) member(id int64) groupQualityMemberSnapshot {
	return e.svc.snapshot.Load().Members[7][id]
}
func (e *qualityTestEnv) state(t *testing.T, id int64, state string, strike, tier int) {
	t.Helper()
	m := e.member(id)
	require.Equal(t, state, m.Result.State)
	require.Equal(t, strike, m.State.Strike)
	require.Equal(t, tier, e.svc.OrderTier(7, id))
}
func (e *qualityTestEnv) reset(id int64, at time.Time) {
	for i := range e.repo.members[7] {
		if e.repo.members[7][i].ID == id {
			e.repo.members[7][i].Extra = map[string]any{"group_quality_reset_at": at.Format(time.RFC3339)}
		}
	}
}

// Each row runs the real evaluator for multiple rounds, including repository
// windows, resets, trial accumulation, guard selection and immutable snapshots.
func TestGroupQualityGateEvaluation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T, *qualityTestEnv)
	}{
		{"startup runs asynchronously and cancellation stops the first round", func(t *testing.T, e *qualityTestEnv) {
			e.evaluate(t, 0)
			e.evaluate(t, time.Minute)
			entered, returned := make(chan struct{}), make(chan struct{})
			e.repo.onLoad = func(ctx context.Context) { close(entered); <-ctx.Done() }
			go func() { e.svc.Start(); close(returned) }()
			defer e.svc.Stop()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("first round did not start")
			}
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("Start blocked on the first query")
			}
		}},
		{"backoff caps at thirty minutes", func(t *testing.T, e *qualityTestEnv) {
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			elapsed := time.Duration(0)
			for i, duration := range []time.Duration{5, 10, 20, 30, 30} {
				e.evaluate(t, elapsed)
				e.state(t, 1, "ejected", i+1, GroupQualityGateTierOther)
				require.Equal(t, duration*time.Minute, e.member(1).State.Until.Sub(e.now))
				elapsed += duration * time.Minute
				e.calls(1, e.base.Add(elapsed-time.Minute), 20, 5, 0, false)
			}
		}},
		{"direct recovery requires original metric samples", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2)
			e.calls(1, e.base.Add(-time.Minute), 20, 0, 25000, false)
			e.calls(2, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.calls(1, e.base.Add(time.Minute), 20, 0, 0, false)
			e.calls(2, e.base.Add(time.Minute), 100, 8, 0, false)
			e.evaluate(t, 5*time.Minute)
			e.state(t, 1, "trial", 1, GroupQualityGateTierTrial)
			e.state(t, 2, "eligible", 1, GroupQualityGateTierFast)
			require.Contains(t, e.logs.String(), "group_quality_gate.recover")
		}},
		{"borderline ttft enters trial then promotes with cleared tier", func(t *testing.T, e *qualityTestEnv) {
			e.calls(1, e.base.Add(-time.Minute), 20, 0, 25000, false)
			e.evaluate(t, 0)
			e.calls(1, e.base.Add(time.Minute), 10, 0, 18000, false)
			e.evaluate(t, 5*time.Minute)
			e.state(t, 1, "trial", 1, GroupQualityGateTierTrial)
			e.calls(1, e.base.Add(6*time.Minute), 20, 0, 0, false)
			e.evaluate(t, 7*time.Minute)
			e.state(t, 1, "eligible", 1, GroupQualityGateTierFast)
			require.Contains(t, e.logs.String(), "group_quality_gate.recover")
		}},
		{"old samples expire and two trial failures eject", func(t *testing.T, e *qualityTestEnv) {
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.evaluate(t, 5*time.Minute)
			e.state(t, 1, "trial", 1, GroupQualityGateTierTrial)
			require.Zero(t, e.member(1).Metric.Calls)
			e.calls(1, e.base.Add(6*time.Minute), 2, 2, 0, false)
			e.evaluate(t, 7*time.Minute)
			e.state(t, 1, "ejected", 2, GroupQualityGateTierOther)
		}},
		{"two hours without ejection resets strikes", func(t *testing.T, e *qualityTestEnv) {
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.evaluate(t, 5*time.Minute)
			e.evaluate(t, 125*time.Minute)
			e.state(t, 1, "trial", 0, GroupQualityGateTierTrial)
			e.calls(1, e.base.Add(126*time.Minute), 2, 2, 0, false)
			e.evaluate(t, 127*time.Minute)
			e.state(t, 1, "ejected", 1, GroupQualityGateTierOther)
		}},
		{"manual reset clears all groups and old metrics", func(t *testing.T, e *qualityTestEnv) {
			e.groups.groups = append(e.groups.groups, Group{ID: 8, Platform: PlatformOpenAI})
			e.repo.policies.Groups["8"] = e.repo.policies.Groups["7"]
			e.repo.members[8] = e.repo.members[7]
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.reset(1, e.base.Add(30*time.Second))
			e.evaluate(t, time.Minute)
			e.state(t, 1, "trial", 0, GroupQualityGateTierTrial)
			require.Equal(t, "trial", e.svc.snapshot.Load().Members[8][1].State.State)
			require.Zero(t, e.member(1).Metric.Calls)
			require.Equal(t, 2, e.repo.memberLoads, "one member query per round, regardless of group count")
			require.Contains(t, e.logs.String(), "group_quality_gate.trial")
		}},
		{"restart ignores historical reset and observations", func(t *testing.T, e *qualityTestEnv) {
			e.reset(1, e.base.Add(-7*24*time.Hour))
			e.svc.processStartedAt = e.base
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.evaluate(t, time.Minute)
			e.state(t, 1, "eligible", 0, GroupQualityGateTierFast)
			require.Zero(t, e.member(1).Metric.Calls)
			require.Equal(t, int64(20), e.svc.snapshot.Load().Metrics[1].Calls, "display retains full-window metrics")
		}},
		{"long idle trial stays bounded and accumulates older requests", func(t *testing.T, e *qualityTestEnv) {
			e.reset(1, e.base.Add(-time.Minute))
			e.evaluate(t, 0)
			e.calls(1, e.base.Add(time.Second), 10, 0, 0, false)
			e.evaluate(t, time.Minute)
			old := e.svc.snapshot.Load()
			e.evaluate(t, 24*time.Hour)
			require.Len(t, e.member(1).State.TrialRequests, 10)
			e.calls(1, e.base.Add(24*time.Hour+time.Second), 10, 0, 0, false)
			e.evaluate(t, 24*time.Hour+time.Minute)
			e.state(t, 1, "eligible", 0, GroupQualityGateTierFast)
			require.Len(t, old.Members[7][1].State.TrialRequests, 10, "published snapshots are immutable")
		}},
		{"trial failures persist across windows and retries deduplicate", func(t *testing.T, e *qualityTestEnv) {
			e.reset(1, e.base.Add(-time.Minute))
			e.evaluate(t, 0)
			e.calls(1, e.base.Add(time.Second), 1, 1, 0, false)
			e.repo.observations = append(e.repo.observations, e.repo.observations[0])
			e.evaluate(t, time.Minute)
			e.evaluate(t, 2*time.Minute)
			require.EqualValues(t, 1, e.member(1).State.TrialSoftFailures)
			require.Len(t, e.member(1).State.TrialRequests, 1)
			e.calls(1, e.base.Add(4*time.Hour+time.Second), 1, 1, 0, false)
			e.evaluate(t, 4*time.Hour+time.Minute)
			e.state(t, 1, "ejected", 1, GroupQualityGateTierOther)
		}},
		{"failures after first twenty requests do not fail trial", func(t *testing.T, e *qualityTestEnv) {
			e.reset(1, e.base.Add(-time.Minute))
			e.evaluate(t, 0)
			e.calls(1, e.base.Add(time.Second), 20, 0, 0, false)
			e.calls(1, e.base.Add(2*time.Second), 2, 2, 0, false)
			e.evaluate(t, time.Minute)
			e.state(t, 1, "eligible", 0, GroupQualityGateTierFast)
		}},
		{"guard retains least severe despite post-ejection data", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(5, 4, 3, 2, 1)
			e.calls(1, e.base.Add(-time.Minute), 50, 6, 0, false)
			e.calls(2, e.base.Add(-time.Minute), 20, 10, 0, false)
			e.calls(3, e.base.Add(-time.Minute), 20, 12, 0, false)
			e.evaluate(t, 0)
			require.True(t, e.member(1).Result.GuardHeld)
			e.calls(1, e.base.Add(time.Second), 12, 0, 8000, false)
			e.evaluate(t, time.Minute)
			e.evaluate(t, 2*time.Minute)
			require.True(t, e.member(1).Result.GuardHeld)
			require.True(t, e.svc.Vetoed(7, 2))
			require.True(t, e.svc.Vetoed(7, 3))
		}},
		{"guard ties prefer prior holder then account id", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(5, 4, 3, 2, 1)
			e.repo.members[7][4].Schedulable = false
			for _, id := range []int64{1, 2, 3} {
				e.calls(id, e.base.Add(-time.Minute), 20, 5, 0, false)
			}
			e.repo.members[7][1].Schedulable = false // only healthy 5 and failing 2/3 usable
			e.evaluate(t, 0)
			require.True(t, e.member(2).Result.GuardHeld)
			e.repo.members[7][4].Schedulable = true
			e.repo.members[7][1].Schedulable = true
			e.evaluate(t, time.Minute)
			require.True(t, e.member(2).Result.GuardHeld)
			require.False(t, e.member(1).Result.GuardHeld)
		}},
		{"pinned failures excluded from countable guard floor", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2, 3)
			p := e.repo.policies.Groups["7"]
			p.PinnedAccountIDs = []int64{1, 2}
			e.repo.policies.Groups["7"] = p
			for _, id := range []int64{1, 2, 3} {
				e.calls(id, e.base.Add(-time.Minute), 20, 5, 0, false)
			}
			e.evaluate(t, 0)
			e.evaluate(t, time.Minute)
			for _, id := range []int64{1, 2} {
				require.False(t, e.svc.Vetoed(7, id))
				require.False(t, e.member(id).Result.Passing)
				require.False(t, e.member(id).Result.GuardHeld)
			}
			require.True(t, e.member(3).Result.GuardHeld)
			p.PinnedAccountIDs = []int64{1, 2, 3}
			e.repo.policies.Groups["7"] = p
			e.evaluate(t, 2*time.Minute)
			for _, id := range []int64{1, 2, 3} {
				require.False(t, e.member(id).Result.GuardHeld)
				require.False(t, e.svc.Vetoed(7, id))
			}
		}},
		{"single usable member retained; hard and profit failures excluded", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2, 3, 4, 5, 6, 7, 8)
			group := &e.groups.groups[0]
			group.ProfitControlEnabled = true
			group.RateMultiplier = 1.25
			group.ProfitMinMargin = 0.2
			accounts := e.repo.members[7]
			accounts[1].Status = StatusDisabled
			accounts[2].Schedulable = false
			accounts[3].TempUnschedulableUntil = groupQualityTimePtr(e.base.Add(time.Hour))
			accounts[3].TempUnschedulableReason = "balance:exhausted"
			accounts[4].RateMultiplier = f64ptr(2)
			accounts[5].RateMultiplier = nil
			accounts[6].Status = StatusError
			accounts[7].AutoPauseOnExpired = true
			accounts[7].ExpiresAt = groupQualityTimePtr(e.base.Add(-time.Hour))
			for _, a := range accounts {
				e.calls(a.ID, e.base.Add(-time.Minute), 20, 5, 0, false)
			}
			e.evaluate(t, 0)
			e.evaluate(t, time.Minute)
			require.True(t, e.member(1).Result.Usable)
			require.True(t, e.member(1).Result.GuardHeld)
			for _, id := range []int64{2, 3, 4, 5, 6, 7, 8} {
				require.False(t, e.member(id).Result.Usable)
				require.False(t, e.member(id).Result.GuardHeld)
			}
		}},
		{"stale suppresses ejection but permits expiry and reset once", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2, 3)
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.repo.observedAt = e.base.Add(-10 * time.Minute)
			e.calls(2, e.base.Add(time.Second), 20, 5, 0, false)
			e.reset(3, e.base.Add(30*time.Second))
			e.evaluate(t, time.Minute)
			e.state(t, 2, "eligible", 0, GroupQualityGateTierFast)
			e.state(t, 3, "trial", 0, GroupQualityGateTierTrial)
			e.calls(3, e.base.Add(2*time.Minute), 2, 2, 0, false)
			e.evaluate(t, 5*time.Minute)
			e.state(t, 1, "trial", 1, GroupQualityGateTierTrial)
			e.state(t, 3, "trial", 0, GroupQualityGateTierTrial)
			require.Equal(t, 1, strings.Count(e.logs.String(), "group_quality_gate.evaluate_failed"))
			require.Contains(t, e.logs.String(), "stale_duration")
			require.Contains(t, e.logs.String(), "usage data timestamp is stale")
			e.repo.observedAt = time.Time{}
			e.evaluate(t, 6*time.Minute)
			e.state(t, 3, "ejected", 1, GroupQualityGateTierOther)
			require.False(t, e.svc.snapshot.Load().Stale)
		}},
		{"query failure preserves ejection and applies reset", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2)
			for _, id := range []int64{1, 2} {
				e.calls(id, e.base.Add(-time.Minute), 20, 5, 0, false)
			}
			e.evaluate(t, 0)
			e.repo.observationErr = errors.New("synthetic query timeout")
			e.reset(2, e.base.Add(30*time.Second))
			e.evaluate(t, time.Minute)
			e.state(t, 1, "ejected", 1, GroupQualityGateTierOther)
			e.state(t, 2, "trial", 0, GroupQualityGateTierTrial)
			e.repo.observations = nil
			e.evaluate(t, 5*time.Minute)
			e.state(t, 1, "trial", 1, GroupQualityGateTierTrial)
		}},
		{"zero cache and fast line only affect tier, disabled groups too", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2, 3)
			e.groups.groups = append(e.groups.groups, Group{ID: 8, Platform: PlatformOpenAI})
			e.repo.members[8] = e.repo.members[7]
			e.calls(1, e.base.Add(-time.Minute), 20, 0, 8000, true)
			e.calls(2, e.base.Add(-time.Minute), 10, 0, 15000, false)
			e.calls(3, e.base.Add(-time.Minute), 19, 0, 8000, true)
			e.evaluate(t, 0)
			e.evaluate(t, time.Minute)
			for _, id := range []int64{1, 2} {
				e.state(t, id, "eligible", 0, GroupQualityGateTierOther)
				require.Equal(t, GroupQualityGateTierOther, e.svc.OrderTier(8, id))
			}
			e.state(t, 3, "eligible", 0, GroupQualityGateTierFast)
		}},
		{"insufficient samples retain slow tier for six hours", func(t *testing.T, e *qualityTestEnv) {
			e.calls(1, e.base.Add(-time.Minute), 10, 0, 15000, false)
			e.evaluate(t, 0)
			for i := 1; i <= 13; i++ {
				at := time.Duration(i) * 30 * time.Minute
				e.calls(1, e.base.Add(at-time.Second), 1, 0, 8000, false)
				e.evaluate(t, at)
				want := GroupQualityGateTierOther
				if i == 13 {
					want = GroupQualityGateTierFast
				}
				require.Equal(t, want, e.svc.OrderTier(7, 1))
			}
		}},
		{"status arrays, summaries, effective expiry and headroom", func(t *testing.T, e *qualityTestEnv) {
			e.accounts(1, 2, 3)
			e.calls(1, e.base.Add(-time.Minute), 20, 5, 0, false)
			e.evaluate(t, 0)
			e.now = e.base.Add(5 * time.Minute)
			status, err := e.svc.Status(context.Background(), nil)
			require.NoError(t, err)
			require.Equal(t, GroupQualitySummaryView{Eligible: 3, Trial: 1}, status.Groups[0].Summary)
			row := status.Groups[0].Accounts[0]
			require.Equal(t, "trial", row.State)
			require.True(t, row.Passing)
			require.NotNil(t, row.Reasons)
			require.Empty(t, row.Reasons)
			require.Nil(t, status.Groups[0].Accounts[1].Headroom)
			raw, err := json.Marshal(status)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"headroom":null`)
			require.Contains(t, string(raw), `"reasons":[]`)
			id := int64(8)
			e.groups.groups = append(e.groups.groups, Group{ID: id, Platform: PlatformOpenAI})
			e.repo.members[id] = e.repo.members[7]
			status, err = e.svc.Status(context.Background(), &id)
			require.NoError(t, err)
			require.False(t, status.Groups[0].Enabled)
			require.NotNil(t, status.Groups[0].Accounts[0].Reasons)
			e.repo.members[id] = nil
			status, err = e.svc.Status(context.Background(), &id)
			require.NoError(t, err)
			require.NotNil(t, status.Groups[0].Accounts)
			require.NotNil(t, status.AccountMetrics)
			e.repo.policies.Groups = map[string]GroupQualityGatePolicy{}
			e.evaluate(t, 6*time.Minute)
			status, err = e.svc.Status(context.Background(), nil)
			require.NoError(t, err)
			raw, err = json.Marshal(status)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"groups":[]`)
			require.Contains(t, string(raw), `"account_metrics":{}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, newQualityTestEnv(t)) })
	}
}

func TestGroupQualityGatePolicyValidationAndStatusContract(t *testing.T) {
	for _, tc := range []struct {
		name, field, message string
		policy               GroupQualityGatePolicy
		platform             string
	}{
		{"error rate", "max_error_rate", "max_error_rate must be in (0,1]", GroupQualityGatePolicy{MaxErrorRate: 1.1}, PlatformOpenAI},
		{"fast minimum", "fast_warm_ttft_p90_ms", "at least 1000", GroupQualityGatePolicy{FastWarmTTFTP90Ms: 999}, PlatformOpenAI},
		{"ejection maximum", "max_warm_ttft_p90_ms", "not exceed 600000", GroupQualityGatePolicy{MaxWarmTTFTP90Ms: 600001}, PlatformOpenAI},
		{"threshold order", "max_warm_ttft_p90_ms", "greater than fast_warm_ttft_p90_ms", GroupQualityGatePolicy{MaxWarmTTFTP90Ms: 9000}, PlatformOpenAI},
		{"pinned nonmember", "pinned_account_ids", "pinned account 42 is not a member of group 7", GroupQualityGatePolicy{PinnedAccountIDs: []int64{42}}, PlatformOpenAI},
		{"platform", "enabled", "only be enabled for OpenAI groups", GroupQualityGatePolicy{Enabled: true}, PlatformAnthropic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newQualityTestEnv(t)
			e.groups.groups[0].Platform = tc.platform
			_, err := e.svc.UpdatePolicy(context.Background(), 7, 0, tc.policy)
			require.ErrorIs(t, err, ErrInvalidGroupQualityGatePolicy)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			require.True(t, response.ErrorFrom(c, err))
			require.Equal(t, http.StatusBadRequest, w.Code)
			var body infraerrors.Status
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, "INVALID_GROUP_QUALITY_GATE_POLICY", body.Reason)
			require.Contains(t, body.Message, tc.message)
			require.Equal(t, tc.field, body.Metadata["field"])
		})
	}
	e := newQualityTestEnv(t)
	_, err := e.svc.Policies(context.Background())
	require.NoError(t, err)
	require.NotNil(t, e.svc.policyCache.Load())
	_, err = e.svc.UpdatePolicy(context.Background(), 7, 0, GroupQualityGatePolicy{Enabled: true})
	require.NoError(t, err)
	require.Nil(t, e.svc.policyCache.Load())
	_, err = e.svc.UpdatePolicy(context.Background(), 7, 0, GroupQualityGatePolicy{})
	require.ErrorIs(t, err, ErrGroupQualityGateVersionConflict)
	var nilService *GroupQualityGateService
	status, err := nilService.Status(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, status.Groups)
	require.NotNil(t, status.AccountMetrics)
	policy := defaultGroupQualityGatePolicy()
	for _, m := range []GroupQualityMetrics{{}, {Calls: 19, SoftFailures: 5, WarmSamples: 9}, {Calls: 20, SoftFailures: 4}} {
		require.Nil(t, qualityHeadroom(policy, m))
	}
	require.InDelta(t, 0, *qualityHeadroom(policy, GroupQualityMetrics{Calls: 50, SoftFailures: 5}), 1e-9)
	require.InDelta(t, 0.5, *qualityHeadroom(policy, GroupQualityMetrics{WarmSamples: 10, WarmTTFTP90Ms: 10000}), 1e-9)
	require.InDelta(t, -1.5, *qualityHeadroom(policy, GroupQualityMetrics{Calls: 20, SoftFailures: 5, WarmSamples: 10, WarmTTFTP90Ms: 10000}), 1e-9)
}

func TestGroupMemberServableUsesSharedResilienceAndProfitThreshold(t *testing.T) {
	now := time.Now()
	account := Account{ID: 11, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1), TempUnschedulableUntil: groupQualityTimePtr(now.Add(time.Minute)), TempUnschedulableReason: "transient:synthetic"}
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	require.True(t, groupMemberUsable(group, &account, now))
	require.False(t, groupMemberServable(group, &account, now, nil, nil))
	account.TempUnschedulableUntil = nil
	group.ProfitControlEnabled = true
	group.RateMultiplier = 1
	group.ProfitMinMargin = 0.2
	for _, cost := range []float64{0, 0.8, 0.81, -1, math.NaN(), math.Inf(1)} {
		account.RateMultiplier = &cost
		want := cost >= 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) && !profitControlOverThreshold(cost, 0.8)
		require.Equal(t, want, groupMemberUsable(group, &account, now))
	}
	account.RateMultiplier = nil
	require.False(t, groupMemberUsable(group, &account, now))
}

func f64ptr(v float64) *float64                  { return &v }
func groupQualityTimePtr(v time.Time) *time.Time { return &v }
