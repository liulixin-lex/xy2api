//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCapabilityScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want UpstreamCapabilityScope
	}{
		{"model code", `{"error":{"code":"model_not_found"}}`, UpstreamCapabilityModel},
		{"model template", `{"detail":"model synthetic-model is not available for this synthetic-account"}`, UpstreamCapabilityModel},
		{"model explicit", `{"error":{"code":"MODEL_UNSUPPORTED"}}`, UpstreamCapabilityModel},
		{"protocol code", `{"error":{"code":"unsupported_protocol"}}`, UpstreamCapabilityProtocol},
		{"protocol inverse", `{"error":{"code":"protocol_not_supported"}}`, UpstreamCapabilityProtocol},
		{"stream mode", `{"error":{"message":"Only supports streaming requests"}}`, UpstreamCapabilityProtocol},
		{"input modality", `{"error":{"message":"This upstream supports text and https input_image content only"}}`, UpstreamCapabilityRequestFeature},
		{"client kind", `{"error":{"message":"Desktop frontend requests native-client are not supported on this service"}}`, UpstreamCapabilityRequestFeature},
		{"input code", `{"error":{"code":"unsupported_input_capability"}}`, UpstreamCapabilityRequestFeature},
		{"frontend code", `{"error":{"code":"unsupported_frontend"}}`, UpstreamCapabilityRequestFeature},
		{"request type ambiguous", `{"error":{"code":"unsupported_request_type"}}`, UpstreamCapabilityRequestFeature},
		{"request type inverse", `{"error":{"code":"request_type_not_supported"}}`, UpstreamCapabilityRequestFeature},
		{"site ambiguous", `{"error":{"message":"This site does not support this request"}}`, UpstreamCapabilityRequestFeature},
		{"upstream ambiguous", `{"error":{"message":"This upstream does not support this request"}}`, UpstreamCapabilityRequestFeature},
		{"endpoint ambiguous", `{"error":{"message":"This endpoint does not support this request"}}`, UpstreamCapabilityRequestFeature},
		{"chinese ambiguous", `{"error":"站点不支持这种请求"}`, UpstreamCapabilityRequestFeature},
		{"flat model", `{"type":"error","status_code":400,"code":"model_not_found","message":"Unavailable"}`, UpstreamCapabilityModel},
		{"flat stream mode", `{"type":"error","code":"stream_only","message":"Only supports streaming requests"}`, UpstreamCapabilityProtocol},
		{"ordinary output stream code", `{"type":"output","code":"stream_only","message":"Only supports streaming requests"}`, UpstreamCapabilityNone},
		{"ordinary input stream code", `{"type":"input","code":"stream_only","message":"Only supports streaming requests"}`, UpstreamCapabilityNone},
		{"ordinary output", `{"type":"output","status_code":400,"code":"model_not_found","message":"Unavailable"}`, UpstreamCapabilityNone},
		{"ordinary input", `{"input":{"type":"error","code":"model_not_found"}}`, UpstreamCapabilityNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := ClassifyUpstreamCapabilityFailure(nil, 400, []byte(tc.body))
			require.Equal(t, tc.want, scope)
			require.Equal(t, tc.want == UpstreamCapabilityModel || tc.want == UpstreamCapabilityProtocol, scope.Markable())
			require.Equal(t, scope, ClassifyUpstreamCapabilityEventFailure([]byte(tc.body)))
		})
	}
}

func TestCapability401RequiresQualifiedAccount(t *testing.T) {
	body := []byte(`{"error":{"code":"model_not_found"}}`)
	for _, tc := range []struct {
		name, platform, kind string
		want                 UpstreamCapabilityScope
		class                schedquality.ErrorClass
	}{
		{"API key OpenAI", PlatformOpenAI, AccountTypeAPIKey, UpstreamCapabilityModel, schedquality.ClassCapability},
		{"OAuth OpenAI", PlatformOpenAI, AccountTypeOAuth, UpstreamCapabilityNone, schedquality.ClassAccountAccess},
		{"API key native", PlatformAnthropic, AccountTypeAPIKey, UpstreamCapabilityNone, schedquality.ClassAccountAccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 1, Platform: tc.platform, Type: tc.kind}
			require.Equal(t, tc.want, ClassifyUpstreamCapabilityFailure(account, 401, body))
			require.Equal(t, UpstreamCapabilityNone, ClassifyUpstreamCapabilityFailure(account, 401, []byte(`{"error":{"code":"unsupported_protocol"}}`)), "the qualified401 exception only covers model availability")
			if tc.platform != PlatformOpenAI {
				return
			}
			st := resilience.NewRequestState()
			st.SetSchedSelection(resilience.SchedSelection{AccountID: 1})
			rec := &errclassRecorder{}
			svc := &OpenAIGatewayService{schedAttemptRecorder: rec}
			capture := svc.beginSchedAttempt(resilience.WithRequestState(context.Background(), st), account, nil, 0, false)
			capture.finishHTTP(&http.Response{StatusCode: 401}, body)
			capture.finish(nil, nil)
			require.Len(t, rec.finishes, 1)
			require.Equal(t, tc.class, rec.finishes[0].Class)
		})
	}
	require.Equal(t, UpstreamCapabilityNone, ClassifyUpstreamCapabilityEventFailure([]byte(`{"type":"error","status_code":401,"code":"model_not_found"}`)))
}

func TestFlatErrorAttribution(t *testing.T) {
	for _, tc := range []struct {
		body string
		want schedquality.ErrorClass
	}{
		{`{"type":"error","status_code":429,"code":"account_rate_limit_exceeded","message":"Limit reached"}`, schedquality.ClassAccountCapacity},
		{`{"type":"error","status_code":400,"code":"model_not_found","message":"Unavailable"}`, schedquality.ClassCapability},
		{`{"type":"error","status_code":503,"code":"model_not_found","message":"Unavailable"}`, schedquality.ClassUpstream},
		{`{"type":"output","code":"account_rate_limit_exceeded","message":"Limit reached"}`, schedquality.ClassUnknown},
	} {
		out := classifySchedAttempt(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault}, &resilience.Outcome{Source: resilience.SourceStreamEvent, StatusCode: 200, Body: []byte(tc.body)}, false, false)
		require.Equal(t, tc.want, out.Class, tc.body)
		require.Equal(t, 200, out.HTTPStatus)
	}
}
