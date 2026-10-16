//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

// resilienceGateLimits holds the window cost, RPM and session counts that the
// Anthropic OAuth gates read.
type resilienceGateLimits struct {
	SessionLimitCache
	windowCost map[int64]float64
	rpm        map[int64]int
	full       map[int64]bool
}

func newResilienceGateLimits() *resilienceGateLimits {
	return &resilienceGateLimits{windowCost: map[int64]float64{}, rpm: map[int64]int{}, full: map[int64]bool{}}
}

func (l *resilienceGateLimits) GetWindowCost(_ context.Context, id int64) (float64, bool, error) {
	cost, ok := l.windowCost[id]
	return cost, ok, nil
}

func (l *resilienceGateLimits) GetWindowCostBatch(_ context.Context, ids []int64) (map[int64]float64, error) {
	out := map[int64]float64{}
	for _, id := range ids {
		if cost, ok := l.windowCost[id]; ok {
			out[id] = cost
		}
	}
	return out, nil
}

func (l *resilienceGateLimits) SetWindowCost(context.Context, int64, float64) error { return nil }

func (l *resilienceGateLimits) RegisterSession(_ context.Context, id int64, _ string, _ int, _ time.Duration) (bool, error) {
	return !l.full[id], nil
}

func (l *resilienceGateLimits) GetRPM(_ context.Context, id int64) (int, error) {
	return l.rpm[id], nil
}

func (l *resilienceGateLimits) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	for _, id := range ids {
		out[id] = l.rpm[id]
	}
	return out, nil
}

func (l *resilienceGateLimits) IncrementRPM(_ context.Context, id int64) (int, error) {
	l.rpm[id]++
	return l.rpm[id], nil
}

// resilienceGateRow is one account that violates exactly one original gate.
type resilienceGateRow struct {
	name string
	// account makes the account violate the gate.
	account func(a *Account, now time.Time)
	// setup installs the service state the gate reads.
	setup func(svc *GatewayService, limits *resilienceGateLimits)
	// ctx adds request state the gate reads.
	ctx func(ctx context.Context) context.Context
	// share is the reason the share adapter gives the candidate; empty when
	// the gate applies at admission (session limit).
	share sharesched.Reason
	// unlisted marks states the schedulable listing already drops.
	unlisted bool
}

const (
	gateTestAccount = int64(1)
	gateTestModel   = "claude-sonnet-4-5"
	gateTestSession = "sess-gate"
)

func resilienceGateRows() []resilienceGateRow {
	oauth := func(extra map[string]any) func(*Account, time.Time) {
		return func(a *Account, _ time.Time) {
			a.Type = AccountTypeOAuth
			a.Extra = extra
		}
	}
	return []resilienceGateRow{
		{name: "platform", share: sharesched.ReasonPlatformMismatch, unlisted: true,
			account: func(a *Account, _ time.Time) { a.Platform = PlatformAntigravity }},
		{name: "model", share: sharesched.ReasonModelUnsupported,
			account: func(a *Account, _ time.Time) {
				a.Credentials = map[string]any{"model_mapping": map[string]any{"claude-other": "claude-other"}}
			}},
		{name: "channel_restriction", share: sharesched.ReasonChannelRestricted,
			account: func(a *Account, _ time.Time) {
				a.Credentials = map[string]any{"model_mapping": map[string]any{gateTestModel: "claude-unpriced"}}
			},
			setup: func(svc *GatewayService, _ *resilienceGateLimits) {
				svc.channelService = newTestChannelService(makeStandardRepo(Channel{
					ID: 1, Status: StatusActive, GroupIDs: []int64{shareTestGroupID}, RestrictModels: true,
					BillingModelSource: BillingModelSourceUpstream,
					ModelPricing:       []ChannelModelPricing{{Platform: PlatformAnthropic, Models: []string{gateTestModel}}},
				}, map[int64]string{shareTestGroupID: PlatformAnthropic}))
			}},
		{name: "profit_gate", share: sharesched.ReasonProfitGate,
			account: func(a *Account, _ time.Time) { rate := 5.0; a.RateMultiplier = &rate },
			ctx: func(ctx context.Context) context.Context {
				return context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
					groupID: shareTestGroupID, platform: PlatformAnthropic, threshold: 1, pricingAt: time.Now()})
			}},
		{name: "quota", share: sharesched.ReasonQuotaExceeded, unlisted: true,
			account: func(a *Account, _ time.Time) { a.Extra = map[string]any{"quota_limit": 10.0, "quota_used": 10.0} }},
		{name: "window_cost", share: sharesched.ReasonWindowCost,
			account: oauth(map[string]any{"window_cost_limit": 10.0}),
			setup:   func(_ *GatewayService, l *resilienceGateLimits) { l.windowCost[gateTestAccount] = 100 }},
		{name: "rpm", share: sharesched.ReasonRPMLimit,
			account: oauth(map[string]any{"base_rpm": 10}),
			setup:   func(_ *GatewayService, l *resilienceGateLimits) { l.rpm[gateTestAccount] = 100 }},
		{name: "session_limit",
			account: oauth(map[string]any{"max_sessions": 1}),
			setup:   func(_ *GatewayService, l *resilienceGateLimits) { l.full[gateTestAccount] = true }},
		{name: "model_rate_limit_hard", share: sharesched.ReasonModelRateLimited,
			account: func(a *Account, now time.Time) {
				a.Extra = map[string]any{modelRateLimitsKey: map[string]any{
					gateTestModel: map[string]any{"rate_limit_reset_at": now.Add(time.Hour).Format(time.RFC3339)}}}
			}},
		{name: "hard_state", share: sharesched.ReasonBalanceExhausted, unlisted: true,
			account: func(a *Account, now time.Time) {
				until := now.Add(time.Hour)
				a.TempUnschedulableUntil, a.TempUnschedulableReason = &until, resilience.ReasonPrefixBalanceExhausted+": HTTP 402"
			}},
	}
}

