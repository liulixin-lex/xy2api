//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNativePreludeQueuedRetriesWithoutLeakingAttempt(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			for _, heartbeat := range []bool{false, true} {
				t.Run(fmtBool(heartbeat), func(t *testing.T) {
					c, rec := compatStreamContext("/v1/responses", `{}`)
					if heartbeat {
						c.Header("Content-Type", "text/event-stream")
						n, err := c.Writer.Write([]byte(": ping\n\n"))
						require.NoError(t, err)
						recordOpenAIStreamKeepaliveBytes(c, n)
						c.Writer.Flush()
					}
					svc := compatStreamService(nil, 0, 0, 0)
					account := rawChatCompletionsTestAccount()
					body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_a\"}}\n\n" +
						"data: {\"type\":\"response.queued\"}\n\n" +
						"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_a\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}}\n\n"
					resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
					var err error
					if passthrough {
						_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					} else {
						_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					}
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.NotContains(t, rec.Body.String(), "resp_a")
					require.NotContains(t, rec.Body.String(), "response.queued")
				})
			}
		})
	}
}

func fmtBool(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func TestNativePreludeAttemptBoundary(t *testing.T) {
	withResilienceSettings(t, nil)
	createdA := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_a\"}}\n\n"
	failedA := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_a\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"},\"usage\":{\"input_tokens\":999}}}\n\n"
	createdB := strings.ReplaceAll(createdA, "resp_a", "resp_b")
	completedB := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_b\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n"
	cases := []struct {
		name, before, after string
		retry               bool
	}{
		{"created", "", "", true},
		{"queued", "", "data: {\"type\":\"response.queued\"}\n\n", true},
		{"unmarked_empty", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n", "", true},
		{"unmarked_whitespace", "data: {\"type\":\"response.output_text.delta\",\"delta\":\" \\t\"}\n\n", "", true},
		{"marked", relayKeepaliveFrame, "", true},
		{"nonempty_before_created", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"real\"}\n\n", "", false},
		{"unknown_codex", "", "data: {\"type\":\"codex.response.metadata\"}\n\n", false},
		{"response_metadata", "", "event: response.metadata\ndata: {\"type\":\"response.metadata\",\"sequence_number\":1,\"metadata\":{\"openai_chatgpt_moderation_metadata\":{}}}\n\n", true},
		{"unknown_added_payload", "", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"content\":[],\"opaque\":\"payload\"}}\n\n", false},
	}
	for _, passthrough := range []bool{false, true} {
		for _, heartbeat := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmtBool(passthrough)+"/"+fmtBool(heartbeat)+"/"+tc.name, func(t *testing.T) {
					c, rec := compatStreamContext("/v1/responses", `{}`)
					if heartbeat {
						c.Header("Content-Type", "text/event-stream")
						n, err := c.Writer.Write([]byte(": ping\n\n"))
						require.NoError(t, err)
						recordOpenAIStreamKeepaliveBytes(c, n)
						c.Writer.Flush()
					}
					svc := compatStreamService(nil, 0, 0, 0)
					run := func(body string) (int, error) {
						resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
						if passthrough {
							r, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "model", "model")
							if r == nil {
								return 0, err
							}
							return r.usage.InputTokens, err
						}
						r, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "model", "model")
						if r == nil {
							return 0, err
						}
						return r.usage.InputTokens, err
					}
					_, err := run(tc.before + createdA + tc.after + failedA)
					var failover *UpstreamFailoverError
					if !tc.retry {
						require.Error(t, err)
						require.False(t, errors.As(err, &failover))
						require.NotEmpty(t, rec.Body.String())
						return
					}
					require.ErrorAs(t, err, &failover)
					require.NotContains(t, rec.Body.String(), "resp_a")
					tokens, err := run(createdB + completedB)
					require.NoError(t, err)
					require.Equal(t, 7, tokens, "failed attempt usage must not enter the successful result")
					var events []gjson.Result
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if strings.HasPrefix(line, "data: ") {
							events = append(events, gjson.Parse(strings.TrimPrefix(line, "data: ")))
						}
					}
					require.Len(t, events, 2)
					require.Equal(t, "response.created", events[0].Get("type").String())
					for i, event := range events {
						require.Equal(t, "resp_b", event.Get("response.id").String())
						require.EqualValues(t, i, event.Get("sequence_number").Int())
					}
				})
			}
		}
	}
}

