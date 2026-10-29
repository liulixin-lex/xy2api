//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAuthoritativeFailedEndsBareErrorWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withResilienceSettings(t, nil)
	opening := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	bare := "data: {\"type\":\"error\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"context too long\"}}\n\n"
	failed := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed\",\"status\":\"failed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2},\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"context too long\"}}}\n\n"
	for _, passthrough := range []bool{false, true} {
		for _, async := range []bool{false, true} {
			for _, terminal := range []bool{false, true} {
				name := map[bool]string{false: "native", true: "passthrough"}[passthrough] + "/" + map[bool]string{false: "sync", true: "async"}[async] + "/" + map[bool]string{false: "bare_eof", true: "failed_open_connection"}[terminal]
				t.Run(name, func(t *testing.T) {
					cfg := &config.Config{}
					if async {
						cfg.Gateway.StreamKeepaliveInterval = 1
						cfg.Gateway.StreamDataIntervalTimeout = 30
					}
					svc := &OpenAIGatewayService{cfg: cfg}
					var body io.ReadCloser = io.NopCloser(strings.NewReader(opening + bare))
					if terminal {
						body = &hangingOpenAISSEAfterTerminal{payload: []byte(opening + bare + failed), release: make(chan struct{})}
					}
					t.Cleanup(func() { _ = body.Close() })
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}
					account := &Account{ID: 811, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
					type outcome struct {
						usage *OpenAIUsage
						err   error
					}
					done := make(chan outcome, 1)
					go func() {
						if passthrough {
							result, err := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
							done <- outcome{result.usage, err}
						} else {
							result, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4")
							done <- outcome{result.usage, err}
						}
					}()
					select {
					case got := <-done:
						require.Error(t, got.err)
						if terminal {
							require.Equal(t, 9, got.usage.InputTokens)
							require.Equal(t, 2, got.usage.OutputTokens)
						}
					case <-time.After(time.Second):
						t.Fatal("authoritative failed did not end the stream")
					}
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
					require.Contains(t, rec.Body.String(), `"code":"context_length_exceeded"`)
					require.NotContains(t, rec.Body.String(), `"type":"error"`)
				})
			}
		}
	}
}