// newResilienceGateEnv builds a one-account group on the given scheduler with
// the account changed by mutate.
func newResilienceGateEnv(t *testing.T, scheduler sharesched.Scheduler, mutate func(*Account, time.Time), setup func(*GatewayService, *resilienceGateLimits)) *shareTestEnv {
	t.Helper()
	withResilienceSettings(t, nil)
	env := newShareTestEnv(t, shareTestPolicy(scheduler, sharesched.AccountRule{AccountID: gateTestAccount, Tier: 1, Weight: 1}),
		Account{ID: gateTestAccount, Priority: 1})
	if mutate != nil {
		mutate(env.repo.accountsByID[gateTestAccount], time.Now())
	}
	limits := newResilienceGateLimits()
	env.gateway.sessionLimitCache = limits
	env.gateway.rpmCache = limits
	env.gateway.accountRepo = groupListingRepo{env.repo}
	env.gateway.resilience = NewResilienceService(nil, newMemResilienceStore())
	if setup != nil {
		setup(env.gateway, limits)
	}
	return env
}

func softPenalise(a *Account, now time.Time) {
	until := now.Add(30 * time.Second)
	a.TempUnschedulableUntil, a.TempUnschedulableReason = &until, resilience.ReasonPrefixTransient+"TRANSIENT HTTP 502"
}

