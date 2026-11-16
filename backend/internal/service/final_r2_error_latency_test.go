//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFinalR2PreOutputConversionErrorReturnsBeforeTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"chat", "messages"} {
		t.Run(target, func(t *testing.T) {
			reader, writer := io.Pipe()
			releaseTerminal := make(chan struct{})
			upstreamDone := make(chan struct{})
			go func() {
				defer close(upstreamDone)
				defer func() { _ = writer.Close() }()
				_, _ = io.WriteString(writer, `data: {"type":"response.created","response":{"id":"resp_r2","output":[]}}`+"\n\n")
				_, _ = io.WriteString(writer, `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":123}`+"\n\n")
				<-releaseTerminal
				_, _ = io.WriteString(writer, `data: {"type":"response.completed","response":{"id":"resp_r2","status":"completed","output":[],"usage":{"input_tokens":13,"output_tokens":5}}}`+"\n\n")
			}()
			type outcome struct {
				result *OpenAIForwardResult
				err    error
			}
			done := make(chan outcome, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, _ := gin.CreateTestContext(w)
				c.Request = r
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
				var result *OpenAIForwardResult
				var err error
				if target == "chat" {
					result, err = svc.handleChatStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", time.Now(), 1)
				} else {
					result, err = svc.handleAnthropicStreamingResponse(resp, c, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", time.Now())
				}
				done <- outcome{result: result, err: err}
			}))
			defer func() {
				close(releaseTerminal)
				_ = reader.Close()
				_ = writer.Close()
				server.Close()
				<-upstreamDone
			}()
			client := server.Client()
			client.Timeout = time.Second
			start := time.Now()
			response, err := client.Post(server.URL+"/v1/"+target, "application/json", strings.NewReader(`{}`))
			require.NoError(t, err, "the complete 502 must arrive while the upstream terminal is still blocked")
			defer func() { _ = response.Body.Close() }()
			headersAt := time.Since(start)
			body, err := io.ReadAll(response.Body)
			bodyEndAt := time.Since(start)
			require.NoError(t, err)
			require.Equal(t, http.StatusBadGateway, response.StatusCode)
			require.Contains(t, string(body), `"error":`)
			require.Less(t, headersAt, 200*time.Millisecond)
			require.Less(t, bodyEndAt, 200*time.Millisecond)
			got := <-done
			require.Error(t, got.err)
			require.Nil(t, got.result, "no observed usage must not create a billable result")
			_, err = writer.Write([]byte("data: {}\n\n"))
			require.ErrorIs(t, err, io.ErrClosedPipe, "the upstream must already be closed")
			t.Logf("status=%d headers_at=%s body_end_at=%s result=nil upstream=closed", response.StatusCode, headersAt, bodyEndAt)
		})
	}
}
