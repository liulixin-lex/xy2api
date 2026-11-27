//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func compensationFixture() (*UsageBillingCommand, *resilience.GatewayRequestMetadata, *OpenAICompensationAction) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := &UsageLog{AccountID: 2, InputTokens: 100, CacheReadTokens: 0, ActualCost: 10, TotalCost: 10, CreatedAt: at, UsageObserved: new(true), SupplierCostEstimate: new(6.0)}
	row.DispatchCount = new(int64(1))
	row.DispatchPhases = json.RawMessage(`{"count_version":2}`)
	row.MeteringAdjustment = json.RawMessage(`{"v":1,"raw_usage":{"input_tokens":100},"customer_gross_cost":10,"customer_credit":0,"supplier_cost_source":"contract","future_evidence":true}`)
	cmd := &UsageBillingCommand{AccountID: 2, BalanceCost: 10, APIKeyQuotaCost: 10, APIKeyRateLimitCost: 10, AccountQuotaCost: 10, Usage: row}
	meta := &resilience.GatewayRequestMetadata{RequestID: "gateway-fixture", Result: resilience.RequestSuccess}
	action := &OpenAICompensationAction{RequestID: meta.RequestID, Kind: "migrate", From: 1, To: 2, At: at.Add(-time.Minute), Reference: &OpenAICompensationReference{CacheRead: 80, Total: 100, At: at.Add(-2 * time.Minute)}}
	return cmd, meta, action
}

func TestOpenAICompensationEligibilityAndMargin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*UsageBillingCommand, *resilience.GatewayRequestMetadata, *OpenAICompensationAction)
		margin float64
		known  bool
		want   float64
		reason string
	}{
		{name: "floor", margin: .2, known: true, want: 7.5, reason: "margin_limited"},
		{name: "disabled_gate", known: true, want: 6, reason: "margin_limited"},
		{name: "already_below", margin: .5, known: true, want: 10, reason: "below_margin_floor_before_compensation"},
		{name: "multi_send", known: true, want: 10, reason: "full_chain_cost_unknown", mutate: func(c *UsageBillingCommand, _ *resilience.GatewayRequestMetadata, _ *OpenAICompensationAction) {
			c.Usage.DispatchCount = new(int64(2))
		}},
		{name: "switch", known: true, want: 10, reason: "full_chain_cost_unknown", mutate: func(_ *UsageBillingCommand, m *resilience.GatewayRequestMetadata, a *OpenAICompensationAction) {
			m.Switches = 1
			a.Kind = "request_switch"
		}},
		{name: "failed_attempt", known: true, want: 10, reason: "full_chain_cost_unknown", mutate: func(_ *UsageBillingCommand, m *resilience.GatewayRequestMetadata, _ *OpenAICompensationAction) {
			m.HadFailedAttempt = true
		}},
		{name: "failed_result", known: true, want: 10, reason: "full_chain_cost_unknown", mutate: func(_ *UsageBillingCommand, m *resilience.GatewayRequestMetadata, _ *OpenAICompensationAction) {
			m.Result = resilience.RequestFailed
		}},
		{name: "unknown_cost", known: true, want: 10, reason: "supplier_cost_unknown", mutate: func(c *UsageBillingCommand, _ *resilience.GatewayRequestMetadata, _ *OpenAICompensationAction) {
			c.Usage.SupplierCostEstimate = nil
		}},
		{name: "unknown_margin", want: 10, reason: "profit_margin_unknown"},
		{name: "old_count", known: true, want: 10, reason: "full_chain_cost_unknown", mutate: func(c *UsageBillingCommand, _ *resilience.GatewayRequestMetadata, _ *OpenAICompensationAction) {
			c.Usage.DispatchPhases = nil
		}},
		{name: "release", known: true, want: 6, reason: "margin_limited", mutate: func(_ *UsageBillingCommand, _ *resilience.GatewayRequestMetadata, a *OpenAICompensationAction) {
			a.To = 0
			a.Kind = "owner_released"
		}},
		{name: "round_floor_up", margin: .3, known: true, want: 8.57142858, reason: "margin_limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m, a := compensationFixture()
			if tc.mutate != nil {
				tc.mutate(c, m, a)
			}
			originalCost := c.Usage.SupplierCostEstimate
			applyOpenAICompensation(c, m, a, tc.margin, tc.known, func(gap float64) (float64, error) { require.Equal(t, 80.0, gap); return 8, nil })
			require.Equal(t, tc.want, c.BalanceCost)
			require.Equal(t, tc.want, c.APIKeyQuotaCost)
			require.Equal(t, tc.want, c.APIKeyRateLimitCost)
			require.Equal(t, tc.want, c.Usage.ActualCost)
			require.Equal(t, 10.0, c.AccountQuotaCost)
			require.Equal(t, 10.0, c.Usage.TotalCost)
			require.Equal(t, originalCost, c.Usage.SupplierCostEstimate)
			var evidence struct {
				Adjustments []compensationDecision `json:"adjustments"`
				Future      bool                   `json:"future_evidence"`
			}
			require.NoError(t, json.Unmarshal(c.Usage.MeteringAdjustment, &evidence))
			require.True(t, evidence.Future)
			require.Len(t, evidence.Adjustments, 1)
			d := evidence.Adjustments[0]
			require.Equal(t, tc.reason, d.Reason)
			require.Equal(t, 8.0, d.EstimatedAmount)
			if tc.want == 10 && tc.reason != "below_margin_floor_before_compensation" {
				require.Equal(t, "openai_voluntary_compensation_pending", d.Kind)
			}
		})
	}
}

func TestOpenAICompensationReferenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*UsageBillingCommand, *OpenAICompensationAction)
		eligible bool
	}{
		{"ten_minutes", func(c *UsageBillingCommand, a *OpenAICompensationAction) {
			a.Reference.At = c.Usage.CreatedAt.Add(-10 * time.Minute)
		}, true},
		{"too_old", func(c *UsageBillingCommand, a *OpenAICompensationAction) {
			a.Reference.At = c.Usage.CreatedAt.Add(-10*time.Minute - time.Nanosecond)
		}, false},
		{"ratio_low", func(c *UsageBillingCommand, _ *OpenAICompensationAction) { c.Usage.InputTokens = 74 }, false},
		{"ratio_high", func(c *UsageBillingCommand, _ *OpenAICompensationAction) { c.Usage.InputTokens = 134 }, false},
		{"ratio_min", func(c *UsageBillingCommand, _ *OpenAICompensationAction) { c.Usage.InputTokens = 75 }, true},
		{"ratio_max", func(c *UsageBillingCommand, _ *OpenAICompensationAction) { c.Usage.InputTokens = 133 }, true},
		{"wrong_target", func(_ *UsageBillingCommand, a *OpenAICompensationAction) { a.To = 3 }, false},
		{"release_old_owner", func(_ *UsageBillingCommand, a *OpenAICompensationAction) { a.To = 0; a.From = 2 }, false},
		{"no_reference", func(_ *UsageBillingCommand, a *OpenAICompensationAction) { a.Reference = nil }, false},
		{"cold_no_gap", func(_ *UsageBillingCommand, a *OpenAICompensationAction) { a.Reference.CacheRead = 0 }, false},
		{"after_action", func(c *UsageBillingCommand, a *OpenAICompensationAction) { a.Reference.At = c.Usage.CreatedAt }, false},
		{"expired_action", func(c *UsageBillingCommand, a *OpenAICompensationAction) {
			a.At = c.Usage.CreatedAt.Add(-time.Hour - time.Second)
		}, false},
		{"foreign_request_switch", func(_ *UsageBillingCommand, a *OpenAICompensationAction) {
			a.Kind = "request_switch"
			a.RequestID = "other"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, m, a := compensationFixture()
			tc.mutate(c, a)
			calls := 0
			applyOpenAICompensation(c, m, a, 0, true, func(float64) (float64, error) { calls++; return 1, nil })
			require.Equal(t, tc.eligible, calls > 0)
			if tc.eligible {
				require.Equal(t, 9.0, c.BalanceCost)
			} else {
				require.Equal(t, 10.0, c.BalanceCost)
			}
		})
	}
}

