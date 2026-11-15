//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestM7AnthropicEndpointURLs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	svc := &GatewayService{cfg: cfg}
	native := &OpenAIGatewayService{cfg: cfg}
	body := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello"}],"max_tokens":10}`)
	builders := []struct {
		name, endpoint string
		beta           bool
		build          func(*gin.Context, *Account) (*url.URL, error)
	}{
		{"messages", "/messages", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, _, err := svc.buildUpstreamRequest(context.Background(), c, a, body, "fixture", "apikey", "claude-sonnet-5-5", false, false)
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"passthrough", "/messages", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, a, body, "fixture")
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"count", "/messages/count_tokens", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, _, err := svc.buildCountTokensRequest(context.Background(), c, a, body, "fixture", "apikey", "claude-sonnet-5-5", false)
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"count_passthrough", "/messages/count_tokens", true, func(c *gin.Context, a *Account) (*url.URL, error) {
			r, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(context.Background(), c, a, body, "fixture")
			if err != nil {
				return nil, err
			}
			return r.URL, nil
		}},
		{"native", "/messages", false, func(_ *gin.Context, a *Account) (*url.URL, error) {
			a.Platform = PlatformZhipu
			raw, err := native.nativeAnthropicTargetURL(a)
			if err != nil {
				return nil, err
			}
			return url.Parse(raw)
		}},
		{"models", "/models", false, func(_ *gin.Context, a *Account) (*url.URL, error) {
			return url.Parse(buildV1ModelsURL(a.GetBaseURL()))
		}},
	}
	for _, b := range builders {
		t.Run(b.name, func(t *testing.T) {
			for _, base := range []struct{ suffix, prefix string }{
				{"", "/v1"}, {"/", "/v1"}, {"/v1", "/v1"}, {"/v1/", "/v1"},
				{"/api/v2beta", "/api/v2beta"}, {"/v1" + b.endpoint, "/v1"}, {"/v1" + b.endpoint + "/", "/v1"},
			} {
				for _, query := range []string{"", "?region=fixture&beta=false&beta=false#fragment"} {
					t.Run(base.suffix+query, func(t *testing.T) {
						a := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com" + base.suffix + query, "api_protocol": "anthropic"}}
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
						u, err := b.build(c, a)
						require.NoError(t, err)
						require.Equal(t, base.prefix+b.endpoint, u.Path)
						require.Empty(t, u.Fragment)
						if query != "" {
							require.Equal(t, "fixture", u.Query().Get("region"))
						}
						if b.beta {
							require.Equal(t, []string{"true"}, u.Query()["beta"])
						} else if query == "" {
							require.Empty(t, u.RawQuery)
						}
					})
				}
			}
			if b.name == "models" {
				return
			}
			cfg.Security.URLAllowlist.Enabled = true
			cfg.Security.URLAllowlist.UpstreamHosts = []string{"allowed.example"}
			defer func() { cfg.Security.URLAllowlist.Enabled = false }()
			a := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://blocked.invalid/v1", "api_protocol": "anthropic"}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			_, err := b.build(c, a)
			require.Error(t, err)
		})
	}
}

