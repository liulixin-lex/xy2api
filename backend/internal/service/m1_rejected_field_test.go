package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestM1RejectedFieldObservation(t *testing.T) {
	for _, tc := range []struct {
		status                int
		body, response, field string
		repair                bool
	}{
		{400, `{"metadata":{"tag":"test"}}`, `{"error":{"code":"unsupported_parameter","param":"metadata","message":"Unsupported parameter: metadata"}}`, "metadata", true},
		{400, `{"input":"hello"}`, `{"error":{"code":"unsupported_parameter","param":"input","message":"unsupported parameter"}}`, "input", false},
		{400, `{"input":[{"type":"function_call","namespace":"secret","arguments":"{}"}]}`, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].namespace"}}`, "input[0].namespace", true},
		{400, `{"max_output_tokens":20}`, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: max_output_tokens"}}`, "max_output_tokens", true},
		{502, `{"metadata":{}}`, `{"error":{"code":"unsupported_parameter","param":"metadata"}}`, "", false},
		{400, `{"input":"hello"}`, `{"error":{"code":"unknown_parameter","message":"Unknown parameter"}}`, "", false},
		{400, `{"input":"hello"}`, `{"error":{"code":"unknown_parameter","param":"customer-secret","message":"Unknown parameter"}}`, "", false},
		{400, `{"metadata":{}}`, `{"error":{"code":"unknown_parameter","param":"metadata","message":"Unknown parameter: temperature"}}`, "", false},
	} {
		t.Run(tc.response, func(t *testing.T) {
			require.Equal(t, tc.field, openAIResponsesRejectedFieldForRecord(tc.status, []byte(tc.response)))
			_, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(tc.status, []byte(tc.body), []byte(tc.response))
			require.NoError(t, err)
			require.Equal(t, tc.repair, changed)
		})
	}
	// Recognizing a field does not consume or extend the existing repair budget.
	body := []byte(`{"metadata":{"tag":"test"}}`)
	response := []byte(`{"error":{"code":"unknown_parameter","param":"metadata"}}`)
	next, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, response)
	require.NoError(t, err)
	require.True(t, changed)
	state := newOpenAIResponsesRejectedFieldRetryState(body)
	state.budget.attempts = maxOpenAIResponsesRejectedFieldRetries
	require.False(t, state.Allow(next))
	require.Equal(t, "metadata", openAIResponsesRejectedFieldForRecord(http.StatusBadRequest, response))
}

func TestM1RejectedFieldActualResend(t *testing.T) {
	for _, field := range []string{"metadata", "max_output_tokens"} {
		body := []byte(`{"model":"gpt-5.5","input":"hi","metadata":{"tag":"test"},"max_output_tokens":20}`)
		state := resilience.NewRequestState()
		state.EnableGatewayRequest(time.Now())
		ctx := resilience.WithRequestState(context.Background(), state)
		c := newOpenAIRejectedFieldTestContext(body)
		c.Request = c.Request.WithContext(ctx)
		upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(400, `{"error":{"code":"unsupported_parameter","param":"`+field+`","message":"Unsupported parameter: `+field+`"}}`), newOpenAIRejectedFieldTestResponse(200, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}}
		result, err := newOpenAIRejectedFieldTestService(upstream).Forward(ctx, c, newOpenAIRejectedFieldTestAccount(), body)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, upstream.bodies, 2)
		value, exists := c.Get(OpsUpstreamErrorsKey)
		require.True(t, exists)
		events := value.([]*OpsUpstreamErrorEvent)
		require.Len(t, events, 1)
		require.Equal(t, field, events[0].RejectedField)
		require.True(t, events[0].Resent)
		if field == "max_output_tokens" {
			require.Contains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: field, Action: "omit"})
		}
	}
	// An event reference alone is not evidence that a repair reached transport.
	body := []byte(`{"metadata":{}}`)
	c := newOpenAIRejectedFieldTestContext(body)
	state := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), state)
	c.Request = c.Request.WithContext(ctx)
	event := recordOpenAIResponsesRejectedFieldRetry(c, newOpenAIRejectedFieldTestAccount(), newOpenAIRejectedFieldTestResponse(400, `{}`), []byte(`{"error":{"param":"metadata"}}`))
	require.NotNil(t, event)
	require.False(t, event.Resent)
	_, sent := state.OpsUpstreamSendBoundary(newOpenAIRejectedFieldTestAccount().ID)
	require.False(t, sent)
}
