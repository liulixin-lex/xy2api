package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesContentCacheControls(t *testing.T) {
	body := []byte(`{"cache_control":{"type":"ephemeral"},"input":[{"type":"message","cache_control":{"type":"ephemeral"},"content":[{"type":"input_text","text":"keep","cache_control":{"type":"ephemeral"}},{"type":"input_image","image_url":"https://image.example/p.png","cache_control":null}]},{"type":"message","content":[{"type":"input_text","text":"{\"cache_control\":true}","cache_control":{"type":"ephemeral"}}]},{"type":"function_call","arguments":"{\"cache_control\":true}"}],"tools":[{"cache_control":{"type":"ephemeral"},"parameters":{"cache_control":true}}]}`)
	expected := `{"cache_control":{"type":"ephemeral"},"input":[{"type":"message","cache_control":{"type":"ephemeral"},"content":[{"type":"input_text","text":"keep"},{"type":"input_image","image_url":"https://image.example/p.png"}]},{"type":"message","content":[{"type":"input_text","text":"{\"cache_control\":true}"}]},{"type":"function_call","arguments":"{\"cache_control\":true}"}],"tools":[{"cache_control":{"type":"ephemeral"},"parameters":{"cache_control":true}}]}`
	for _, responseBody := range []string{
		`{"error":{"code":"unsupported_parameter","param":"input[0].content[0].cache_control","message":"Unsupported parameter"}}`,
		`{"error":{"type":"invalid_request_error","message":"Unknown parameter: 'input[0].content[0].cache_control'."}}`,
	} {
		t.Run(responseBody, func(t *testing.T) {
			retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(responseBody))
			require.NoError(t, err)
			require.True(t, changed)
			require.JSONEq(t, expected, string(retryBody))

			state := newOpenAIResponsesRejectedFieldRetryState(body)
			require.True(t, state.Allow(retryBody))
			require.False(t, state.Allow(retryBody), "the existing loop guard rejects a duplicate replay")
			_, _, changed, err = normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, retryBody, []byte(responseBody))
			require.NoError(t, err)
			require.False(t, changed, "a repeated rejection cannot strip the hints again")
		})
	}
}

const openAIChatContentCacheRetryBody = `{"model":"public","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}},{"type":"text","text":"second"},{"type":"text","text":"third"},{"type":"text","text":"fourth","cache_control":{"type":"ephemeral"}}]}]}`

const openAIChatContentCacheRetryError = `{"code":"unknown_parameter","message":"Unknown parameter: 'input[0].content[0].cache_control'.","param":"input[0].content[0].cache_control","type":"invalid_request_error"}`

const openAIChatFieldRetrySuccessStream = "event: response.created\ndata: {\"response\":{\"id\":\"resp_retry\",\"model\":\"public\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retry\",\"model\":\"public\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\ndata: [DONE]\n\n"

func newOpenAIChatFieldRetryContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestOpenAIGatewayService_ChatStreamRejectedFieldReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		error   string
		removed []string
	}{
		{
			name:    "cache_control",
			body:    openAIChatContentCacheRetryBody,
			error:   openAIChatContentCacheRetryError,
			removed: []string{"input.0.content.0.cache_control", "input.0.content.3.cache_control"},
		},
		{
			name:    "top_p",
			body:    `{"model":"public","stream":true,"top_p":0.7,"messages":[{"role":"user","content":"hello"}]}`,
			error:   `{"code":"unsupported_parameter","message":"Unsupported parameter: 'top_p'.","param":"top_p","type":"invalid_request_error"}`,
			removed: []string{"top_p"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(
				"event: response.created\ndata: {\"response\":{\"id\":\"resp_rejected\",\"model\":\"public\"}}\n\n" +
					"event: response.failed\ndata: {\"response\":{\"error\":" + tc.error + "}}\n\n")}
			first := newOpenAIRejectedFieldTestResponse(http.StatusOK, "")
			first.Body = firstBody
			first.Header.Set("Content-Type", "text/event-stream")
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				first, newOpenAIRejectedFieldTestResponse(http.StatusOK, openAIChatFieldRetrySuccessStream),
			}}
			service := newOpenAIRejectedFieldTestService(upstream)
			service.httpUpstream = &openAIRejectedFieldReplayUpstream{httpUpstreamRecorder: upstream, beforeReplay: func() {
				require.True(t, firstBody.closed, "the old stream must close before replay")
				require.ErrorIs(t, upstream.requests[0].Context().Err(), context.Canceled)
			}}
			c, recorder := newOpenAIChatFieldRetryContext([]byte(tc.body))
			result, err := service.ForwardAsChatCompletions(context.Background(), c, newOpenAIRejectedFieldTestAccount(), []byte(tc.body), "", "")
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.bodies, 2)
			for _, path := range tc.removed {
				require.True(t, gjson.GetBytes(upstream.bodies[0], path).Exists())
				require.False(t, gjson.GetBytes(upstream.bodies[1], path).Exists())
			}
			require.Equal(t, upstream.requests[0].URL, upstream.requests[1].URL)
			require.Equal(t, upstream.requests[0].Header.Get("Authorization"), upstream.requests[1].Header.Get("Authorization"))
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), `"content":"ok"`)
			require.NotContains(t, recorder.Body.String(), `"error"`)
			require.NotContains(t, recorder.Body.String(), "resp_rejected")
			require.NotContains(t, recorder.Body.String(), "response.failed")
			require.Equal(t, 1, strings.Count(recorder.Body.String(), "[DONE]"))
			require.Equal(t, 1, result.Usage.OutputTokens)
		})
	}
}