func TestM7AnthropicPingDoesNotExtendProgress(t *testing.T) {
	ping := ": transport alive\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"
	zeroUsage := "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":0}}\n\n"
	t.Run("pump_ping", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pr.Close()
		pump := newAnthropicNativeLinePump(bufio.NewScanner(pr), 80*time.Millisecond)
		defer pump.stop()
		go func() {
			defer pw.Close()
			for i := 0; i < 30; i++ {
				if _, err := io.WriteString(pw, ping); err != nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		for {
			_, err := pump.next()
			if err != nil {
				require.ErrorIs(t, err, errAnthropicNativeStreamIdle)
				break
			}
		}
	})
	t.Run("pump_usage_progress", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pr.Close()
		pump := newAnthropicNativeLinePump(bufio.NewScanner(pr), 180*time.Millisecond)
		defer pump.stop()
		go func() {
			defer pw.Close()
			for i := 0; i < 5; i++ {
				if _, err := io.WriteString(pw, ping+zeroUsage); err != nil {
					return
				}
				time.Sleep(60 * time.Millisecond)
			}
		}()
		var received strings.Builder
		for {
			line, err := pump.next()
			if err != nil {
				require.ErrorIs(t, err, io.EOF)
				break
			}
			received.WriteString(line + "\n")
		}
		require.Equal(t, strings.Repeat(ping+zeroUsage, 5), received.String())
	})
	t.Run("queued_lines_do_not_refresh_deadline", func(t *testing.T) {
		pump := &anthropicNativeLinePump{events: make(chan anthropicNativeLineEvent, 1), done: make(chan struct{}), interval: time.Millisecond}
		pump.progress.start()
		pump.progress.lastProgressAt.Store(time.Now().Add(-time.Second).UnixNano())
		pump.events <- anthropicNativeLineEvent{line: "event: ping"}
		defer pump.stop()
		_, err := pump.next()
		require.ErrorIs(t, err, errAnthropicNativeStreamIdle)
		require.Len(t, pump.events, 1)
	})
	t.Run("first_output_guard_owns_deadline", func(t *testing.T) {
		pump := &anthropicNativeLinePump{events: make(chan anthropicNativeLineEvent, 1), done: make(chan struct{}), interval: time.Millisecond, firstEvent: &openAICompatFirstEvent{}}
		pump.progress.start()
		pump.progress.lastProgressAt.Store(time.Now().Add(-time.Second).UnixNano())
		pump.events <- anthropicNativeLineEvent{line: "event: ping"}
		defer pump.stop()
		line, err := pump.next()
		require.NoError(t, err)
		require.Equal(t, "event: ping", line)
	})
	for _, upstreamPing := range []bool{false, true} {
		name := "native_downstream_heartbeat"
		if upstreamPing {
			name = "native_upstream_ping"
		}
		t.Run(name, func(t *testing.T) {
			svc := newNativeAnthropicHangTestService(1)
			svc.cfg.Gateway.StreamKeepaliveInterval = 1
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			pr, pw := io.Pipe()
			defer pr.Close()
			defer pw.Close()
			go func() {
				_, _ = io.WriteString(pw, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n")
				for i := 0; i < 16; i++ {
					if upstreamPing {
						if _, err := io.WriteString(pw, ping); err != nil {
							return
						}
					}
					time.Sleep(100 * time.Millisecond)
				}
				_ = pw.Close()
			}()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
			res, err := svc.handleNativeAnthropicStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, "fixture", "fixture", "fixture", nil, time.Now())
			require.ErrorContains(t, err, "stream data interval timeout")
			require.NotNil(t, res)
			require.Equal(t, 4, res.Usage.InputTokens)
			require.Zero(t, res.Usage.OutputTokens)
		})
	}
	t.Run("native_usage_and_content_progress", func(t *testing.T) {
		svc := newNativeAnthropicHangTestService(1)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		pr, pw := io.Pipe()
		defer pr.Close()
		go func() {
			defer pw.Close()
			for i := 0; i < 4; i++ {
				if _, err := io.WriteString(pw, ping+zeroUsage+"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"); err != nil {
					return
				}
				time.Sleep(400 * time.Millisecond)
			}
			_, _ = io.WriteString(pw, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}()
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
		res, err := svc.handleNativeAnthropicStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, "fixture", "fixture", "fixture", nil, time.Now())
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Equal(t, 4, strings.Count(rec.Body.String(), "hello"))
		require.Contains(t, rec.Body.String(), `"output_tokens":0`)
	})
}

