//go:build unit

package service

import (
	"errors"
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
		})
	}
}
