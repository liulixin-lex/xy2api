//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAILocalFailureBody struct {
	io.Reader
	closed bool
}

func (b *openAILocalFailureBody) Close() error { b.closed = true; return nil }

func TestOpenAILocalStreamFailurePreservesResponseAndRetryAdvice(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := compatStreamContext("/v1/responses", `{}`)
	ctx := resilience.WithRetryAdvice(c.Request.Context(), resilience.RetryAdvice{Seconds: 7})
	c.Request = c.Request.WithContext(ctx)
	cause := errors.New("connection reset by peer")
	body := &openAILocalFailureBody{Reader: &openAIStreamReadThenErrorCloser{
		reader: strings.NewReader("data: {\"type\":\"response.created\",\"sequence_number\":4,\"response\":{\"id\":\"resp_original\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":5,\"delta\":\"partial\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}\n"),
		err: cause,
	}}
	svc := &OpenAIGatewayService{}
	result, err := svc.handleStreamingResponse(ctx, &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, c,
		&Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
	require.ErrorIs(t, err, cause)
	require.True(t, body.closed)
	require.Equal(t, 7, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
	require.NotContains(t, rec.Body.String(), "event: error\n")
	require.Contains(t, rec.Body.String(), "\n\nevent: response.failed\n", "a truncated event must end before the failure frame")
	var failed gjson.Result
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") && gjson.Get(line[6:], "type").String() == "response.failed" {
			failed = gjson.Parse(line[6:])
		}
	}
	require.Equal(t, "resp_original", failed.Get("response.id").String())
	require.Equal(t, int64(6), failed.Get("sequence_number").Int())
	require.Equal(t, "server_error", failed.Get("response.error.code").String())
	require.Equal(t, "7", failed.Get("response.error.headers.retry-after").String())
	require.True(t, IsResponseCommitted(c))
}

func TestOpenAILocalStreamFailurePreservesUnterminatedVisiblePayload(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"truncated_json", `{"type":"response.output_text.delta","delta":"partial`},
		{"unknown_event", `{"type":"response.vendor_progress","value":"partial"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/responses", `{}`)
			cause := errors.New("connection reset by peer")
			prefix := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_original\"}}\n\n" +
				"data: " + tc.payload + "\n"
			body := &openAILocalFailureBody{Reader: &openAIStreamReadThenErrorCloser{
				reader: strings.NewReader(prefix),
				err:    cause,
			}}
			svc := &OpenAIGatewayService{}
			_, err := svc.handleStreamingResponse(c.Request.Context(), &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, c,
				&Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
			require.ErrorIs(t, err, cause)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.True(t, strings.HasPrefix(rec.Body.String(), prefix+"\nevent: response.failed\n"), rec.Body.String())
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed\n"))
			require.True(t, body.closed)
			require.True(t, IsResponseCommitted(c))
		})
	}
}
