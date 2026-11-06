//go:build unit

package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
	payload string
}

func (u *wrappedForbiddenUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	// Only the validated wrapper carries a status; the inner body is generic.
	payload := u.payload
	if payload == "" {
		payload = `{"type":"proxy.upstream_error","status":403,"upstream_body":{"error":{"code":"upstream_error","message":"upstream rejected"}}}`
	}
	body := "event: error\ndata: " + payload + "\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestOpenAIWrappedRequestRejectionUncommitted(t *testing.T) {
	testOpenAIWrappedRequestRejectionTerminal(t, false)
}

func TestOpenAIWrappedRequestRejectionAfterHeartbeat(t *testing.T) {
	testOpenAIWrappedRequestRejectionTerminal(t, true)
}

func testOpenAIWrappedRequestRejectionTerminal(t *testing.T, committed bool) {
	t.Helper()
	withHandlerResilience(t, nil)
	for _, tc := range []struct {
		name, code, message, wantCode string
		status                        int
	}{
		{"state", "client_tool_continuation_unavailable", "Conversation state rejected", "invalid_prompt", http.StatusUnauthorized},
		{"context wording", "", "maximum context length exceeded", "context_length_exceeded", http.StatusForbidden},
		{"cyber", "cyber_policy", "Cyber policy rejected this request", "cyber_policy", http.StatusUnauthorized},
		{"content safety", "content_policy_violation", "Content policy rejected this request", "invalid_prompt", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			ctx := logger.IntoContext(budgetCtx(t, resilience.KindText, 7), zap.New(core))
			c, recorder := newOpenAIResponsesFailoverTestContext(t, ctx)
			service.InstallDeliveryTracking(c, time.Now())
			if committed {
				c.Header("Content-Type", "text/event-stream")
				n, err := c.Writer.WriteString("event: ping\ndata: {\"type\":\"ping\"}\n\n")
				require.NoError(t, err)
				require.True(t, service.NoteDeliveryHeartbeat(c, n))
				c.Writer.Flush()
				only, tracked := service.DeliveryOnlyHeartbeats(c)
				require.True(t, only && tracked)
			}
			innerMessage := tc.message + "; workspace_id=workspace-secret. Please try again in 7s."
			payload, err := json.Marshal(map[string]any{
				"type": "proxy.upstream_error", "status": tc.status,
				"upstream_body": map[string]any{"error": map[string]any{"code": tc.code, "message": innerMessage}},
			})
			require.NoError(t, err)
			upstream := &wrappedForbiddenUpstream{payload: string(payload)}
			gateway := service.NewOpenAIGatewayService(nil, nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
			account := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			_, err = gateway.Forward(ctx, c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`))
			var failure *service.UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.status, failure.WrappedStatus, "preserve validated status for diagnostics")
			require.NotEqual(t, service.OpenAIStreamForbiddenReason, failure.Reason)
			require.False(t, failure.ShouldReportAccountScheduleFailure())
			require.Equal(t, resilience.CategoryClientError, service.ClassifyFailoverError(failure).Category)
			state := NewFailoverState(10, false)
			hosts := &hostSharingUnscheduler{sameHost: map[int64][]int64{1: {2}}}
			require.Equal(t, FailoverExhausted, state.HandleFailoverError(ctx, hosts, account.ID, service.PlatformOpenAI, 3, failure))
			require.Empty(t, state.FailedAccountIDs, "request rejection must stop before excluding accounts or hosts")
			require.Empty(t, logs.FilterMessage("resilience.forbidden_cooldown").All())

			// Neither a competing status passthrough nor stale retry advice may
			// override the terminal semantics of the decoded request rejection.
			passthroughStatus, passthroughMessage := http.StatusBadGateway, "passthrough must not win"
			rules := service.NewErrorPassthroughService(&passthroughRuleRepo{rules: []*model.ErrorPassthroughRule{{
				ID: 1, Name: "wrapped status", Enabled: true, Priority: 1, ErrorCodes: []int{tc.status},
				MatchMode: model.MatchModeAll, Platforms: []string{"openai"}, ResponseCode: &passthroughStatus, CustomMessage: &passthroughMessage,
			}}}, nil)
			require.NotNil(t, rules.MatchRule(service.PlatformOpenAI, failure.StatusCode, failure.ResponseBody))
			failure.ResponseHeaders = http.Header{"Retry-After": {"9"}}
			c.Header("Retry-After", "7")
			c.Set(ginKeyResilienceRetryAfter, 7)
			resilience.SetRetryAdvice(ctx, resilience.RetryAdvice{Seconds: 7})
			handler := &OpenAIGatewayHandler{gatewayService: gateway, errorPassthroughService: rules}
			handler.handleFailoverExhausted(c, failure, committed)
			wantMessage := tc.message + "; workspace_id=[redacted]"
			if committed {
				require.Equal(t, http.StatusOK, recorder.Code, "the heartbeat already committed HTTP 200")
				stream := recorder.Body.String()
				require.Contains(t, stream, "event: ping\n")
				require.Contains(t, stream, "event: response.created\n")
				terminalAt := strings.LastIndex(stream, "event: response.failed\n")
				require.NotEqual(t, -1, terminalAt)
				_, body := parseResponsesFailedSSE(t, stream[terminalAt:])
				require.Equal(t, tc.wantCode, body["code"])
				require.Equal(t, wantMessage, body["message"])
				require.NotContains(t, body, "headers")
				// Codex 0.166 codex-api/src/sse/responses_error.rs maps these
				// codes to ContextWindowExceeded, CyberPolicy and InvalidPrompt,
				// respectively, before the default ApiError::Retryable arm.
				require.Contains(t, []string{"context_length_exceeded", "cyber_policy", "invalid_prompt"}, body["code"])
				streamErr, ok := service.GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, http.StatusBadRequest, streamErr.IntendedStatus)
			} else {
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Equal(t, tc.wantCode, gjson.Get(recorder.Body.String(), "error.code").String())
				require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
				require.Equal(t, wantMessage, gjson.Get(recorder.Body.String(), "error.message").String())
			}
			require.Empty(t, recorder.Header().Get("Retry-After"))
			require.Empty(t, recorder.Result().Header.Get("Retry-After"))
			require.Empty(t, recorder.Header().Get("x-should-retry"))
			require.Zero(t, c.GetInt(ginKeyResilienceRetryAfter))
			require.True(t, resilience.RetryAdviceFrom(ctx).Suppress)
			require.NotContains(t, recorder.Body.String(), "Please try again")
			require.NotContains(t, recorder.Body.String(), "workspace-secret")
			require.NotContains(t, recorder.Body.String(), passthroughMessage)
		})
	}
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
