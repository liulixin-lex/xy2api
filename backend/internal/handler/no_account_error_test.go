//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/liulixin-lex/xy2api/internal/service"
)

type fakeDiagnoser struct {
	calls []fakeDiagnoseCall
	resp  service.ModelAvailabilityDiagnosis
}

type fakeDiagnoseCall struct {
	GroupID  *int64
	Model    string
	Platform string
}

func (f *fakeDiagnoser) DiagnoseModelAvailabilityForPlatform(
	_ context.Context,
	groupID *int64,
	model, platform string,
) service.ModelAvailabilityDiagnosis {
	f.calls = append(f.calls, fakeDiagnoseCall{
		GroupID:  groupID,
		Model:    model,
		Platform: platform,
	})
	return f.resp
}

func ptrInt64(v int64) *int64 { return &v }

// newTestGinContextWithRequest wraps the bare newTestGinContext helper
// (defined in openai_gateway_cyber_test.go) by additionally attaching a stub
// *http.Request so the classifier can extract c.Request.Context().
func newTestGinContextWithRequest() *gin.Context {
	c := newTestGinContext()
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	return c
}

func TestClassifyNoAccountError_NilDiagnoser_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(c, nil, apiKey, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

func TestM8LegacyModelDiagnosisPreserves404(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result service.ModelAvailabilityDiagnosis
		status int
	}{
		{"legacy_missing", service.ModelAvailabilityDiagnosis{HasAccountsInPool: true}, 404},
		{"legacy_supported", service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}, 503},
		{"unknown", service.ModelAvailabilityDiagnosis{}, 503},
		{"dependency_error", service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, Err: fmt.Errorf("scan failed")}, 503},
		{"typed_empty", service.ModelAvailabilityDiagnosis{Known: true}, 503},
		{"typed_missing", service.ModelAvailabilityDiagnosis{Known: true, Members: 1}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestGinContextWithRequest()
			classification := classifyNoAccountErrorFromGin(c, &fakeDiagnoser{resp: tc.result},
				&service.APIKey{GroupID: ptrInt64(42)}, "gpt-5.1-codex-mini", "gpt-5.1-codex-mini", service.PlatformOpenAI)
			require.Equal(t, tc.status, classification.Status)
			if tc.status == 404 {
				require.True(t, classification.ModelNotFound)
				require.Equal(t, "model_not_found", classification.ErrType)
				require.Contains(t, classification.Message, "gpt-5.1-codex-mini")
			}
		})
	}
}

func TestClassifySelectionFailureError_RateLimitedPool(t *testing.T) {
	fallback := noAccountErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: "Service temporarily unavailable"}

	got := classifySelectionFailureError(
		&service.SelectionExhaustion{Known: true, Members: 3, ModelSupported: 3, RequestSupported: 3, Reasons: map[service.SelectionReason]int{service.SelectionReasonModelRateLimited: 3}},
		fallback,
	)

	require.Equal(t, http.StatusTooManyRequests, got.Status)
	require.Equal(t, "rate_limit_error", got.ErrType)
	require.Contains(t, got.Message, "rate-limited")
	require.Equal(t, fallback, classifySelectionFailureError(fmt.Errorf("model_rate_limited=0"), fallback))
	require.Equal(t, fallback, classifySelectionFailureError(fmt.Errorf("no available accounts"), fallback))
}

func TestClassifyNoAccountError_NilAPIKey_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}

	cls := classifyNoAccountErrorFromGin(c, fd, nil, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls, "diagnoser must not be consulted when apiKey missing")
}

func TestClassifyNoAccountError_NilGroupID_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: nil}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls, "diagnoser must not be consulted when group not bound")
}

func TestClassifyNoAccountError_EmptyModel_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "   ", "", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls)
}

func TestClassifyNoAccountError_ModelNotSupported_Returns404(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(42)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "gpt-5.1-codex-mini", "gpt-5.1-codex-mini", service.PlatformOpenAI)

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "gpt-5.1-codex-mini", "message must surface the requested model")

	require.Len(t, fd.calls, 1)
	require.Equal(t, "gpt-5.1-codex-mini", fd.calls[0].Model)
	require.Equal(t, service.PlatformOpenAI, fd.calls[0].Platform)
	require.NotNil(t, fd.calls[0].GroupID)
	require.Equal(t, int64(42), *fd.calls[0].GroupID)
	require.True(t, service.HasOpsClientBusinessLimited(c))
	require.Equal(t, service.OpsClientBusinessLimitedReasonLocalModelConfiguration, service.OpsClientBusinessLimitedReason(c))
}

