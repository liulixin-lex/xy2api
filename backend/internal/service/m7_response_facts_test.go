//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestM7NilResultResponseFacts(t *testing.T) {
	c, _ := compatStreamContext("/v1/messages", "")
	state := resilience.NewRequestState()
	state.EnableGatewayRequest(time.Now())
	state.NoteGatewaySend(11)
	c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
	svc := &OpenAIGatewayService{}
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.done\",\"response\":{\"status\":\"future\"}}\n\n"))}
	result, err := svc.handleAnthropicBufferedStreamingResponse(resp, c, &Account{ID: 11}, "fixture", "fixture", "fixture", time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	// M8 owns freezing these fields into metadata; inspect the actual live
	// trace here to test M7's publication without a replacement production API.
	trace := reflect.ValueOf(state).Elem().FieldByName("gatewayRecord").Elem()
	present := trace.FieldByName("UsageObserved")
	require.False(t, present.IsNil())
	require.False(t, present.Elem().Bool())
	require.Equal(t, "missing", trace.FieldByName("UsageSource").Elem().String())
	require.True(t, trace.FieldByName("TerminalEventSeen").Elem().Bool())
	require.False(t, trace.FieldByName("TerminalEventValid").Elem().Bool())
	require.Equal(t, "sse", trace.FieldByName("ResponseBodyType").Elem().String())
}

func TestM7UnknownTerminalMapping(t *testing.T) {
	for _, source := range []string{"chat", "anthropic"} {
		c, rec := compatStreamContext("/v1/responses", "")
		state := resilience.NewRequestState()
		c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
		var err error
		if source == "chat" {
			wire := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"eos_token\"}]}\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			_, err = (&OpenAIGatewayService{}).streamChatCompletionsAsResponses(c, resp, &Account{ID: 1}, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now())
		} else {
			wire := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"some_future_reason\"}}\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
			_, err = (&GatewayService{}).handleResponsesBufferedStreamingResponse(resp, c, nil, "fixture", "fixture", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
		}
		require.NoError(t, err)
		require.Equal(t, 200, rec.Code)
		field := "finish_reason"
		if source == "anthropic" {
			field = "stop_reason"
		}
		require.Equal(t, []apicompat.ConversionNotice{{Field: field, Action: "map"}}, resilience.SnapshotConversionNotices(c.Request.Context()))
	}
}

func TestM7RecoveryHintPreserved(t *testing.T) {
	for _, row := range []struct {
		name, fields, header string
		seconds              int
	}{
		{"nested", "\"headers\":{\"retry-after\":\"300\"}", "", 300},
		{"body_wins", "\"retry_after\":310", "1", 310},
		{"seconds_win", "\"retry_after_ms\":1000,\"retry_after\":310", "", 310},
		{"string", "\"retry_after\":\"310\"", "", 310},
		{"zero", "\"retry_after\":0", "", 0},
	} {
		t.Run(row.name, func(t *testing.T) {
			c, _ := compatStreamContext("/v1/messages", "")
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			payload := `{"type":"response.failed","response":{"status":"failed","error":{"message":"fixture failed",` + row.fields + `},"usage":{"input_tokens":13,"output_tokens":5}}}`
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}
			if row.header != "" {
				resp.Header.Set("Retry-After", row.header)
			}
			result, err := (&OpenAIGatewayService{}).handleAnthropicBufferedStreamingResponse(resp, c, &Account{ID: 1}, "fixture", "fixture", "fixture", time.Now())
			require.Error(t, err)
			require.NotNil(t, result)
			if row.seconds == 0 {
				require.Nil(t, state.UpstreamRecoveryHint())
				require.Nil(t, result.UpstreamRecoveryHint)
			} else {
				require.NotNil(t, result.UpstreamRecoveryHint)
				require.Equal(t, time.Duration(row.seconds)*time.Second, result.UpstreamRecoveryHint.Delay)
				require.Equal(t, state.UpstreamRecoveryHint(), result.UpstreamRecoveryHint)
			}
		})
	}
}

func TestM7UsagePresenceAndTTL(t *testing.T) {
	usage := &ClaudeUsage{InputTokens: 5, OutputTokens: 0, CacheCreationInputTokens: 9, CacheCreation5mTokens: 4, CacheCreation1hTokens: 5,
		InputTokensPresent: true, OutputTokensPresent: true, CacheCreationInputTokensPresent: true, CacheCreation5mTokensPresent: true, CacheCreation1hTokensPresent: true}
	result := claudeUsageToOpenAIUsage(usage)
	require.Equal(t, 14, result.InputTokens)
	require.True(t, result.OutputTokensPresent)
	require.Equal(t, 4, result.CacheCreation5mTokens)
	require.Equal(t, 5, result.CacheCreation1hTokens)
	require.True(t, result.CacheCreation5mTokensPresent)
	require.True(t, result.CacheCreation1hTokensPresent)
	before, err := json.Marshal(result)
	require.NoError(t, err)
	result.InputTokensPresent = false
	result.OutputTokensPresent = false
	result.CacheCreation5mTokensPresent = false
	result.CacheCreation1hTokensPresent = false
	after, err := json.Marshal(result)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	raw := extractCCStreamUsage(`{"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	require.NotNil(t, raw)
	require.True(t, raw.InputTokensPresent)
	require.True(t, raw.OutputTokensPresent)
}