func TestNativePreludeSwitchOff(t *testing.T) {
	for _, master := range []bool{true, false} {
		t.Run(fmtBool(master), func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = master; s.Stream.NativePreludeWhitelist = false })
			require.True(t, openAIStreamDataStartsClientOutput(`{"type":"response.queued"}`, "response.queued"))
			require.False(t, openAIStreamDataStartsClientOutput(`{"type":"response.created"}`, "response.created"))
			require.True(t, openAIStreamDataStartsClientOutput(`{"type":"response.metadata","metadata":{}}`, "response.metadata"))
			require.False(t, sseBlockIsOpening([]byte("event: response.metadata\ndata: {\"type\":\"response.metadata\",\"metadata\":{}}")))
		})
	}
}

// 生产 2026-10-22：Codex 后端过载时先回 200，依次发 created、in_progress、
// response.metadata，再在 seq 3 发裸 error（server_is_overloaded），没有
// response.failed。白名单开启时 response.metadata 是开场，应在输出前换号；
// 关闭时回到旧边界；codex.response.metadata 始终算输出。
func nativeMetadataOverloadBody(metadataType string) string {
	return "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_a\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.in_progress\n" +
		"data: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"resp_a\",\"status\":\"in_progress\"}}\n\n" +
		"event: " + metadataType + "\n" +
		"data: {\"type\":\"" + metadataType + "\",\"sequence_number\":2,\"metadata\":{\"openai_chatgpt_moderation_metadata\":{\"flagged\":false}}}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\",\"param\":null},\"sequence_number\":3}\n\n"
}

func TestNativePreludeResponseMetadataBeforeOverloadedError(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		whitelist      bool
		retry          bool
	}{
		{"response_metadata/whitelist_on", "response.metadata", true, true},
		{"response_metadata/whitelist_off", "response.metadata", false, false},
		{"codex_metadata/whitelist_on", "codex.response.metadata", true, false},
	} {
		for _, passthrough := range []bool{false, true} {
			t.Run(tc.name+"/passthrough_"+fmtBool(passthrough), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.NativePreludeWhitelist = tc.whitelist })
				logSink, restore := captureStructuredLog(t)
				defer restore()
				c, rec := compatStreamContext("/v1/responses", `{}`)
				svc := compatStreamService(nil, 0, 0, 0)
				resp := &http.Response{StatusCode: http.StatusOK,
					Header: http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:   io.NopCloser(strings.NewReader(nativeMetadataOverloadBody(tc.metadata)))}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.5", "gpt-5.5")
				} else {
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.5", "gpt-5.5")
				}
				var failover *UpstreamFailoverError
				if tc.retry {
					require.ErrorAs(t, err, &failover)
					require.True(t, failover.RetryableOnSameAccount)
					require.True(t, failover.RequestScopedTransient)
					require.True(t, failover.IsOpenAICapacityShed())
					require.Equal(t, http.StatusServiceUnavailable, failover.ClientStatusCode)
					require.False(t, c.Writer.Written())
					require.Empty(t, rec.Body.String())
					require.False(t, logSink.ContainsMessage("gateway.failover_suppressed_after_semantic_output"))
					return
				}
				require.Error(t, err)
				require.False(t, errors.As(err, &failover))
				require.Contains(t, rec.Body.String(), tc.metadata)
				require.True(t, logSink.ContainsMessage("gateway.failover_suppressed_after_semantic_output"))
			})
		}
	}
}

const nativeMetadataFrame = "event: response.metadata\ndata: {\"type\":\"response.metadata\",\"sequence_number\":2,\"metadata\":{\"openai_chatgpt_moderation_metadata\":{}}}\n\n"

func runNativeResponsesStream(svc *OpenAIGatewayService, c *gin.Context, resp *http.Response, passthrough bool) chan error {
	done := make(chan error, 1)
	go func() {
		var err error
		if passthrough {
			_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
		} else {
			_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, rawChatCompletionsTestAccount(), time.Now(), "gpt-5.4", "gpt-5.4")
		}
		done <- err
	}()
	return done
}