// TestResilienceGateConsistency covers review F4: an account that fails one
// original gate is rejected by the original scheduler, by the share
// candidates and by last-resort selection, on both schedulers. Last resort
// relaxes soft penalties only, so the same account with a soft cooldown on
// top must still be rejected.
func TestResilienceGateConsistency(t *testing.T) {
	for _, row := range resilienceGateRows() {
		for _, scheduler := range []sharesched.Scheduler{sharesched.SchedulerSub2API, sharesched.SchedulerShare} {
			for _, soft := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/soft=%v", row.name, scheduler, soft), func(t *testing.T) {
					mutate := row.account
					if soft {
						mutate = func(a *Account, now time.Time) {
							row.account(a, now)
							if a.TempUnschedulableUntil == nil {
								softPenalise(a, now)
							}
						}
					}
					env := newResilienceGateEnv(t, scheduler, mutate, row.setup)
					st := resilience.NewRequestState()
					ctx := resilience.WithRequestState(cacheableContext(), st)
					if row.ctx != nil {
						ctx = row.ctx(ctx)
					}
					gid := shareTestGroupID
					result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, gateTestSession, gateTestModel, nil, "", 0)
					if err == nil && result != nil && result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
					require.ErrorIs(t, err, ErrNoAvailableAccounts, "the gate rejects the only account")
					require.Zero(t, st.LastResort(), "last resort does not bypass the gate")
				})
			}
		}
		t.Run(row.name+"/share_candidate", func(t *testing.T) {
			env := newResilienceGateEnv(t, sharesched.SchedulerShare, row.account, row.setup)
			ctx := cacheableContext()
			if row.ctx != nil {
				ctx = row.ctx(ctx)
			}
			gid := shareTestGroupID
			group := &Group{ID: shareTestGroupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
			_, in, err := env.gateway.shareGatewayCandidates(ctx, group, &gid, gateTestModel, nil, 0)
			require.NoError(t, err)
			var cand *sharesched.Candidate
			for i := range in.cands {
				if in.cands[i].AccountID == gateTestAccount {
					cand = &in.cands[i]
				}
			}
			if row.unlisted {
				require.Nil(t, cand, "dropped by the schedulable listing")
				gates := env.gateway.newGatewayCandidateGates(ctx, &gid, PlatformAnthropic, true, gateTestModel)
				reason, _ := gates.candidateReason(ctx, env.repo.accountsByID[gateTestAccount], false, time.Now())
				require.Equal(t, row.share, reason, "and rejected by the shared gates")
				return
			}
			require.NotNil(t, cand)
			if row.share == "" {
				require.True(t, cand.Eligible, "admission gates apply when the plan is served")
				return
			}
			require.False(t, cand.Eligible)
			require.Equal(t, row.share, cand.Reason)
		})
	}
}

// limitRecordingConcurrency records the slot limit of every acquisition;
// accounts in busy have no free slot.
type limitRecordingConcurrency struct {
	schedulerTestConcurrencyCache
	limits map[int64][]int
	busy   map[int64]bool
}

func (c *limitRecordingConcurrency) AcquireAccountSlot(_ context.Context, id int64, limit int, _ string) (bool, error) {
	c.limits[id] = append(c.limits[id], limit)
	return !c.busy[id], nil
}

