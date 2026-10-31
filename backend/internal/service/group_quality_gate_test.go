package service

import (
	"testing"
	"time"
)

func TestGroupQualityGatePolicyAndEvaluation(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	policy := defaultGroupQualityGatePolicy()
	tests := []struct {
		name       string
		metric     GroupQualityMetrics
		previous   groupQualityAccountState
		wantState  string
		wantStrike int
		wantTier   int
	}{
		{"first ejection", GroupQualityMetrics{Calls: 20, SoftFailures: 5}, groupQualityAccountState{}, "ejected", 1, GroupQualityGateTierOther},
		{"backoff second strike", GroupQualityMetrics{Calls: 20, SoftFailures: 5}, groupQualityAccountState{State: "ejected", Strike: 1, Until: now.Add(-time.Minute), LastEjected: now.Add(-time.Minute)}, "ejected", 2, GroupQualityGateTierOther},
		{"borderline becomes trial", GroupQualityMetrics{Calls: 20, SoftFailures: 3}, groupQualityAccountState{State: "ejected", Strike: 1, Until: now.Add(-time.Minute), LastEjected: now.Add(-time.Minute)}, "trial", 1, GroupQualityGateTierTrial},
		{"clean recovery", GroupQualityMetrics{Calls: 20, SoftFailures: 1}, groupQualityAccountState{State: "ejected", Strike: 1, Until: now.Add(-time.Minute), LastEjected: now.Add(-time.Minute), LastEjectionEnd: now.Add(-time.Minute)}, "eligible", 1, GroupQualityGateTierFast},
		{"trial failure", GroupQualityMetrics{TrialRequests: 2, TrialSoftFailures: 2}, groupQualityAccountState{State: "trial", Strike: 1, TrialSince: now.Add(-time.Hour)}, "ejected", 2, GroupQualityGateTierOther},
		{"trial promotion", GroupQualityMetrics{TrialRequests: 20}, groupQualityAccountState{State: "trial", TrialSince: now.Add(-time.Hour)}, "eligible", 0, GroupQualityGateTierFast},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, _ := evaluateGroupQualityMember(now, policy, test.metric, test.previous, false)
			if state.State != test.wantState || state.Strike != test.wantStrike || state.Tier != test.wantTier {
				t.Fatalf("got state=%q strike=%d tier=%d", state.State, state.Strike, state.Tier)
			}
		})
	}
}

func TestGroupQualityGatePolicyValidationAndGuard(t *testing.T) {
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	if err := ValidateGroupQualityGatePolicy(GroupQualityGatePolicy{Enabled: true, PinnedAccountIDs: []int64{2}}, group, []int64{1}); err == nil {
		t.Fatal("expected pinned non-member validation error")
	}
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1)}, {ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1)}, {ID: 3, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1)}}
	members := map[int64]groupQualityMemberSnapshot{}
	policy := defaultGroupQualityGatePolicy()
	metrics := map[int64]GroupQualityMetrics{
		1: {Calls: 20, SoftFailures: 5},
		2: {Calls: 20, SoftFailures: 15},
		3: {WarmSamples: 10, WarmTTFTP90Ms: 30000},
	}
	for _, account := range accounts {
		members[account.ID] = groupQualityMemberSnapshot{AccountID: account.ID, Metric: metrics[account.ID], State: groupQualityAccountState{State: "ejected"}, Result: GroupQualityMemberResult{Usable: true, Passing: false, State: "ejected"}}
	}
	applyQualityGuard(accounts, members, policy)
	held := 0
	for _, member := range members {
		if member.Result.GuardHeld {
			held++
		}
	}
	if held != 2 || !members[1].Result.GuardHeld || members[2].Result.GuardHeld || !members[3].Result.GuardHeld {
		t.Fatalf("guard selections = {%t,%t,%t}; want accounts 1 and 3", members[1].Result.GuardHeld, members[2].Result.GuardHeld, members[3].Result.GuardHeld)
	}
}