func TestM7RawUsageIsNotTerminal(t *testing.T) {
	withResilienceSettings(t, nil)
	content := `{"choices":[{"delta":{"content":"hello"}}]}`
	meter := `{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":6,"total_tokens":17}}`
	zero := `{"choices":[],"usage":{"input_tokens":0,"output_tokens":0}}`
	partial := `{"choices":[],"usage":{"prompt_tokens":11}}`
	finish := `{"choices":[{"delta":{},"finish_reason":"stop"}]}`
	for _, tc := range []struct {
		name                       string
		payloads                   []string
		readError                  bool
		wantError, terminal, usage bool
	}{
		{"complete_tail", []string{content, meter}, false, false, false, true},
		{"zero_tail", []string{content, zero}, false, false, false, true},
		{"meter_only", []string{meter}, false, false, false, true},
		{"partial_empty_answer", []string{partial}, false, false, false, true},
		{"role_only", []string{`{"choices":[{"delta":{"role":"assistant"}}]}`}, false, false, false, false},
		{"partial_tail", []string{content, partial}, false, true, false, true},
		{"empty_tail", []string{content, `{"choices":[],"usage":{}}`}, false, true, false, false},
		{"early_meter", []string{meter, content}, false, true, false, true},
		{"content_after_tail", []string{content, meter, content}, false, true, false, true},
		{"broken_tail", []string{content, meter, `{"choices":`}, false, true, false, true},
		{"read_error_after_meter", []string{content, meter}, true, true, false, true},
		{"finish_only", []string{content, finish}, false, false, true, false},
		{"done_only", []string{content, "[DONE]"}, false, false, true, false},
		{"read_error_after_finish", []string{content, finish}, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var terminal openAIRawStreamTerminalState
			for _, payload := range tc.payloads {
				terminal.ObserveDataLine(payload)
			}
			require.Equal(t, tc.terminal, terminal.Terminated())
			if !tc.readError {
				require.Equal(t, tc.wantError, terminal.IsTruncated(true))
			}
			var body strings.Builder
			for _, payload := range tc.payloads {
				body.WriteString("data: " + payload + "\n\n")
			}
			wire := strings.TrimSuffix(body.String(), "\n")
			reader := io.NopCloser(strings.NewReader(wire))
			if tc.readError {
				reader = &openAIChatStreamReadErrorCloser{payload: []byte(wire), err: errors.New("fixture read failure")}
			}
			c, _ := compatStreamContext("/v1/chat/completions", "")
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			result, err := svc.streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			require.True(t, result.ResponseFactsKnown)
			require.Equal(t, tc.usage, result.UsagePresent)
			require.Equal(t, tc.terminal, result.TerminalSeen)
			require.Equal(t, tc.terminal, result.TerminalValid)
			require.Equal(t, tc.wantError, result.PartialFailure)
			require.Equal(t, "sse", result.ResponseBodyKind)
			if tc.name == "complete_tail" || tc.name == "read_error_after_meter" {
				require.Equal(t, 11, result.Usage.InputTokens)
				require.Equal(t, 6, result.Usage.OutputTokens)
				require.Equal(t, "upstream_sse_progressive", result.UsageSource)
			}
		})
	}
}

func TestM7RawPreludeClassifiesWholeError(t *testing.T) {
	withResilienceSettings(t, nil)
	role := "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	content := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	failure := "event: error\ndata: {\"error\":\ndata: {\"type\":\"server_error\",\"code\":\"server_is_overloaded\",\"message\":\"busy\"}}\n\n"
	late := "data: {\"choices\":[{\"delta\":{\"content\":\"should_not_reach\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	for _, requestBytes := range []int{1, 128 << 10} {
		for _, afterContent := range []bool{false, true} {
			t.Run(fmt.Sprintf("request_%d_content_%t", requestBytes, afterContent), func(t *testing.T) {
				wire := role
				if afterContent {
					wire += content
				}
				wire += failure + late
				c, rec := compatStreamContext("/v1/chat/completions", "")
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				result, err := svc.streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), requestBytes)
				require.Error(t, err)
				var failover *UpstreamFailoverError
				if !afterContent {
					require.ErrorAs(t, err, &failover)
					require.Nil(t, result)
					require.Empty(t, rec.Body.String())
				} else {
					require.False(t, errors.As(err, &failover))
					require.NotNil(t, result)
					require.True(t, result.PartialFailure)
					require.Contains(t, rec.Body.String(), "hello")
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"error"`))
					require.Equal(t, 1, strings.Count(rec.Body.String(), "[DONE]"))
					require.NotContains(t, rec.Body.String(), "should_not_reach")
					require.True(t, ProtocolTerminalClaimed(c))
					before := rec.Body.String()
					require.False(t, WriteTargetProtocolError(c, PreOutputProtocolChat, 502, "server_error", "", "", "again"))
					require.Equal(t, before, rec.Body.String())
				}
			})
		}
	}
	for _, tc := range []struct {
		name, wire string
		wantError  bool
	}{
		{"normal_order", role + content + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false},
		{"event_only", role + "event: error\n\n", true},
		{"prelude_limit", strings.Repeat(role, openAIFirstOutputStageMaxBytes/len(role)+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := compatStreamContext("/v1/chat/completions", "")
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.wire))}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			_, err := svc.streamRawChatCompletions(c, resp, rawChatCompletionsTestAccount(), "fixture", "fixture", "fixture", nil, nil, time.Now(), 1)
			if tc.wantError {
				require.Error(t, err)
				require.Empty(t, rec.Body.String())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wire, rec.Body.String())
			}
		})
	}
}