func TestClassifyOpenAICompatibleNoAccountError_GrokUsesGrokPlatform(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	groupID := int64(43)
	apiKey := &service.APIKey{
		GroupID: &groupID,
		Group: &service.Group{
			ID:       groupID,
			Platform: service.PlatformGrok,
		},
	}

	cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, fd, apiKey, "grok-4.5", "grok-4.5")

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Len(t, fd.calls, 1)
	require.Equal(t, service.PlatformGrok, fd.calls[0].Platform)
	require.True(t, service.HasOpsClientBusinessLimited(c))
	require.Equal(t, service.OpsClientBusinessLimitedReasonLocalModelConfiguration, service.OpsClientBusinessLimitedReason(c))

	logErr := openAICompatibleSelectionErrorForLog(
		fmt.Errorf("no available OpenAI accounts supporting model: grok-4.5"),
		service.PlatformGrok,
	)
	require.EqualError(t, logErr, "no available Grok accounts supporting model: grok-4.5")
}

func TestClassifyNoAccountError_PureClassifierDoesNotMarkGinContext(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountError(c.Request.Context(), fd, apiKey, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.False(t, service.HasOpsClientBusinessLimited(c))
	require.Empty(t, service.OpsClientBusinessLimitedReason(c))
}

func TestClassifyNoAccountError_HasModelSupport_KeepsRoutingMessageGenerationToCaller(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, ModelSupported: 1, RequestSupported: 1, HasAccountsInPool: true, HasModelSupport: true}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status, "model exists somewhere — caller stays on 503")
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

func TestClassifyNoAccountError_ModelSupportedOnlyByRateLimitedAccount_Returns503(t *testing.T) {
	c := newTestGinContextWithRequest()
	// The diagnoser's configured-state lookup still sees the model-supporting
	// account even though normal scheduling has excluded it during cooldown.
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, ModelSupported: 1, RequestSupported: 1, HasAccountsInPool: true, HasModelSupport: true}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "claude-opus-4-8", "claude-opus-4-8", service.PlatformAnthropic)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound, "temporary account cooldown must remain retryable")
}

func TestClassifyNoAccountError_NoAccountsInPool_Stays503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: false, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status, "empty pool is a service-availability issue, not a model issue")
	require.False(t, cls.ModelNotFound)
}

func TestClassifyNoAccountError_DisplayModelOverridesRoutingForMessage(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "gpt-5", "claude-3-fancy", service.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "claude-3-fancy", "user-facing message must reference the model the user asked for, not the post-mapping routing model")
	require.Len(t, fd.calls, 1)
	require.Equal(t, "gpt-5", fd.calls[0].Model, "diagnosis must run against the routing model (post group dispatch mapping)")
}

func TestClassifyNoAccountError_FromGin_NilContextStillSafe(t *testing.T) {
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoAccountErrorFromGin(nil, fd, apiKey, "gpt-5", "gpt-5", service.PlatformOpenAI)

	require.Equal(t, http.StatusNotFound, cls.Status, "even with a nil gin context the classifier must still run and yield a coherent response")
	require.True(t, cls.ModelNotFound)
	require.False(t, service.HasOpsClientBusinessLimited(nil))
	require.Empty(t, service.OpsClientBusinessLimitedReason(nil))
}

// 权威的 404 model_not_found 不能被"账号被限流"的 429 盖掉。
//
// 选号失败的错误串同时携带多种过滤原因，例如
// "pool=9, filtered: model_not_supported=8 model_rate_limited=1"：8 个账号根本不支持该模型，
// 剩下 1 个恰好处于模型级冷却。此时 classifyNoAccountError 已通过持久化判据确认整个分组
// 没有账号能服务该模型（ModelNotFound=true），改判成 429 "All available accounts are
// currently rate-limited" 是错误诊断——重试永远不会成功，而把 429 当限流的客户端会反复
// 重试并吞掉 body（Codex 只显示 "exceeded retry limit"），恰好丢掉唯一说明真实原因的信息。
func TestClassifySelectionFailureError_ModelNotFoundIsNotOverriddenByRateLimited(t *testing.T) {
	modelNotFound := noAccountErrorClassification{
		Status:        http.StatusNotFound,
		ErrType:       "model_not_found",
		Message:       `Model "gpt-5.3-codex" is not supported by any configured account in this group`,
		ModelNotFound: true,
	}

	got := classifySelectionFailureError(
		fmt.Errorf("no available OpenAI accounts supporting model: gpt-5.3-codex "+
			"(pool=9, filtered: model_not_supported=8 model_rate_limited=1)"),
		modelNotFound,
	)

	require.Equal(t, modelNotFound, got,
		"分组里没有任何账号能服务该模型时，模型级冷却不该把 404 改判成 429")
}

