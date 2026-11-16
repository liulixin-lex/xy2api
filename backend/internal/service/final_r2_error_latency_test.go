//go:build unit

package service

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFinalR3ChatFallbackErrorBeforeUsageDrain(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, target := range []string{"responses", "messages"} {
		for _, prefix := range []string{"content", "heartbeat"} {
			t.Run(target+"/"+prefix, func(t *testing.T) {
				reader, writer := io.Pipe()
				releaseUsage := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseUsage) }) }
				upstreamDone := make(chan struct{})
				go func() {
					defer close(upstreamDone)
					defer func() { _ = writer.Close() }()
					if prefix == "content" {
						_, _ = io.WriteString(writer, "data: "+`{"id":"chat_r3","choices":[{"index":0,"delta":{"content":"hello"}}]}`+"\n\n")
					}
					_, _ = io.WriteString(writer, "data: "+`{"id":"chat_r3","choices":[{"index":0,"delta":{"content":123}}]}`+"\n\n")
					<-releaseUsage
					_, _ = io.WriteString(writer, "data: "+`{"id":"chat_r3","choices":[{"index":0,"delta":{"content":"after-error"}}]}`+"\n\n")
					_, _ = io.WriteString(writer, "data: "+`{"id":"chat_r3","choices":[],"usage":{"prompt_tokens":13,"completion_tokens":5,"total_tokens":18}}`+"\n\ndata: [DONE]\n\n")
				}()
				type outcome struct {
					result  *OpenAIForwardResult
					err     error
					claimed bool
				}
				done := make(chan outcome, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, _ := gin.CreateTestContext(w)
					c.Request = r
					if prefix == "heartbeat" {
						c.Header("Content-Type", "text/event-stream")
						_, _ = c.Writer.WriteString(": heartbeat\n\n")
						c.Writer.Flush()
					}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
					account := rawChatCompletionsTestAccount()
					var result *OpenAIForwardResult
					var err error
					if target == "responses" {
						result, err = svc.streamChatCompletionsAsResponses(c, resp, account, "fixture", nil, nil, false, nil, "fixture", "fixture", nil, nil, time.Now())
					} else {
						result, err = svc.streamChatCompletionsAsAnthropic(c, resp, account, "fixture", "fixture", "fixture", nil, nil, time.Now())
					}
					done <- outcome{result: result, err: err, claimed: ProtocolTerminalClaimed(c)}
				}))
				defer func() {
					release()
					_ = reader.Close()
					_ = writer.Close()
					server.Close()
					<-upstreamDone
				}()
				client := server.Client()
				client.Timeout = time.Second
				start := time.Now()
				resp, err := client.Post(server.URL+"/v1/"+target, "application/json", strings.NewReader(`{}`))
				require.NoError(t, err)
				defer func() { _ = resp.Body.Close() }()
				require.Equal(t, http.StatusOK, resp.StatusCode)
				stream := bufio.NewReader(resp.Body)
				var body strings.Builder
				for !strings.Contains(body.String(), `"error":`) {
					line, readErr := stream.ReadString('\n')
					body.WriteString(line)
					require.NoError(t, readErr, "error must arrive before the upstream usage is released")
				}
				errorAt := time.Since(start)
				require.Less(t, errorAt, 200*time.Millisecond)
				select {
				case <-done:
					t.Fatal("the upstream drain must still be waiting for usage")
				default:
				}
				release()
				tail, err := io.ReadAll(stream)
				require.NoError(t, err)
				body.Write(tail)
				got := <-done
				require.Error(t, got.err)
				require.True(t, got.claimed)
				require.NotNil(t, got.result)
				require.True(t, got.result.PartialFailure)
				require.True(t, got.result.UsagePresent)
				require.Equal(t, 13, got.result.Usage.InputTokens)
				require.Equal(t, 5, got.result.Usage.OutputTokens)
				require.Equal(t, 1, strings.Count(body.String(), `"error":`))
				require.NotContains(t, body.String(), "after-error")
				require.NotContains(t, body.String(), `"type":"response.completed"`)
				require.NotContains(t, body.String(), "event: message_stop")
				t.Logf("error_bytes_at=%s errors=1 usage=13/5 terminal_claimed=true", errorAt)
			})
		}
	}
}

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