func TestOpenAIGatewayService_ChatStreamRejectedFieldAfterOutputDoesNotReplay(t *testing.T) {
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
		"event: response.failed\ndata: {\"response\":{\"error\":" + openAIChatContentCacheRetryError + "}}\n\n"
	upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(http.StatusOK, stream)}}
	body := []byte(openAIChatContentCacheRetryBody)
	c, recorder := newOpenAIChatFieldRetryContext(body)
	result, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), c, newOpenAIRejectedFieldTestAccount(), body, "", "")
	require.Error(t, err)
	require.NotNil(t, result)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Len(t, upstream.bodies, 1)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "input.0.content.0.cache_control").Exists())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"content":"partial"`)
	require.Contains(t, recorder.Body.String(), `"error"`)
	require.Contains(t, recorder.Body.String(), "Unknown parameter: 'input[0].content[0].cache_control'.")
	require.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestOpenAIGatewayService_ChatHTTPRetriesRewrappedContentCacheControlRejection(t *testing.T) {
	const rejection = `{"error":{"message":"Unknown parameter: 'input[0].content[3].cache_control'.","type":"upstream_error"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, rejection),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, openAIChatFieldRetrySuccessStream),
	}}
	body := []byte(openAIChatContentCacheRetryBody)
	c, recorder := newOpenAIChatFieldRetryContext(body)
	result, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), c, newOpenAIRejectedFieldTestAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	for _, path := range []string{"input.0.content.0.cache_control", "input.0.content.3.cache_control"} {
		require.True(t, gjson.GetBytes(upstream.bodies[0], path).Exists())
		require.False(t, gjson.GetBytes(upstream.bodies[1], path).Exists())
	}
	require.Contains(t, recorder.Body.String(), `"content":"ok"`)
	require.NotContains(t, recorder.Body.String(), `"error"`)
}

func TestOpenAIGatewayService_ChatStreamRejectedFieldRespectsSharedRetryBudget(t *testing.T) {
	body := []byte(strings.Replace(openAIChatContentCacheRetryBody, `"stream":true`, `"stream":true,"top_p":0.7`, 1))
	c, recorder := newOpenAIChatFieldRetryContext(body)
	priorAttempt := openAIResponsesRejectedFieldRetryStateForRequest(c, body)
	for i := 0; i < maxOpenAIResponsesRejectedFieldRetries-1; i++ {
		require.True(t, priorAttempt.Allow([]byte(fmt.Sprintf(`{"variant":%d}`, i))))
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: 'top_p'.","param":"top_p"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, "event: response.failed\ndata: {\"response\":{\"error\":"+openAIChatContentCacheRetryError+"}}\n\n"),
	}}
	_, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), c, newOpenAIRejectedFieldTestAccount(), body, "", "")
	require.Error(t, err)
	require.Len(t, upstream.bodies, 2, "the HTTP repair consumes the last slot, so the in-band error must terminate")
	require.False(t, gjson.GetBytes(upstream.bodies[1], "top_p").Exists())
	require.True(t, gjson.GetBytes(upstream.bodies[1], "input.0.content.0.cache_control").Exists())
	require.Contains(t, recorder.Body.String(), "Unknown parameter: 'input[0].content[0].cache_control'.")
	require.False(t, priorAttempt.Allow([]byte(`{"after":"exhaustion"}`)))
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyKeepsUnrejectedContentCacheControls(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":[{"type":"input_text","text":"keep","cache_control":{"type":"ephemeral"}}]}]}`)
	for _, responseBody := range []string{
		`{"error":{"type":"invalid_request_error","message":"Invalid request; input[0].content[0].cache_control was supplied"}}`,
		`{"error":{"code":"unsupported_parameter","param":"input[0].content[0].text","message":"Unsupported parameter: 'input[0].content[0].text'"}}`,
		`{"error":{"code":"unknown_parameter","param":"cache_control","message":"Unknown parameter: 'cache_control'"}}`,
		`{"error":{"code":"unsupported_parameter","param":"input[0].content[0].text","message":"Unsupported parameter: 'input[0].content[0].cache_control'"}}`,
		`{"error":{"type":"invalid_request_error","message":"Unknown parameter: input[0].content[0].cache_control.type"}}`,
		`{"error":{"type":"upstream_error","message":"Unknown parameter: input[0].content[0].cache_control.ttl. Remove it and retry."}}`,
		`{"error":{"type":"upstream_error","message":"Unknown parameter: tools[0].cache_control, metadata"}}`,
	} {
		t.Run(responseBody, func(t *testing.T) {
			retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(responseBody))
			require.NoError(t, err)
			require.False(t, changed)
			require.Nil(t, retryBody)
		})
	}
	_, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusInternalServerError, body,
		[]byte(`{"error":{"code":"unknown_parameter","param":"input[0].content[0].cache_control"}}`))
	require.NoError(t, err)
	require.False(t, changed, "only a 400 rejection authorizes the repair")
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyContentCacheMessagePunctuation(t *testing.T) {
	for _, message := range []string{
		"Unknown parameter: input[0].content[3].cache_control. Remove it and retry.",
		"Unsupported parameter: input[0].content[3].cache_control, metadata",
	} {
		t.Run(message, func(t *testing.T) {
			responseBody := []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error"}}`, message))
			retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, []byte(openAIChatContentCacheRetryBody), responseBody)
			require.NoError(t, err)
			require.True(t, changed)
			require.False(t, gjson.GetBytes(retryBody, "messages.0.content.0.cache_control").Exists())
			require.False(t, gjson.GetBytes(retryBody, "messages.0.content.3.cache_control").Exists())
			require.Equal(t, "fourth", gjson.GetBytes(retryBody, "messages.0.content.3.text").String())
		})
	}
}

