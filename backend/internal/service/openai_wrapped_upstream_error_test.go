//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWrappedUpstreamErrorStatusE1(t *testing.T) {
	t.Parallel()

	status, wrapped, malformed := openAIWrappedUpstreamErrorStatus("error", []byte(`{"type":"proxy.upstream_error","status":403,"message":"late status"}`))
	require.Equal(t, http.StatusForbidden, status)
	require.True(t, wrapped)
	require.False(t, malformed)

	status, wrapped, malformed = openAIWrappedUpstreamErrorStatus("", []byte(`{"type":"proxy.upstream_error","status":403}`))
	require.Zero(t, status)
	require.False(t, wrapped)
	require.True(t, malformed)
}

func TestOpenAIWrappedUpstreamErrorStatusE2(t *testing.T) {
	t.Parallel()

	status, wrapped, malformed := openAIWrappedUpstreamErrorStatus("response.failed", []byte(`{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"413","status_code":413}}}`))
	require.Equal(t, http.StatusRequestEntityTooLarge, status)
	require.True(t, wrapped)
	require.False(t, malformed)
}

func TestOpenAIWrappedUpstreamErrorStatusRejectsMalformedNumbers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		event   string
		payload string
	}{
		{"string status", "error", `{"type":"proxy.upstream_error","status":"400"}`},
		{"float status", "error", `{"type":"proxy.upstream_error","status":400.0}`},
		{"exponent status", "error", `{"type":"proxy.upstream_error","status":4e2}`},
		{"out of range", "error", `{"type":"proxy.upstream_error","status":399}`},
		{"mismatched E2", "response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"400","status_code":403}}}`},
		{"nondecimal code", "response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"4e2","status_code":400}}}`},
		{"truncated E1 JSON", "error", `{"type":"proxy.upstream_error","status":400`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, wrapped, malformed := openAIWrappedUpstreamErrorStatus(tc.event, []byte(tc.payload))
			require.Zero(t, status)
			require.False(t, wrapped)
			require.True(t, malformed)
		})
	}
}

func TestOpenAIWrappedSemanticStatusUsesValidatedFrameStatus(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"403","status_code":403}}}`)
	status, wrapped, malformed := openAIWrappedUpstreamErrorStatus("response.failed", payload)
	require.True(t, wrapped)
	require.False(t, malformed)
	require.Equal(t, http.StatusForbidden, openAIStreamFailedEventSemanticStatus(payload, "", status))
}

func TestOpenAIWrappedStreamFailoverUsesHTTPStatusPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "400"},
		{http.StatusForbidden, "403"},
		{http.StatusUnauthorized, "401"},
		{http.StatusRequestEntityTooLarge, "413"},
		{http.StatusTooManyRequests, "429"},
		{http.StatusBadGateway, "502"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			c, _ := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()
			payload := []byte(`{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"` + tc.code + `","status_code":` + tc.code + `,"message":"upstream rejected"}}}`)
			status, wrapped, malformed := openAIWrappedUpstreamErrorStatus("response.failed", payload)
			require.Equal(t, tc.status, status)
			require.True(t, wrapped)
			require.False(t, malformed)

			err := svc.newOpenAIWrappedStreamFailoverError(c.Request.Context(), c, newNonStreamingFailoverAccount(), false, "rid-wrapper", payload, "upstream rejected", "model", nil, status)
			require.NotNil(t, err)
			require.Equal(t, tc.status, err.StatusCode)
			require.Equal(t, tc.status, err.WrappedStatus)
			require.Equal(t, payload, err.ResponseBody)
		})
	}
}

func TestNonStreamingWrappedErrorFrameBypassesConservativeEventErrorCheck(t *testing.T) {
	c, _ := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	account := newNonStreamingFailoverAccount()
	payload := []byte(`{"type":"proxy.upstream_error","status":400,"message":"upstream rejected"}`)

	err := svc.nonStreamingTerminalFailureFailover(c, newNonStreamingSSEResponse(), account, false, "error", "error", payload, "upstream rejected")

	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.StatusCode)
	require.Equal(t, http.StatusBadRequest, err.WrappedStatus)
	require.Equal(t, payload, err.ResponseBody)
}

func TestNonStreamingWrappedErrorPreservesOriginalSSEEvent(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		status        int
	}{
		{"E1", `{"type":"proxy.upstream_error","status":400,"message":"upstream rejected"}`, 400},
		{"E2", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"503","status_code":503,"message":"busy"}}}`, 503},
	} {
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/passthrough=%t", tc.name, passthrough), func(t *testing.T) {
				c, rec := newNonStreamingFailoverContext(t)
				svc := newNonStreamingFailoverService()
				body := []byte("event: error\ndata: " + tc.payload + "\n\n")
				var err error
				if passthrough {
					_, err = svc.handlePassthroughSSEToJSON(newNonStreamingSSEResponse(), c, newNonStreamingFailoverAccount(), body, "model", "model")
				} else {
					_, err = svc.handleSSEToJSON(newNonStreamingSSEResponse(), c, newNonStreamingFailoverAccount(), body, "model", "model")
				}
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, tc.status, failover.StatusCode)
				require.Equal(t, tc.status, failover.WrappedStatus)
				require.Empty(t, rec.Body.String())
			})
		}
	}
}