type compensationMemoryStore struct {
	ResilienceStore
	action     *OpenAICompensationAction
	err        error
	references int
	takes      int
}

func (s *compensationMemoryStore) SetCompensationReference(context.Context, string, OpenAICompensationReference) error {
	s.references++
	return s.err
}
func (s *compensationMemoryStore) FreezeCompensationAction(_ context.Context, _ string, a OpenAICompensationAction) error {
	if s.action == nil {
		s.action = &a
	}
	return s.err
}
func (s *compensationMemoryStore) TakeCompensationAction(context.Context, string, string, int64) (*OpenAICompensationAction, error) {
	s.takes++
	a := s.action
	s.action = nil
	return a, s.err
}

func TestOpenAICompensationUsesGateConfigAndRedisFailureIsZero(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		c, m, a := compensationFixture()
		m.RequestScope = resilience.GatewayRequestScopeText
		m.CompensationKey = "fixture"
		c.Usage.GroupID = new(int64(7))
		c.Usage.BillingMode = new(string(BillingModeToken))
		store := &compensationMemoryStore{action: a}
		svc := &OpenAIGatewayService{resilience: &ResilienceService{store: store}}
		ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 7, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, ProfitControlEnabled: enabled, ProfitMinMargin: .2})
		ctx = resilience.WithGatewayRequestMetadata(ctx, m)
		resolve, note := svc.compensationResolver(ctx, c.Usage, func(float64) (float64, error) { return 8, nil })
		require.NotNil(t, resolve)
		resolve(c)
		note()
		want := 6.0
		if enabled {
			want = 7.5
		}
		require.Equal(t, want, c.BalanceCost)
		require.Equal(t, 1, store.references)
		c, m, a = compensationFixture()
		store.action = a
		store.err = errors.New("unavailable")
		resolve(c)
		require.Equal(t, 10.0, c.BalanceCost)
	}
}

func TestOpenAICompensationQuoteUsesBillingTiersAndFractionalGap(t *testing.T) {
	billing := NewBillingService(nil, nil)
	group := &Group{ID: 7, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{Models: []string{"fixture"}, InputPrice: new(.1), OutputPrice: new(.2), CacheReadPrice: new(.01)}}}
	resolver := NewModelPricingResolver(nil, billing)
	quote := func(tokens UsageTokens) (float64, error) {
		c, e := billing.CalculateCostUnified(CostInput{Ctx: context.Background(), Model: "fixture", Group: group, GroupID: &group.ID, Resolver: resolver, Tokens: tokens, RateMultiplier: 1.5})
		if e != nil {
			return 0, e
		}
		return c.ActualCost, nil
	}
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 10}
	gross, err := quote(tokens)
	require.NoError(t, err)
	credit, err := compensationQuote(tokens, gross, quote)(50.5)
	require.NoError(t, err)
	require.InDelta(t, 50.5*.09*1.5, credit, 1e-10)
}

type compensationApplyingRepo struct {
	UsageBillingRepository
	cmd *UsageBillingCommand
}

func (r *compensationApplyingRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	if cmd.ResolveCompensation != nil {
		cmd.ResolveCompensation(cmd)
	}
	cmd.Usage.ID = 99
	r.cmd = cmd
	return &UsageBillingApplyResult{Applied: true, CachesManagedDurably: true}, nil
}