// TestResilienceOpenAILastResortUsesCodexAdaptiveConcurrency covers review
// F4: a Codex account under upstream pressure takes last-resort traffic only
// up to its adaptive limit, for the slot and for the wait plan.
func TestResilienceOpenAILastResortUsesCodexAdaptiveConcurrency(t *testing.T) {
	withResilienceSettings(t, nil)
	const codexAccount = int64(91)
	until := time.Now().Add(45 * time.Second)
	accounts := []Account{{
		ID: codexAccount, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Concurrency: 5, RateLimitResetAt: &until,
		Extra: map[string]any{codexPressureKey: map[string]any{"limit": 1}},
	}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	cfg.Gateway.OpenAICodexTicket.AdaptiveConcurrencyAccountIDs = []int64{codexAccount}
	conc := &limitRecordingConcurrency{limits: map[int64][]int{}, busy: map[int64]bool{}}
	svc := &OpenAIGatewayService{
		accountRepo:        openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(conc),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	gid := int64(9)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	sel, _, err := svc.SelectAccountWithScheduler(ctx, &gid, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, codexAccount, sel.Account.ID)
	require.Equal(t, codexAccount, st.LastResort())
	require.Equal(t, []int{1}, conc.limits[codexAccount], "the slot is taken at the adaptive limit")
	sel.ReleaseFunc()

	conc.busy[codexAccount] = true
	sel, _, err = svc.SelectAccountWithScheduler(ctx, &gid, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel.WaitPlan)
	require.Equal(t, 1, sel.WaitPlan.MaxConcurrency, "the wait plan queues at the adaptive limit")
}

// TestResilienceLastResortRelaxesSoftPenaltiesOnly is the counterpart of the
// gate table: soft penalties, including a short per-model rate limit, are
// the only restrictions last resort relaxes.
func TestResilienceLastResortRelaxesSoftPenaltiesOnly(t *testing.T) {
	soft := map[string]func(*Account, time.Time){
		"cooldown": softPenalise,
		"model_rate_limit": func(a *Account, now time.Time) {
			a.Extra = map[string]any{modelRateLimitsKey: map[string]any{
				gateTestModel: map[string]any{"rate_limit_reset_at": now.Add(time.Minute).Format(time.RFC3339)}}}
		},
	}
	for name, mutate := range soft {
		for _, scheduler := range []sharesched.Scheduler{sharesched.SchedulerSub2API, sharesched.SchedulerShare} {
			t.Run(fmt.Sprintf("%s/%s", name, scheduler), func(t *testing.T) {
				env := newResilienceGateEnv(t, scheduler, mutate, nil)
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(cacheableContext(), st)
				gid := shareTestGroupID
				result, err := env.gateway.SelectAccountWithLoadAwareness(ctx, &gid, gateTestSession, gateTestModel, nil, "", 0)
				require.NoError(t, err)
				require.Equal(t, gateTestAccount, result.Account.ID)
				require.Equal(t, gateTestAccount, st.LastResort())
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

// resilienceOpenAIGateRow is one OpenAI account that violates exactly one
// original gate of the OpenAI scheduler.
type resilienceOpenAIGateRow struct {
	name    string
	account func(a *Account)
	setup   func(svc *OpenAIGatewayService)
	ctx     func(ctx context.Context) context.Context
	compact bool
}

func resilienceOpenAIGateRows() []resilienceOpenAIGateRow {
	return []resilienceOpenAIGateRow{
		{name: "model", account: func(a *Account) {
			a.Credentials = map[string]any{"model_mapping": map[string]any{"gpt-other": "gpt-other"}}
		}},
		{name: "channel_restriction",
			account: func(a *Account) {
				a.Credentials = map[string]any{"model_mapping": map[string]any{"gpt-5.1": "gpt-unpriced"}}
			},
			setup: func(svc *OpenAIGatewayService) {
				svc.channelService = newTestChannelService(makeStandardRepo(Channel{
					ID: 1, Status: StatusActive, GroupIDs: []int64{shareTestGroupID}, RestrictModels: true,
					BillingModelSource: BillingModelSourceUpstream,
					ModelPricing:       []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-5.1"}}},
				}, map[int64]string{shareTestGroupID: PlatformOpenAI}))
			}},
		{name: "profit_gate",
			account: func(a *Account) { rate := 5.0; a.RateMultiplier = &rate },
			ctx: func(ctx context.Context) context.Context {
				return context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
					groupID: shareTestGroupID, platform: PlatformOpenAI, threshold: 1, pricingAt: time.Now()})
			}},
		{name: "compact_unsupported", compact: true,
			account: func(a *Account) { a.Extra = map[string]any{"openai_compact_supported": false} }},
	}
}

// TestResilienceOpenAIGateConsistency is the OpenAI counterpart of
// TestResilienceGateConsistency.
func TestResilienceOpenAIGateConsistency(t *testing.T) {
	for _, row := range resilienceOpenAIGateRows() {
		for _, scheduler := range []sharesched.Scheduler{sharesched.SchedulerSub2API, sharesched.SchedulerShare} {
			for _, soft := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/soft=%v", row.name, scheduler, soft), func(t *testing.T) {
					withResilienceSettings(t, nil)
					env := newOpenAIShareTestEnv(t, shareTestPolicy(scheduler, sharesched.AccountRule{AccountID: gateTestAccount, Tier: 1, Weight: 1}), gateTestAccount)
					acc := env.account(gateTestAccount)
					row.account(acc)
					if soft {
						softPenalise(acc, time.Now())
					}
					env.svc.accountRepo = openAIGroupListingRepo{env.repo}
					env.svc.resilience = NewResilienceService(nil, newMemResilienceStore())
					if row.setup != nil {
						row.setup(env.svc)
					}
					st := resilience.NewRequestState()
					ctx := resilience.WithRequestState(cacheableContext(), st)
					if row.ctx != nil {
						ctx = row.ctx(ctx)
					}
					gid := shareTestGroupID
					sel, _, err := env.svc.SelectAccountWithScheduler(ctx, &gid, "", gateTestSession, "gpt-5.1", nil, OpenAIUpstreamTransportAny, row.compact)
					if err == nil && sel != nil && sel.ReleaseFunc != nil {
						sel.ReleaseFunc()
					}
					require.Error(t, err, "the gate rejects the only account")
					require.Zero(t, st.LastResort(), "last resort does not bypass the gate")
				})
			}
		}
	}
}
