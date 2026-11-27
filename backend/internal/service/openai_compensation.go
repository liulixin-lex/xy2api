package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/shopspring/decimal"
)

// Optional extension of the resilience Redis store, deliberately independent
// of cache-aware reservation estimates and their feature switch.
type OpenAICompensationStore interface {
	SetCompensationReference(context.Context, string, OpenAICompensationReference) error
	FreezeCompensationAction(context.Context, string, OpenAICompensationAction) error
	TakeCompensationAction(context.Context, string, string, int64) (*OpenAICompensationAction, error)
}

type OpenAICompensationReference struct {
	CacheRead int       `json:"cache_read"`
	Total     int       `json:"total"`
	At        time.Time `json:"at"`
}

type OpenAICompensationAction struct {
	RequestID string                       `json:"request_id"`
	Kind      string                       `json:"kind"`
	From      int64                        `json:"from"`
	To        int64                        `json:"to"`
	At        time.Time                    `json:"at"`
	Reference *OpenAICompensationReference `json:"reference,omitempty"`
}

func compensationKey(turn string) string {
	if turn == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(turn))
	return hex.EncodeToString(sum[:])
}

func (s *OpenAIGatewayService) compensationStore() OpenAICompensationStore {
	if s == nil || s.resilience == nil {
		return nil
	}
	store, _ := s.resilience.store.(OpenAICompensationStore)
	return store
}

func (s *OpenAIGatewayService) installOpenAICompensationObserver(ctx context.Context) {
	if s.compensationStore() == nil {
		return
	}
	resilience.StateFrom(ctx).SetAccountSwitchObserver(func(from, to int64) {
		s.recordOpenAICompensationAction(ctx, "request_switch", from, to)
	})
}

func (s *OpenAIGatewayService) recordOpenAICompensationAction(ctx context.Context, kind string, from, to int64) {
	store := s.compensationStore()
	st := resilience.StateFrom(ctx)
	if store == nil || st == nil || !st.GatewayRequestEnabled() || from <= 0 || from == to {
		return
	}
	if reason := st.StickyBypassReason(); reason == "owner_group_mismatch" || reason == "identity_conflict" {
		return
	}
	key := compensationKey(st.TurnKey())
	if key == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCacheRatioTimeout)
	defer cancel()
	if err := store.FreezeCompensationAction(writeCtx, key, OpenAICompensationAction{RequestID: st.GatewayRequestID(), Kind: kind, From: from, To: to, At: time.Now()}); err != nil {
		slog.DebugContext(ctx, "openai_compensation.freeze_failed")
	}
}

type compensationDecision struct {
	Kind            string  `json:"kind"`
	Reason          string  `json:"reason"`
	ActionKind      string  `json:"action_kind"`
	ReferenceRatio  float64 `json:"reference_ratio"`
	ReferenceAgeMs  int64   `json:"reference_age_ms"`
	EstimatedAmount float64 `json:"estimated_amount"`
	Amount          float64 `json:"amount"`
}