// 真实调用点的顺序：先 classifyNoAccountErrorFromGin，再 classifySelectionFailureError。
// 覆盖这条链路是为了同时锁住 ops 归因——调用点用 ModelNotFound 决定是否标记
// routing capacity limited，一旦 404 被改判成 429，同一个请求会既被标成
// local model configuration 又被标成容量问题，自相矛盾。
func TestClassifySelectionFailureError_CallSiteChainKeepsModelNotFoundAttribution(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{Known: true, Members: 1, HasAccountsInPool: true, HasModelSupport: false}}
	apiKey := &service.APIKey{GroupID: ptrInt64(43)}

	cls := classifyNoAccountErrorFromGin(c, fd, apiKey, "gpt-5.3-codex", "gpt-5.3-codex", service.PlatformOpenAI)
	cls = classifySelectionFailureError(
		fmt.Errorf("no available OpenAI accounts supporting model: gpt-5.3-codex "+
			"(pool=9, filtered: model_not_supported=8 model_rate_limited=1)"),
		cls,
	)

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "gpt-5.3-codex")
	require.True(t, service.HasOpsClientBusinessLimited(c))
	require.Equal(t, service.OpsClientBusinessLimitedReasonLocalModelConfiguration, service.OpsClientBusinessLimitedReason(c))
}

// 池子里确实存在能服务该模型、只是全部在冷却的账号时，429 改判仍需保留：
// 这种情况 fallback 是 503（HasModelSupport=true），重试是有意义的。
func TestClassifySelectionFailureError_StillUpgradesNonModelNotFoundFallback(t *testing.T) {
	fallback := noAccountErrorClassification{
		Status:  http.StatusServiceUnavailable,
		ErrType: "api_error",
		Message: "Service temporarily unavailable",
	}

	got := classifySelectionFailureError(
		&service.SelectionExhaustion{Known: true, Members: 3, ModelSupported: 3, RequestSupported: 3, Reasons: map[service.SelectionReason]int{service.SelectionReasonModelRateLimited: 3}},
		fallback,
	)

	require.Equal(t, http.StatusTooManyRequests, got.Status)
	require.Equal(t, "rate_limit_error", got.ErrType)
	require.False(t, got.ModelNotFound)
}

func TestM8SelectionFailureMapping(t *testing.T) {
	fallback := noAccountErrorClassification{Status: 503, ErrType: "api_error", Message: "Service temporarily unavailable"}
	for _, tc := range []struct {
		name   string
		e      service.SelectionExhaustion
		status int
		code   string
	}{
		{"unknown", service.SelectionExhaustion{}, 503, ""},
		{"empty", service.SelectionExhaustion{Known: true, Reasons: map[service.SelectionReason]int{service.SelectionReasonNoMembers: 1}}, 503, ""},
		{"model", service.SelectionExhaustion{Known: true, Members: 2, Reasons: map[service.SelectionReason]int{service.SelectionReasonModelNotSupported: 2}}, 404, "model_not_found"},
		{"responses", service.SelectionExhaustion{Known: true, Members: 2, ModelSupported: 2, Reasons: map[service.SelectionReason]int{service.SelectionReasonResponsesRequired: 2}}, 400, "responses_required"},
		{"capacity", service.SelectionExhaustion{Known: true, Members: 2, ModelSupported: 2, RequestSupported: 2, Reasons: map[service.SelectionReason]int{service.SelectionReasonRateLimited: 1, service.SelectionReasonQueueFull: 1}}, 429, ""},
		{"mixed", service.SelectionExhaustion{Known: true, Members: 2, ModelSupported: 2, RequestSupported: 2, Reasons: map[service.SelectionReason]int{service.SelectionReasonRateLimited: 1, service.SelectionReasonBalanceExhausted: 1}}, 503, ""},
		{"dependency", service.SelectionExhaustion{Reasons: map[service.SelectionReason]int{service.SelectionReasonDependencyError: 1}}, 503, ""},
		{"owner", service.SelectionExhaustion{Known: true, Members: 1, Reasons: map[service.SelectionReason]int{service.SelectionReasonOwnerFixed: 1}}, 503, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cls := classifySelectionFailureError(&tc.e, fallback)
			require.Equal(t, tc.status, cls.Status)
			require.Equal(t, tc.code, cls.Code)
		})
	}
	cause := &service.UpstreamFailoverError{StatusCode: 429}
	cls := classifySelectionFailureError(&service.SelectionExhaustion{Known: true, LastCause: cause, Reasons: map[service.SelectionReason]int{service.SelectionReasonNoMembers: 1}}, fallback)
	require.Equal(t, 429, cls.Status)
	require.Same(t, cause, cls.LastCause)
	require.Equal(t, fallback, classifySelectionFailureError(fmt.Errorf("rate_limited=999"), fallback))
	c := newTestGinContextWithRequest()
	c.Request = c.Request.WithContext(service.WithChatRequestRequirement(c.Request.Context(), service.ChatRequestRequirement{HasTools: true, ToolField: "functions"}))
	fd := &fakeDiagnoser{}
	cls = classifyNoAccountErrorFromGin(c, fd, &service.APIKey{GroupID: ptrInt64(7)}, "model", "model", service.PlatformOpenAI, &service.SelectionExhaustion{Known: true, Members: 1, ModelSupported: 1, Reasons: map[service.SelectionReason]int{service.SelectionReasonResponsesRequired: 1}})
	require.Equal(t, "functions", cls.Param)
	require.Contains(t, cls.Message, `"functions"`)
	require.Empty(t, fd.calls)
}
