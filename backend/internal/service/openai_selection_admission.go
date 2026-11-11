package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
)

// SchedTestAdmission is implemented by the OpenAI service. It deliberately
// uses the same request admission functions as the scheduler; the only
// difference is that the request has no group, session, or capacity.
var _ SchedTestAdmission = (*OpenAIGatewayService)(nil)

func (s *OpenAIGatewayService) TestRequestModel(ctx context.Context, account *Account, evidenceModel string) (string, bool, string) {
	evidenceModel = strings.TrimSpace(evidenceModel)
	if account == nil || evidenceModel == "" {
		return "", false, "no_model"
	}
	// A test has no group, even if its caller carries a group profit gate.
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, (*openAIProfitControlGate)(nil))
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	models := []string{evidenceModel}
	for model := range account.GetModelMapping() {
		if strings.TrimSpace(model) != "" && !strings.ContainsAny(model, "*?") && model != evidenceModel {
			models = append(models, model)
		}
	}
	sort.Strings(models[1:])
	for _, model := range models {
		if OpenAIAttemptModel(account, model) != evidenceModel || !account.IsModelSupported(model) {
			continue
		}
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, account) {
			return "", false, "account_scheduling_threshold"
		}
		req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: model}
		if reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, PlatformOpenAI, model, false, "", s); reason != "" {
			return "", false, reason
		}
		if ok, reason := (&defaultOpenAIAccountScheduler{service: s}).isAccountRequestCompatibleReason(ctx, account, req); !ok {
			return "", false, reason
		}
		return model, true, ""
	}
	return "", false, "no_model"
}

// Keep the scheduler's view dependency explicit for W0 wiring. A nil view is
// equivalent to a disabled quality system and therefore leaves accounts front.
var _ schedquality.View = (*emptySelectionView)(nil)

type emptySelectionView struct{}

func (emptySelectionView) Version() int64      { return 0 }
func (emptySelectionView) At() time.Time       { return time.Time{} }
func (emptySelectionView) GlobalEnabled() bool { return false }
func (emptySelectionView) StateOf(int64, int64, string) schedquality.StateView {
	return schedquality.StateView{}
}
func (emptySelectionView) TakeoverIdle(int64) (time.Duration, bool) { return 0, false }