func TestGroupMemberUsableExcludesHardStatesAndProfitViolations(t *testing.T) {
	now := time.Now()
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	base := Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1)}
	profitGroup := &Group{ID: 7, Platform: PlatformOpenAI, ProfitControlEnabled: true, RateMultiplier: 1, ProfitMinMargin: 0.2}
	softUntil := now.Add(time.Minute)
	tests := []struct {
		name  string
		group *Group
		acc   Account
		want  bool
	}{
		{"active", group, base, true},
		{"disabled", group, Account{ID: 2, Platform: PlatformOpenAI, Status: StatusDisabled, Schedulable: true, RateMultiplier: f64ptr(1)}, false},
		{"unschedulable", group, Account{ID: 3, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: false, RateMultiplier: f64ptr(1)}, false},
		{"hard balance pause", group, Account{ID: 4, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1), TempUnschedulableUntil: &softUntil, TempUnschedulableReason: "balance:exhausted"}, false},
		{"profit within ceiling", profitGroup, Account{ID: 5, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(0.8)}, true},
		{"profit over ceiling", profitGroup, Account{ID: 6, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(0.81)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := groupMemberUsable(test.group, &test.acc, now); got != test.want {
				t.Fatalf("groupMemberUsable() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestGroupQualityGateOrderingTier(t *testing.T) {
	now := time.Now()
	policy := defaultGroupQualityGatePolicy()
	tests := []struct {
		name    string
		metric  GroupQualityMetrics
		state   groupQualityAccountState
		prior   groupQualityAccountState
		enabled bool
		want    int
	}{
		{"no TTFT data is fast", GroupQualityMetrics{}, groupQualityAccountState{State: "eligible"}, groupQualityAccountState{}, true, GroupQualityGateTierFast},
		{"trial overrides old tier", GroupQualityMetrics{WarmSamples: 30, WarmTTFTP90Ms: 1000}, groupQualityAccountState{State: "trial"}, groupQualityAccountState{State: "eligible", Tier: GroupQualityGateTierOther, TierAt: now}, true, GroupQualityGateTierTrial},
		{"zero cache over 30 percent is tier two", GroupQualityMetrics{WarmSamples: 10, WarmTTFTP90Ms: 1000, ZeroCacheSamples: 20, WarmZeroCacheRate: 0.31}, groupQualityAccountState{State: "eligible"}, groupQualityAccountState{}, true, GroupQualityGateTierOther},
		{"sample shortage carries slow tier", GroupQualityMetrics{WarmSamples: 5}, groupQualityAccountState{State: "eligible"}, groupQualityAccountState{State: "eligible", Tier: GroupQualityGateTierOther, TierAt: now.Add(-time.Hour)}, true, GroupQualityGateTierOther},
		{"sample memory expires after six hours", GroupQualityMetrics{WarmSamples: 5}, groupQualityAccountState{State: "eligible"}, groupQualityAccountState{State: "eligible", Tier: GroupQualityGateTierOther, TierAt: now.Add(-7 * time.Hour)}, true, GroupQualityGateTierFast},
		{"ejection clears prior slow tier", GroupQualityMetrics{WarmSamples: 5}, groupQualityAccountState{State: "eligible"}, groupQualityAccountState{State: "ejected", Tier: GroupQualityGateTierOther, TierAt: now.Add(-time.Minute)}, true, GroupQualityGateTierFast},
		{"fast line is not ejection line", GroupQualityMetrics{WarmSamples: 10, WarmTTFTP90Ms: 15000}, groupQualityAccountState{State: "eligible"}, groupQualityAccountState{}, true, GroupQualityGateTierOther},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveGroupQualityTier(now, policy, test.metric, test.state, test.prior, test.enabled, true); got != test.want {
				t.Fatalf("tier=%d, want %d", got, test.want)
			}
		})
	}
}

func TestGroupQualityGateTrialCountsPastThirtyFiveMinutes(t *testing.T) {
	now := time.Now().UTC()
	policy := defaultGroupQualityGatePolicy()
	for _, tc := range []struct {
		name            string
		failureFrom     int
		failureTo       int
		wantState       string
		wantTrialErrors int64
	}{
		{"two failures in the first twenty eject after a long trial", 0, 2, "ejected", 2},
		{"failures after the first twenty do not prevent promotion", 20, 22, "eligible", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trialSince := now.Add(-44 * time.Minute)
			observations := make([]GroupQualityObservation, 0, 22)
			for i := range 22 {
				observation := GroupQualityObservation{AccountID: 1, RequestID: string(rune('a' + i)), At: trialSince.Add(time.Nanosecond + time.Duration(i)*2*time.Minute)}
				if i >= tc.failureFrom && i < tc.failureTo {
					observation.SoftFailure = true
				} else {
					observation.Success = true
				}
				observations = append(observations, observation)
			}
			metrics := groupQualityMetricsFor(observations, now, time.Time{}, time.Time{}, time.Time{}, trialSince)
			if metrics.TrialRequests != 22 || metrics.TrialSoftFailures != tc.wantTrialErrors {
				t.Fatalf("trial counts=%d failures=%d, want 22/%d", metrics.TrialRequests, metrics.TrialSoftFailures, tc.wantTrialErrors)
			}
			state, _ := evaluateGroupQualityMember(now, policy, metrics, groupQualityAccountState{State: "trial", TrialSince: trialSince}, false)
			if state.State != tc.wantState {
				t.Fatalf("state=%q, want %q", state.State, tc.wantState)
			}
		})
	}
}

func TestGroupMemberServableUsesDefaultSoftPenaltyWhenPenaltyIsNil(t *testing.T) {
	now := time.Now()
	account := Account{ID: 11, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, RateMultiplier: f64ptr(1), TempUnschedulableUntil: groupQualityTimePtr(now.Add(time.Minute)), TempUnschedulableReason: "transient:synthetic"}
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	if groupMemberUsable(group, &account, now) != true {
		t.Fatal("soft resilience penalties must not affect groupMemberUsable")
	}
	if groupMemberServable(group, &account, now, nil, nil) {
		t.Fatal("nil penalty must use the default classifier and reject soft-penalty accounts")
	}
}

func f64ptr(v float64) *float64 { return &v }

func groupQualityTimePtr(v time.Time) *time.Time { return &v }