func TestOpenAIWrappedUpstreamErrorUsesInnerBodyFor403Classification(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"proxy.upstream_error","status":403,"message":"late status","upstream_body":"{\"error\":{\"type\":\"authentication_error\",\"code\":\"invalid_api_key\",\"message\":\"bad key\"}}"}`)
	inner := openAIWrappedUpstreamErrorClassificationBody(payload)
	require.JSONEq(t, `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"bad key"}}`, string(inner))
	require.True(t, openAIStream403AccountFailure(inner, "bad key"))
}

func TestOpenAIWrappedFailoverRetainsInnerClassificationBody(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) { s.Failure.DecisionV2 = true })
	for _, tc := range []struct {
		name, inner string
		status      int
		category    resilience.Category
		capacity    bool
	}{
		{"capacity", `{"error":{"code":"server_is_overloaded","message":"request declined"}}`, 503, resilience.CategoryCapacityShed, true},
		{"balance", `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"request declined"}}`, 429, resilience.CategoryBalance, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()
			// E1 relays may serialize upstream_body as a JSON string.
			payload, err := json.Marshal(map[string]any{"type": "proxy.upstream_error", "status": tc.status, "message": "late upstream status", "upstream_body": tc.inner})
			require.NoError(t, err)
			failure := svc.newOpenAIWrappedStreamFailoverError(c.Request.Context(), c, newNonStreamingFailoverAccount(), false, "rid-inner", payload, "late upstream status", "model", nil, tc.status)
			require.NotNil(t, failure)
			require.Equal(t, tc.status, failure.StatusCode)
			require.Equal(t, tc.status, failure.WrappedStatus)
			require.JSONEq(t, tc.inner, string(failure.ResponseBody))
			require.Equal(t, tc.capacity, failure.IsOpenAICapacityShed())
			require.Equal(t, tc.category, ClassifyFailoverError(failure).Category)
		})
	}
}

func TestOpenAIWrappedAuthStatusesRetainInnerBodyForIntegration(t *testing.T) {
	// The integration branch owns 401/403 soft-cooldown routing. This contract
	// only ensures its downstream classifier receives the decoded origin body.
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, _ := newNonStreamingFailoverContext(t)
			svc := newNonStreamingFailoverService()
			inner := `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"origin rejected"}}`
			payload, err := json.Marshal(map[string]any{"type": "proxy.upstream_error", "status": status, "message": "late upstream status", "upstream_body": json.RawMessage(inner)})
			require.NoError(t, err)
			failure := svc.newOpenAIWrappedStreamFailoverError(c.Request.Context(), c, newNonStreamingFailoverAccount(), false, "rid-inner", payload, "late upstream status", "model", nil, status)
			require.NotNil(t, failure)
			require.Equal(t, status, failure.WrappedStatus)
			require.JSONEq(t, inner, string(failure.ResponseBody))
		})
	}
}

func TestOpenAIWrapped400RetainsOriginalBodyForM3(t *testing.T) {
	c, _ := newNonStreamingFailoverContext(t)
	svc := newNonStreamingFailoverService()
	payload := []byte(`{"type":"proxy.upstream_error","status":400,"message":"late upstream status","upstream_body":{"error":{"code":"conversation_invalid","message":"origin rejected"}}}`)
	failure := svc.newOpenAIWrappedStreamFailoverError(c.Request.Context(), c, newNonStreamingFailoverAccount(), false, "rid-m3", payload, "late upstream status", "model", nil, 400)
	require.NotNil(t, failure)
	require.Equal(t, http.StatusBadRequest, failure.WrappedStatus)
	require.Equal(t, payload, failure.ResponseBody)
}