func TestOpenAIGatewayService_ChatCompletionsContentCacheControlReplay(t *testing.T) {
	body := []byte(`{"model":"public","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}},{"type":"text","text":"second","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":[{"type":"text","text":"third","cache_control":{"type":"ephemeral"}}]}]}`)
	const rejection = `{"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: 'input[0].content[0].cache_control'.","param":"input[0].content[0].cache_control"}}`
	const unrelatedRejection = `{"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: 'input[0].content[0].text'.","param":"input[0].content[0].text"}}`
	const success = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cache_replay\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\ndata: [DONE]\n\n"
	for _, tc := range []struct {
		name       string
		firstError string
		rejectNext bool
	}{
		{name: "repair succeeds", firstError: rejection},
		{name: "repeated rejection terminates", firstError: rejection, rejectNext: true},
		{name: "unrelated rejection terminates", firstError: unrelatedRejection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, tc.firstError),
			}}
			if tc.rejectNext {
				upstream.responses = append(upstream.responses, newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, rejection))
			} else {
				upstream.responses = append(upstream.responses, newOpenAIRejectedFieldTestResponse(http.StatusOK, success))
			}
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			account := newOpenAIRejectedFieldTestAccount()
			result, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			if tc.rejectNext || tc.firstError == unrelatedRejection {
				var rejectionErr *OpenAIRequestRejectionError
				require.ErrorAs(t, err, &rejectionErr)
				require.Nil(t, result)
				require.Equal(t, http.StatusBadRequest, c.Writer.Status())
				require.Equal(t, "invalid_request_error", gjson.Get(recorder.Body.String(), "error.type").String())
				require.Equal(t, gjson.Get(tc.firstError, "error.message").String(), gjson.Get(recorder.Body.String(), "error.message").String())
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, http.StatusOK, c.Writer.Status())
			}
			wantAttempts := 2
			if tc.firstError == unrelatedRejection {
				wantAttempts = 1
			}
			require.Len(t, upstream.bodies, wantAttempts)
			for _, path := range []string{"input.0.content.0", "input.0.content.1", "input.1.content.0"} {
				require.True(t, gjson.GetBytes(upstream.bodies[0], path+".cache_control").Exists(), "no proactive stripping")
				if wantAttempts == 2 {
					require.False(t, gjson.GetBytes(upstream.bodies[1], path+".cache_control").Exists())
					require.Equal(t, gjson.GetBytes(upstream.bodies[0], path+".text").String(), gjson.GetBytes(upstream.bodies[1], path+".text").String())
				}
			}
			if wantAttempts == 2 {
				require.Equal(t, upstream.requests[0].URL, upstream.requests[1].URL)
				require.Equal(t, upstream.requests[0].Header.Get("Authorization"), upstream.requests[1].Header.Get("Authorization"))
			}
		})
	}
}