// The callback runs only while inserting the first durable intent. Retries and
// recovery decode the settled amount from SQL and never consume Redis again.
func (s *OpenAIGatewayService) compensationResolver(ctx context.Context, row *UsageLog, quote func(float64) (float64, error), metering ...meteringAdjustment) (func(*UsageBillingCommand), func()) {
	store := s.compensationStore()
	meta := resilience.GatewayRequestMetadataFromContext(ctx)
	if store == nil || meta == nil || meta.RequestScope != resilience.GatewayRequestScopeText || row.BillingMode == nil || *row.BillingMode != string(BillingModeToken) {
		return nil, func() {}
	}
	key := compensationKey(meta.CompensationKey)
	if key == "" || row.GroupID == nil {
		return nil, func() {}
	}
	// Identical source and precedence to the real-time OpenAI profit gate.
	group := s.openAIProfitControlGroup(ctx, *row.GroupID)
	margin, known := 0.0, group != nil
	if group != nil && group.ProfitControlEnabled {
		margin = group.ProfitMinMargin
	}
	resolve := func(cmd *UsageBillingCommand) {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCacheRatioTimeout)
		defer cancel()
		action, err := store.TakeCompensationAction(readCtx, key, meta.RequestID, cmd.AccountID)
		if err != nil {
			slog.DebugContext(ctx, "openai_compensation.take_failed")
			return
		}
		if action == nil {
			return
		}
		seeded := len(cmd.Usage.MeteringAdjustment) == 0 && len(metering) > 0
		var seed []byte
		if seeded {
			seed, _ = json.Marshal(metering[0])
			cmd.Usage.MeteringAdjustment = seed
		}
		applyOpenAICompensation(cmd, meta, action, margin, known, quote)
		if seeded && string(seed) == string(cmd.Usage.MeteringAdjustment) {
			cmd.Usage.MeteringAdjustment = nil
		}
	}
	note := func() {
		if (meta.Result != resilience.RequestSuccess && meta.Result != resilience.RequestRescued) || row.UsageObserved == nil || !*row.UsageObserved {
			return
		}
		total := row.InputTokens + row.CacheReadTokens + row.CacheCreationTokens
		if total <= 0 || row.CacheReadTokens < 0 || row.CacheReadTokens > total {
			return
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionCacheRatioTimeout)
		defer cancel()
		if err := store.SetCompensationReference(writeCtx, key, OpenAICompensationReference{CacheRead: row.CacheReadTokens, Total: total, At: row.CreatedAt}); err != nil {
			slog.DebugContext(ctx, "openai_compensation.reference_failed")
		}
	}
	return resolve, note
}

func applyOpenAICompensation(cmd *UsageBillingCommand, meta *resilience.GatewayRequestMetadata, action *OpenAICompensationAction, margin float64, marginKnown bool, quote func(float64) (float64, error)) {
	if cmd == nil || cmd.Usage == nil || action == nil || action.Reference == nil {
		return
	}
	row, ref := cmd.Usage, action.Reference
	if action.Kind == "request_switch" && action.RequestID != meta.RequestID {
		return
	}
	if action.From <= 0 || action.From == action.To || action.At.After(row.CreatedAt) || row.CreatedAt.Sub(action.At) > time.Hour {
		return
	}
	// A request switch is scoped to its current bill; committed owner changes
	// additionally require the new owner's first bill (release-to-zero excludes old).
	if action.Kind != "request_switch" && ((action.To > 0 && action.To != cmd.AccountID) || (action.To == 0 && action.From == cmd.AccountID)) {
		return
	}
	age := row.CreatedAt.Sub(ref.At)
	total := row.InputTokens + row.CacheReadTokens + row.CacheCreationTokens
	if age < 0 || age > 10*time.Minute || ref.At.After(action.At) || ref.Total <= 0 || ref.CacheRead < 0 || ref.CacheRead > ref.Total || total <= 0 {
		return
	}
	ratio := float64(total) / float64(ref.Total)
	if ratio < 0.75 || ratio > 1.33 {
		return
	}
	gap := math.Max(0, float64(total)*float64(ref.CacheRead)/float64(ref.Total)-float64(row.CacheReadTokens))
	if gap <= 0 || quote == nil {
		return
	}
	estimate, err := quote(gap)
	decision := compensationDecision{Kind: "openai_voluntary_compensation_pending", ActionKind: action.Kind,
		ReferenceRatio: float64(ref.CacheRead) / float64(ref.Total), ReferenceAgeMs: age.Milliseconds()}
	if err != nil || !validUsageBillingAmount(estimate) {
		decision.Reason = "pricing_unavailable"
	} else {
		decision.EstimatedAmount = QuantizeUsageBillingAmount(estimate)
		switch {
		case meta.Switches > 0 || action.Kind == "request_switch" || meta.HadFailedAttempt || meta.Result != resilience.RequestSuccess || !row.DispatchCountExact() || *row.DispatchCount != 1:
			decision.Reason = "full_chain_cost_unknown"
		case row.SupplierCostEstimate == nil || !validUsageBillingAmount(*row.SupplierCostEstimate) || row.UsageObserved == nil || !*row.UsageObserved:
			decision.Reason = "supplier_cost_unknown"
		case !marginKnown || math.IsNaN(margin) || math.IsInf(margin, 0) || margin < 0 || margin >= 1:
			decision.Reason = "profit_margin_unknown"
		default:
			// Round the minimum revenue UP, not the credit: even a sub-cent boundary
			// must not lower the final decimal(20,8) revenue below the margin floor.
			revenue := decimal.NewFromFloat(row.ActualCost)
			floor := decimal.NewFromFloat(*row.SupplierCostEstimate).DivRound(decimal.NewFromInt(1).Sub(decimal.NewFromFloat(margin)), 24).RoundCeil(UsageBillingMonetaryScale)
			room := decimal.Max(decimal.Zero, revenue.Sub(floor))
			credit := decimal.Min(room, decimal.NewFromFloat(estimate).Truncate(UsageBillingMonetaryScale))
			decision.Amount, _ = credit.Float64()
			decision.Kind = "openai_voluntary_compensation"
			decision.Reason = "within_margin_floor"
			if credit.IsZero() {
				decision.Reason = "below_margin_floor_before_compensation"
			} else if credit.LessThan(decimal.NewFromFloat(estimate)) {
				decision.Reason = "margin_limited"
			}
		}
	}
	// Preserve the metering schema, including future fields; failed encoding
	// leaves the original charge and evidence untouched.
	var record map[string]json.RawMessage
	if json.Unmarshal(row.MeteringAdjustment, &record) != nil || record == nil {
		return
	}
	var adjustments []json.RawMessage
	if raw := record["adjustments"]; len(raw) > 0 && json.Unmarshal(raw, &adjustments) != nil {
		return
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		return
	}
	adjustments = append(adjustments, encoded)
	record["adjustments"], err = json.Marshal(adjustments)
	if err != nil {
		return
	}
	var previousCredit float64
	_ = json.Unmarshal(record["customer_credit"], &previousCredit)
	record["customer_credit"], err = json.Marshal(QuantizeUsageBillingAmount(previousCredit + decision.Amount))
	if err != nil {
		return
	}
	evidence, err := json.Marshal(record)
	if err != nil {
		return
	}
	if decision.Amount > 0 {
		subtract := func(v float64) float64 { return QuantizeUsageBillingAmount(math.Max(0, v-decision.Amount)) }
		cmd.BalanceCost = subtract(cmd.BalanceCost)
		cmd.SubscriptionCost = subtract(cmd.SubscriptionCost)
		cmd.APIKeyQuotaCost = subtract(cmd.APIKeyQuotaCost)
		cmd.APIKeyRateLimitCost = subtract(cmd.APIKeyRateLimitCost)
		row.ActualCost = subtract(row.ActualCost)
		if len(row.ServiceTierDetails) > 0 {
			var detail map[string]json.RawMessage
			if json.Unmarshal(row.ServiceTierDetails, &detail) == nil && detail != nil {
				detail["actual_cost"], _ = json.Marshal(row.ActualCost)
				if raw, e := json.Marshal(detail); e == nil {
					row.ServiceTierDetails = raw
				}
			}
		}
	}
	row.MeteringAdjustment = evidence
}