func awaitNativeResponsesStream(t *testing.T, done chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("stream did not return within %s", within)
		return nil
	}
}

// With the whitelist on, response.metadata is not first output: an attempt
// that sends nothing but opening events after it still hits the first-output
// deadline. With the switch off, metadata disarms the deadline as before and
// late content completes normally.
func TestNativePreludeResponseMetadataKeepsFirstOutputDeadline(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, whitelist := range []bool{true, false} {
			t.Run(fmtBool(passthrough)+"/whitelist_"+fmtBool(whitelist), func(t *testing.T) {
				withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.NativePreludeWhitelist = whitelist })
				w := newGWWriter()
				c := ptContext(w)
				up := newGWUpstream(nil)
				done := runNativeResponsesStream(compatStreamService(nil, 1, 0, 0), c, up.response(nil), passthrough)
				up.send(t, ptCreated, ptProgress, nativeMetadataFrame)
				if whitelist {
					requireFirstOutputFailover(t, awaitNativeResponsesStream(t, done, 4*time.Second))
					requireOnlyHeartbeats(t, c, w.Body())
					return
				}
				time.Sleep(1500 * time.Millisecond)
				up.send(t, ptDelta, ptCompleted)
				_ = up.pw.Close()
				require.NoError(t, awaitNativeResponsesStream(t, done, 4*time.Second))
				require.Contains(t, w.Body(), "response.metadata")
			})
		}
	}
}

// On a successful attempt the staged response.metadata is released with the
// first content, in upstream order and with its sequence number unchanged.
func TestNativePreludeResponseMetadataReleasedInOrder(t *testing.T) {
	withResilienceSettings(t, nil)
	delta := strings.Replace(ptDelta, `"sequence_number":2`, `"sequence_number":3`, 1)
	completed := strings.Replace(ptCompleted, `"sequence_number":3`, `"sequence_number":4`, 1)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmtBool(passthrough), func(t *testing.T) {
			c, rec := compatStreamContext("/v1/responses", `{}`)
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(ptCreated + ptProgress + nativeMetadataFrame + delta + completed))}
			require.NoError(t, awaitNativeResponsesStream(t, runNativeResponsesStream(compatStreamService(nil, 0, 0, 0), c, resp, passthrough), 4*time.Second))
			var types []string
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				event := gjson.Parse(strings.TrimPrefix(line, "data: "))
				types = append(types, event.Get("type").String())
				if event.Get("type").String() == "response.metadata" {
					require.EqualValues(t, 2, event.Get("sequence_number").Int())
				}
			}
			require.Equal(t, []string{"response.created", "response.in_progress", "response.metadata", "response.output_text.delta", "response.completed"}, types)
		})
	}
}

// The WebSocket-over-HTTP bridge shares the boundary: with the whitelist on
// response.metadata is held and the attempt fails over; with it off the
// metadata is written and the failure is forwarded.
func TestNativePreludeResponseMetadataWSBridgeFollowsWhitelist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, whitelist := range []bool{true, false} {
		t.Run(fmtBool(whitelist), func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Stream.NativePreludeWhitelist = whitelist })
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_boundary\"}}\n\n" +
				"data: {\"type\":\"response.metadata\",\"metadata\":{\"openai_chatgpt_moderation_metadata\":{}}}\n\n" +
				"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
			c, _ := compatStreamContext("/v1/responses", `{}`)
			payload := []byte(`{"type":"response.create","model":"gpt-5.4","input":"hi"}`)
			var writes [][]byte
			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.4", "", "", "", "", 1, func(data []byte) error {
				writes = append(writes, append([]byte(nil), data...))
				return nil
			})
			var failover *UpstreamFailoverError
			if whitelist {
				require.ErrorAs(t, err, &failover)
				require.Empty(t, writes)
				return
			}
			require.False(t, errors.As(err, &failover))
			require.GreaterOrEqual(t, len(writes), 2)
			require.Equal(t, "response.metadata", gjson.GetBytes(writes[1], "type").String())
		})
	}
}
