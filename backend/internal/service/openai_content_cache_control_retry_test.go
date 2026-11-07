package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyKeepsUnrejectedContentCacheControls(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":[{"type":"input_text","text":"keep","cache_control":{"type":"ephemeral"}}]}]}`)
	for _, responseBody := range []string{
		`{"error":{"type":"invalid_request_error","message":"Invalid request; input[0].content[0].cache_control was supplied"}}`,
		`{"error":{"code":"unsupported_parameter","param":"input[0].content[0].text","message":"Unsupported parameter: 'input[0].content[0].text'"}}`,
		`{"error":{"code":"unknown_parameter","param":"cache_control","message":"Unknown parameter: 'cache_control'"}}`,
		`{"error":{"code":"unsupported_parameter","param":"input[0].content[0].text","message":"Unsupported parameter: 'input[0].content[0].cache_control'"}}`,
		`{"error":{"type":"invalid_request_error","message":"Unknown parameter: input[0].content[0].cache_control.type"}}`,
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