// Usage counters are integral, but the estimated gap need not be. Interpolate
// two adjacent quotes at the unchanged context length, using the billing path
// for both (including its service-tier caps and multipliers).
func compensationQuote(tokens UsageTokens, gross float64, quote func(UsageTokens) (float64, error)) func(float64) (float64, error) {
	return func(gap float64) (float64, error) {
		gap = math.Min(gap, float64(tokens.InputTokens+tokens.CacheCreationTokens))
		shifted := func(n int) (float64, error) {
			t := tokens
			input := min(n, t.InputTokens)
			t.InputTokens -= input
			t.CacheReadTokens += input
			n -= input
			creation := min(n, t.CacheCreationTokens)
			t.CacheCreationTokens -= creation
			t.CacheReadTokens += creation
			five := min(creation, t.CacheCreation5mTokens)
			t.CacheCreation5mTokens -= five
			t.CacheCreation1hTokens -= min(creation-five, t.CacheCreation1hTokens)
			return quote(t)
		}
		whole := int(math.Floor(gap))
		low, err := shifted(whole)
		if err != nil {
			return 0, err
		}
		cost := low
		if fraction := gap - float64(whole); fraction > 0 {
			high, e := shifted(whole + 1)
			if e != nil {
				return 0, e
			}
			cost = low + (high-low)*fraction
		}
		return math.Max(0, gross-cost), nil
	}
}
