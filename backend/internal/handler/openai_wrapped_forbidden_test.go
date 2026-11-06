//go:build unit

package handler

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/model"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type wrappedForbiddenUpstream struct {
	service.HTTPUpstream
}

func (*wrappedForbiddenUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	// Only the validated wrapper carries a status; the inner body is generic.
	body := "event: error\ndata: " + `{"type":"proxy.upstream_error","status":403,"upstream_body":{"error":{"code":"upstream_error","message":"upstream rejected"}}}` + "\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestOpenAIWrappedForbiddenFailoverChain(t *testing.T) {
	withHandlerResilience(t, func(s *resilience.Settings) { s.Budget.MaxAttempts = 2 })
	cfg := &config.Config{RunMode: config.RunModeSimple}
	upstream := &wrappedForbiddenUpstream{}
	gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
	code, message := http.StatusForbidden, "passthrough must not win"
	rules := service.NewErrorPassthroughService(&passthroughRuleRepo{rules: []*model.ErrorPassthroughRule{{
		ID: 1, Name: "all 403", Enabled: true, Priority: 1, ErrorCodes: []int{403},
		MatchMode: model.MatchModeAll, Platforms: []string{"openai"}, ResponseCode: &code, CustomMessage: &message,
	}}}, nil)
	handler := &OpenAIGatewayHandler{gatewayService: gateway, errorPassthroughService: rules}
	hosts := &hostSharingUnscheduler{sameHost: map[int64][]int64{1: {2}}}
	body := []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`)

	// Two independent requests reach the same accounts: first hit counts only,
	// second hit applies the existing ten-second account/model soft cooldown.
	for request := 1; request <= 2; request++ {
		core, logs := observer.New(zap.DebugLevel)
		ctx := logger.IntoContext(budgetCtx(t, resilience.KindText, 7), zap.New(core))
		c, recorder := newOpenAIResponsesFailoverTestContext(t, ctx)
		state := NewFailoverState(10, false)
		for _, id := range []int64{1, 3} {
			account := &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			_, err := gateway.Forward(ctx, c, account, body)
			var failover *service.UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Equal(t, service.OpenAIStreamForbiddenReason, failover.Reason)
			require.Equal(t, http.StatusForbidden, failover.WrappedStatus)
			require.False(t, failover.ShouldReportAccountScheduleFailure(), "the handler must not report this as an account scheduling failure")
			require.False(t, failover.RetryableOnSameAccount)
			require.NotNil(t, rules.MatchRule(service.PlatformOpenAI, failover.StatusCode, failover.ResponseBody), "the competing 403 passthrough rule must actually match")
			action := state.HandleFailoverError(ctx, hosts, id, service.PlatformOpenAI, 3, failover)
			if id == 1 {
				require.Equal(t, FailoverContinue, action)
				require.Contains(t, state.FailedAccountIDs, int64(2), "the next attempt must exclude the failed account's host")
				require.False(t, requestBudget(ctx).CapacitySwitchUsed())
			} else {
				require.Equal(t, FailoverExhausted, action)
			}
		}
		require.Equal(t, resilience.ExhaustedAttempts, requestBudget(ctx).Exhausted())
		cooldowns := logs.FilterMessage("resilience.forbidden_cooldown").All()
		require.Len(t, cooldowns, 2)
		for i, entry := range cooldowns {
			fields := entry.ContextMap()
			require.Equal(t, int64(1+2*i), fields["account_id"])
			require.Equal(t, "gpt-5.1", fields["model"])
			require.Equal(t, int64(request), fields["failure_streak"])
			require.Equal(t, int64((request-1)*10000), fields["cooldown_ms"])
		}
		handler.handleFailoverExhausted(c, state.LastFailoverErr, false)
		require.Equal(t, http.StatusBadGateway, recorder.Code)
		require.Equal(t, "server_error", gjson.Get(recorder.Body.String(), "error.type").String())
		require.Equal(t, "server_error", gjson.Get(recorder.Body.String(), "error.code").String())
		require.NotContains(t, recorder.Body.String(), message)
	}
}
