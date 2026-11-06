//go:build unit

package service

import (
	"net/http"
	"testing"

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

	err := svc.nonStreamingTerminalFailureFailover(c, newNonStreamingSSEResponse(), account, false, "error", payload, "upstream rejected")

	require.NotNil(t, err)
	require.Equal(t, http.StatusBadRequest, err.StatusCode)
	require.Equal(t, http.StatusBadRequest, err.WrappedStatus)
	require.Equal(t, payload, err.ResponseBody)
}

func TestOpenAIWrappedUpstreamErrorUsesInnerBodyFor403Classification(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"proxy.upstream_error","status":403,"message":"late status","upstream_body":"{\"error\":{\"type\":\"authentication_error\",\"code\":\"invalid_api_key\",\"message\":\"bad key\"}}"}`)
	inner := openAIWrappedUpstreamErrorClassificationBody(payload)
	require.JSONEq(t, `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"bad key"}}`, string(inner))
	require.True(t, openAIStream403AccountFailure(inner, "bad key"))
}