func TestOpenAICompensationRecordUsageKeepsRawCostAndUsesTierQuote(t *testing.T) {
	logs := &openAIRecordUsageLogRepoStub{inserted: true}
	bills := &compensationApplyingRepo{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, bills, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	store := &compensationMemoryStore{ResilienceStore: newMemResilienceStore()}
	svc.resilience = &ResilienceService{store: store}
	key := &APIKey{ID: 2, GroupID: new(int64(7)), Group: &Group{ID: 7, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, RateMultiplier: 2, FreeOpenAIFast: true, ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-5.1"}, InputPrice: new(.01), OutputPrice: new(.02), CacheReadPrice: new(.001)}}}}
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: new(.01)}
	at := time.Now()
	st := resilience.NewRequestState()
	st.SetAPIKey(2)
	st.NoteTurn(7, "gpt-5.1", "session")
	st.EnableGatewayRequest(at)
	ctx := resilience.WithRequestState(context.Background(), st)
	ctx = context.WithValue(ctx, ctxkey.Group, key.Group)
	meta := &resilience.GatewayRequestMetadata{RequestID: "fixture", Result: resilience.RequestSuccess, RequestScope: resilience.GatewayRequestScopeText, CompensationKey: st.TurnKey()}
	ctx = resilience.WithGatewayRequestMetadata(ctx, meta)
	// Real dispatch reading stays the same as the production NoteDispatch path.
	st.NoteDispatch(time.Now(), "direct")
	store.action = &OpenAICompensationAction{Kind: "migrate", From: 1, To: 3, At: at.Add(-time.Second), Reference: &OpenAICompensationReference{CacheRead: 80, Total: 100, At: at.Add(-time.Minute)}}
	result := &OpenAIForwardResult{RequestID: "fixture", Model: "gpt-5.1", ServiceTier: new("priority"), Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10}, ResponseFactsKnown: true, UsagePresent: true, UsageSource: "upstream"}
	require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: account}))
	require.NotNil(t, bills.cmd)
	require.Equal(t, 1, store.takes)
	require.Equal(t, 1, store.references)
	var record struct {
		Gross       float64                `json:"customer_gross_cost"`
		Credit      float64                `json:"customer_credit"`
		Adjustments []compensationDecision `json:"adjustments"`
	}
	require.NoError(t, json.Unmarshal(bills.cmd.Usage.MeteringAdjustment, &record))
	require.Len(t, record.Adjustments, 1)
	require.Greater(t, record.Credit, 0.0)
	require.InDelta(t, record.Gross-record.Credit, bills.cmd.BalanceCost, 1e-8)
	require.Equal(t, 100, bills.cmd.Usage.InputTokens)
	require.Zero(t, bills.cmd.Usage.CacheReadTokens)
	require.NotNil(t, bills.cmd.Usage.SupplierCostEstimate)
	require.Equal(t, 100, result.Usage.InputTokens)
	require.Zero(t, result.Usage.CacheReadInputTokens)
}

func TestOpenAICompensationOnlyCommittedOwnerChangesFreeze(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel", "conflict", "error"} {
		t.Run(outcome, func(t *testing.T) {
			f := selectionNewSessionFixture()
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			st.SetAPIKey(1)
			st.NoteTurn(7, "m", "session")
			ctx := resilience.WithRequestState(context.Background(), st)
			store := &compensationMemoryStore{ResilienceStore: newMemResilienceStore()}
			svc := &OpenAIGatewayService{resilience: &ResilienceService{store: store}}
			st.SetStickyChange(&openAIStickyChange{groupID: 7, session: "session", key: f.session.cacheKey("session"), recordCompensationAction: svc.recordOpenAICompensationAction})
			selected := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			require.Nil(t, store.action)
			f.binding.casFail = outcome == "conflict"
			f.binding.casError = outcome == "error"
			CompleteOpenAIStickyChange(ctx, selected.Account.ID, outcome != "failure" && outcome != "cancel", outcome == "cancel")
			if outcome == "success" {
				require.NotNil(t, store.action)
				require.Equal(t, f.owner.ID, store.action.From)
				require.Equal(t, selected.Account.ID, store.action.To)
			} else {
				require.Nil(t, store.action)
			}
		})
	}
}
