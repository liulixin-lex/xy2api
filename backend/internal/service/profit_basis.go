package service

import (
	"context"
	"encoding/json"
	"math"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type ProfitBasis = resilience.ProfitBasis
type ProfitDecision = resilience.ProfitDecision

func recordProfitDecision(ctx context.Context, gate *openAIProfitControlGate, accountID int64, upstream float64, source string, valid bool, verdict string) {
	decision := ProfitDecision{AccountID: accountID, Sell: gate.sell, Source: source, Verdict: verdict}
	if math.IsNaN(decision.Sell) || math.IsInf(decision.Sell, 0) {
		decision.Sell = 0
	}
	if valid {
		decision.Upstream = &upstream
	}
	if gate.ceilingOn {
		decision.Ceiling = &gate.threshold
	}
	if gate.floor > 0 {
		decision.Floor = &gate.floor
	}
	resilience.StateFrom(ctx).NoteProfitDecision(decision)
}

func marshalProfitBasis(basis *ProfitBasis) json.RawMessage {
	if basis == nil {
		return nil
	}
	raw, _ := json.Marshal(basis)
	return raw
}
