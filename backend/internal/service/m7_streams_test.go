//go:build unit

package service

import (
	"bufio"
	"context"
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
