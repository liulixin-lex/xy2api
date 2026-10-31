package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const ttlWireResponse = `{"id":"msg_ttl","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":5,"cache_creation_input_tokens":100,"cache_read_input_tokens":40,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}}}`

func ttlWireSSE() string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + ttlWireResponse + "}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func ttlWireUpstream(stream bool) *anthropicHTTPUpstreamRecorder {
	body, contentType := ttlWireResponse, "application/json"
	if stream {
		body, contentType = ttlWireSSE(), "text/event-stream"
	}
	return &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}}
}

// Real Forward entrypoints, captured HTTP bodies, both client response modes.
func TestAnthropicCacheTTLForwardWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"messages", "passthrough", "chat", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, mode := range []string{"off", "5m", "1h"} {
				t.Run(fmt.Sprintf("%s/%t/%s", route, stream, mode), func(t *testing.T) {
					svc, key, a := cacheFixture(t)
					key.AnthropicCacheTTLMode = mode
					cc := `{"type":"ephemeral","ttl":"5m","scope":"global","future_flag":true}`
					schema := `{"type":"object","properties":{"cache_control":{"const":{"type":"ephemeral","ttl":"5m"}}}}`
					var raw string
					switch route {
					case "chat":
						raw = fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%t,"cache_control":%s,"tools":[{"type":"function","function":{"name":"probe","parameters":%s,"cache_control":%s}}],"messages":[{"role":"system","content":[{"type":"text","text":"stable","cache_control":%s}]},{"role":"user","content":[{"type":"text","text":"hello","cache_control":%s}]}]}`, stream, cc, schema, cc, cc, cc)
					case "responses":
						raw = fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%t,"cache_control":%s,"tools":[{"type":"function","name":"probe","parameters":%s,"cache_control":%s}],"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"stable","cache_control":%s}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello","cache_control":%s}]}]}`, stream, cc, schema, cc, cc, cc)
					default:
						raw = fmt.Sprintf(`{"model":"claude-sonnet-4-5","stream":%t,"max_tokens":8,"cache_control":%s,"tools":[{"name":"probe","input_schema":%s,"cache_control":%s}],"system":[{"type":"text","text":"stable","cache_control":%s}],"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":%s}]}]}`, stream, cc, schema, cc, cc, cc)
					}
					if route == "passthrough" {
						a.Extra = map[string]any{"anthropic_passthrough": true, "cache_ttl_override_enabled": true, "cache_ttl_override_target": "1h"}
					}
					upstream := ttlWireUpstream(stream || route == "chat" || route == "responses")
					svc.httpUpstream = upstream
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest("POST", "/v1/"+route, nil)
					ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(raw))
					c.Request = c.Request.WithContext(ctx)
					parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(raw)), PlatformAnthropic)
					require.NoError(t, err)
					parsed.GroupID = key.GroupID
					var result *ForwardResult
					switch route {
					case "chat":
						result, err = svc.ForwardAsChatCompletions(ctx, c, a, []byte(raw), parsed)
					case "responses":
						result, err = svc.ForwardAsResponses(ctx, c, a, []byte(raw), parsed)
					default:
						result, err = svc.Forward(ctx, c, a, parsed)
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					paths := anthropicCacheControlPaths(upstream.lastBody)
					require.Len(t, paths, 4, string(upstream.lastBody))
					wantTTL := mode
					if mode == "off" {
						wantTTL = "5m"
					}
					for _, path := range paths {
						require.Equal(t, wantTTL, gjson.GetBytes(upstream.lastBody, path+".ttl").String(), path)
						require.True(t, gjson.GetBytes(upstream.lastBody, path+".future_flag").Bool(), path)
					}
					require.JSONEq(t, schema, gjson.GetBytes(upstream.lastBody, "tools.0.input_schema").Raw)
					require.NotNil(t, result.AnthropicCacheTTLPolicy)
					require.Equal(t, mode, *result.AnthropicCacheTTLPolicy)
					require.Equal(t, mode != "off", *result.AnthropicCacheTTLApplied)
					require.Equal(t, 100, result.Usage.CacheCreationInputTokens)
					require.True(t, result.Usage.CacheCreationInputTokensPresent)
					require.Equal(t, 30, result.Usage.CacheCreation5mTokens)
					require.Equal(t, 20, result.Usage.CacheCreation1hTokens)
					require.True(t, result.Usage.CacheCreation5mTokensPresent)
					require.True(t, result.Usage.CacheCreation1hTokensPresent)
					require.JSONEq(t, raw, string(parsed.Body.Bytes()), "attempt cannot change the admitted body")
					if route == "messages" || route == "passthrough" {
						require.Contains(t, rec.Body.String(), `"ephemeral_5m_input_tokens":30`)
						require.Contains(t, rec.Body.String(), `"ephemeral_1h_input_tokens":20`)
					}
				})
			}
		}
	}
}

func TestAnthropicCacheTTLNoMarkersAndActualModelWire(t *testing.T) {
	for _, target := range []string{"claude-sonnet-4-5", "kimi-k3", "deepseek-v4", "glm-5", "unverified"} {
		t.Run(target, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			key.AnthropicCacheTTLMode = "1h"
			svc.settingService.claudeCacheSnapshot.Store(&cachedClaudeCachePolicy{policy: emptyClaudeCachePolicy(), expiresAt: time.Now().Add(time.Minute)})
			a.Credentials["model_mapping"] = map[string]any{"alias": target}
			raw := []byte(`{"model":"alias","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
			svc.httpUpstream = ttlWireUpstream(false)
			up, ok := svc.httpUpstream.(*anthropicHTTPUpstreamRecorder)
			require.True(t, ok)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, raw)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(raw), PlatformAnthropic)
			require.NoError(t, err)
			parsed.GroupID = key.GroupID
			result, err := svc.Forward(ctx, c, a, parsed)
			require.NoError(t, err)
			require.False(t, HasClaudeCacheControl(up.lastBody))
			if target == "claude-sonnet-4-5" {
				require.Equal(t, "1h", *result.AnthropicCacheTTLPolicy)
				require.False(t, *result.AnthropicCacheTTLApplied)
			} else {
				require.Nil(t, result.AnthropicCacheTTLPolicy)
				require.Nil(t, result.AnthropicCacheTTLApplied)
			}
		})
	}
}

func TestAnthropicCacheTTLAttemptSnapshotWire(t *testing.T) {
	svc, key, a := cacheFixture(t)
	gatewayForwardingSF.Forget("gateway_forwarding")
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{anthropicCacheTTLDefault: "1h", expiresAt: time.Now().Add(time.Minute).UnixNano()})
	t.Cleanup(func() {
		gatewayForwardingSF.Forget("gateway_forwarding")
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	})
	key.AnthropicCacheTTLMode = "inherit"
	raw := []byte(`{"model":"claude-sonnet-4-5","max_tokens":8,"tools":[{"name":"probe","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, raw)
	key.AnthropicCacheTTLMode = "off"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{anthropicCacheTTLDefault: "5m", expiresAt: time.Now().Add(time.Minute).UnixNano()})
	ctx = svc.WithClaudeCacheFallbackRequest(ctx, key, raw)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(raw), PlatformAnthropic)
	require.NoError(t, err)
	parsed.GroupID = key.GroupID
	// Same account twice, another account, fallback group, then non-Claude mapping.
	for i := 0; i < 5; i++ {
		account := *a
		account.Credentials = map[string]any{"api_key": "fixture"}
		if i == 0 {
			account.Type = AccountTypeSetupToken
			account.Credentials["access_token"] = "fixture"
		}
		if i == 2 {
			account.ID = 12
		}
		if i == 3 {
			gid := int64(8)
			parsed.GroupID = &gid
			account.GroupIDs = []int64{8}
		}
		if i == 4 {
			account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "kimi-k3"}
		}
		svc.httpUpstream = ttlWireUpstream(false)
		up, ok := svc.httpUpstream.(*anthropicHTTPUpstreamRecorder)
		require.True(t, ok)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		c.Request = c.Request.WithContext(ctx)
		result, err := svc.Forward(ctx, c, &account, parsed)
		require.NoError(t, err)
		require.Equal(t, string(raw), string(parsed.Body.Bytes()))
		if i < 3 {
			require.True(t, anthropicCacheTTLApplied(up.lastBody, "1h"))
			require.Equal(t, "1h", *result.AnthropicCacheTTLPolicy)
		} else {
			require.False(t, HasClaudeCacheControl(up.lastBody))
			if i == 4 {
				require.Nil(t, result.AnthropicCacheTTLPolicy)
			}
		}
	}
}

func TestAnthropicCacheTTLCountTokensWireDoesNotInject(t *testing.T) {
	svc, key, a := cacheFixture(t)
	key.AnthropicCacheTTLMode = "1h"
	a.Type = AccountTypeSetupToken
	a.Credentials["access_token"] = "fixture"
	for _, declared := range []bool{false, true} {
		raw := `{"model":"claude-sonnet-4-5","tools":[{"name":"probe","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
		if declared {
			raw = `{"model":"claude-sonnet-4-5","cache_control":{"type":"ephemeral","ttl":"5m"},"messages":[{"role":"user","content":"hi"}]}`
		}
		up := ttlWireUpstream(false)
		up.resp.Body = io.NopCloser(strings.NewReader(`{"input_tokens":42}`))
		svc.httpUpstream = up
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
		ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(raw))
		parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(raw)), PlatformAnthropic)
		require.NoError(t, err)
		require.NoError(t, svc.ForwardCountTokens(ctx, c, a, parsed))
		require.Equal(t, declared, HasClaudeCacheControl(up.lastBody))
		if declared {
			require.Equal(t, "5m", gjson.GetBytes(up.lastBody, "cache_control.ttl").String())
		}
	}
}

func TestAnthropicCacheTTLUsagePresenceWire(t *testing.T) {
	cases := []struct {
		name, start, delta string
		total, five, hour  int
		present            bool
	}{
		{"omitted", `"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}`, `"output_tokens":5`, 100, 30, 20, true},
		{"explicit_zero", `"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}`, `"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0},"output_tokens":5`, 0, 0, 20, true},
		{"derived", `"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}`, `"cache_creation":{"ephemeral_5m_input_tokens":0},"output_tokens":5`, 20, 0, 20, false},
		{"cumulative", `"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}`, `"cache_creation_input_tokens":150,"cache_creation":{"ephemeral_1h_input_tokens":70},"output_tokens":5`, 150, 30, 70, true},
	}
	for _, route := range []string{"messages", "passthrough", "chat", "responses"} {
		for _, tc := range cases {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				svc, key, a := cacheFixture(t)
				key.AnthropicCacheTTLMode = "1h"
				if route == "passthrough" {
					a.Extra = map[string]any{"anthropic_passthrough": true}
				}
				raw := `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`
				if route == "responses" {
					raw = `{"model":"claude-sonnet-4-5","stream":true,"input":"hi"}`
				}
				sse := `event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_presence","model":"claude-sonnet-4-5","role":"assistant","content":[],"usage":{"input_tokens":2,` + tc.start + `}}}` + "\n\n" +
					`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + tc.delta + `}}` + "\n\n" + `event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n"
				up := ttlWireUpstream(true)
				up.resp.Body = io.NopCloser(strings.NewReader(sse))
				svc.httpUpstream = up
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/"+route, nil)
				ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(raw))
				c.Request = c.Request.WithContext(ctx)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(raw)), PlatformAnthropic)
				require.NoError(t, err)
				parsed.GroupID = key.GroupID
				var result *ForwardResult
				switch route {
				case "chat":
					result, err = svc.ForwardAsChatCompletions(ctx, c, a, []byte(raw), parsed)
				case "responses":
					result, err = svc.ForwardAsResponses(ctx, c, a, []byte(raw), parsed)
				default:
					result, err = svc.Forward(ctx, c, a, parsed)
				}
				require.NoError(t, err)
				require.Equal(t, tc.total, result.Usage.CacheCreationInputTokens)
				require.Equal(t, tc.five, result.Usage.CacheCreation5mTokens)
				require.Equal(t, tc.hour, result.Usage.CacheCreation1hTokens)
				require.Equal(t, tc.present, result.Usage.CacheCreationInputTokensPresent)
			})
		}
	}
	// Native nonstream must distinguish absent aggregate from an explicit zero too.
	for _, aggregate := range []string{"", `"cache_creation_input_tokens":0,`} {
		body := []byte(`{"usage":{` + aggregate + `"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}}}`)
		usage := parseClaudeUsageFromResponseBody(body)
		want := 50
		if aggregate != "" {
			want = 0
		}
		require.Equal(t, want, usage.CacheCreationInputTokens)
		require.Equal(t, aggregate != "", usage.CacheCreationInputTokensPresent)
	}
}

func TestAnthropicCacheTTLAutomaticAndDeferredWire(t *testing.T) {
	cases := []struct {
		name, mode, raw string
		markers         int
		conflict        bool
	}{
		{"off_conflict", "off", `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`, 2, true},
		{"force_resolves_conflict", "1h", `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`, 2, false},
		{"reserved_slots", "1h", `{"cache_control":{"type":"ephemeral"},"tools":[{"name":"probe","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},{"type":"text","text":"c","cache_control":{"type":"ephemeral"}}]}]}`, 4, false},
		{"illegal_locations", "1h", `{"tools":[{"name":"deferred","custom":{"defer_loading":true},"input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"x","cache_control":{"type":"ephemeral"}},{"type":"redacted_thinking","data":"private","cache_control":{"type":"ephemeral"}},{"type":"text","text":"hi"}]}]}`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			key.AnthropicCacheTTLMode = tc.mode
			if tc.name == "reserved_slots" {
				a.Type = AccountTypeSetupToken
				a.Credentials["access_token"] = "fixture"
			}
			raw := []byte(tc.raw)
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, raw)
			ctx, _ = svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			body := raw
			if tc.name == "reserved_slots" {
				body = svc.applyClaudeCodeOAuthMimicryToBody(ctx, c, a, body, nil, "claude-sonnet-4-5")
			}
			req, _, err := svc.buildUpstreamRequest(ctx, c, a, body, "fixture", "apikey", "claude-sonnet-4-5", false, false)
			require.NoError(t, err)
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, req.Body.Close())
			require.Len(t, anthropicCacheControlPaths(wire), tc.markers, string(wire))
			require.Equal(t, tc.conflict, anthropicAutomaticTailConflict(wire))
			if tc.name == "illegal_locations" {
				require.False(t, gjson.GetBytes(wire, "tools.0.cache_control").Exists())
				require.False(t, gjson.GetBytes(wire, "messages.0.content.0.cache_control").Exists())
				require.False(t, gjson.GetBytes(wire, "messages.0.content.1.cache_control").Exists())
			}
		})
	}
}

func TestAnthropicCacheTTLOfficialFamiliesWire(t *testing.T) {
	for _, model := range []string{"claude-fable-5-1", "claude-mythos-preview", "claude-mythos-5", "us.anthropic.claude-fable-5-v1:0", "claude-unknown-model", "claudette-5"} {
		t.Run(model, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			key.AnthropicCacheTTLMode = "1h"
			bedrock := strings.Contains(model, "anthropic.")
			if bedrock {
				a.Type = AccountTypeBedrock
			}
			raw := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`)
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, raw)
			ctx, _ = svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			var req *http.Request
			var err error
			if bedrock {
				req, err = svc.buildUpstreamRequestBedrock(ctx, raw, model, "us-east-1", false, NewBedrockSigner("fixture", "fixture", "", "us-east-1"))
			} else {
				req, _, err = svc.buildUpstreamRequest(ctx, c, a, raw, "fixture", "apikey", model, false, false)
			}
			require.NoError(t, err)
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, req.Body.Close())
			ttl := "1h"
			if strings.Contains(model, "unknown") || strings.HasPrefix(model, "claudette") {
				ttl = "5m"
			}
			require.Equal(t, ttl, gjson.GetBytes(wire, "messages.0.content.0.cache_control.ttl").String())
		})
	}
}

func TestAnthropicCacheTTLBedrockFinalCapabilitiesWire(t *testing.T) {
	for _, model := range []string{"anthropic.claude-3-haiku-20240307-v1:0", "anthropic.claude-sonnet-4-5-20250929-v1:0"} {
		t.Run(model, func(t *testing.T) {
			svc, key, a := cacheFixture(t)
			key.AnthropicCacheTTLMode = "1h"
			a.Type = AccountTypeBedrock
			raw := []byte(`{"tools":[{"name":"deferred","custom":{"defer_loading":true},"input_schema":{"type":"object"}}],"system":"stable","messages":[{"role":"user","content":"hi"}]}`)
			ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, raw)
			ctx, _ = svc.beginClaudeCacheAttempt(ctx, &ParsedRequest{GroupID: key.GroupID}, a)
			prepared, err := prepareBedrockRequestBodyWithTokens(raw, model, nil, false, true)
			require.NoError(t, err)
			req, err := svc.buildUpstreamRequestBedrock(ctx, prepared, model, "us-east-1", false, NewBedrockSigner("fixture", "fixture", "", "us-east-1"))
			require.NoError(t, err)
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, req.Body.Close())
			require.False(t, gjson.GetBytes(wire, "tools.0.cache_control").Exists())
			require.False(t, gjson.GetBytes(wire, "tools.0.custom").Exists())
			modern := strings.Contains(model, "4-5")
			require.Equal(t, modern, gjson.GetBytes(wire, "system.0.cache_control.ttl").Exists())
			result := &ForwardResult{}
			finishAnthropicCacheTTLAttempt(ctx, result)
			require.Equal(t, modern, *result.AnthropicCacheTTLApplied)
		})
	}
	_, err := PrepareBedrockRequestBody([]byte(`{"cache_control":{"type":"ephemeral"}}`), "anthropic.claude-fable-5", "")
	require.ErrorContains(t, err, "top-level cache_control")
}

// forwardCacheWire sends raw through a real Forward entrypoint and returns the
// recorded upstream body.
func forwardCacheWire(t *testing.T, route, mode, raw string) (*ForwardResult, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc, key, a := cacheFixture(t)
	key.AnthropicCacheTTLMode = mode
	upstream := ttlWireUpstream(true)
	svc.httpUpstream = upstream
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/"+route, nil)
	ctx := svc.WithClaudeCacheFallbackRequest(context.Background(), key, []byte(raw))
	c.Request = c.Request.WithContext(ctx)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(raw)), PlatformAnthropic)
	require.NoError(t, err)
	parsed.GroupID = key.GroupID
	var result *ForwardResult
	switch route {
	case "chat":
		result, err = svc.ForwardAsChatCompletions(ctx, c, a, []byte(raw), parsed)
	case "responses":
		result, err = svc.ForwardAsResponses(ctx, c, a, []byte(raw), parsed)
	default:
		result, err = svc.Forward(ctx, c, a, parsed)
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	return result, upstream.lastBody
}

const (
	cacheWireCall     = `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"probe","arguments":"{}"}}]}`
	cacheWireCallItem = `{"type":"function_call","call_id":"call_1","name":"probe","arguments":"{}"}`
)

// Converted tool results keep the client's marker, so fallback reports
// client_declared and injects nothing next to it.
func TestAnthropicCacheConvertedToolMarkersWire(t *testing.T) {
	raws := map[string]string{
		"chat":      `{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"system","content":"rules"},{"role":"user","content":"go"},` + cacheWireCall + `,{"role":"tool","tool_call_id":"call_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
		"responses": `{"model":"claude-sonnet-4-5","stream":true,"instructions":"rules","input":[{"type":"message","role":"user","content":"go"},` + cacheWireCallItem + `,{"type":"function_call_output","call_id":"call_1","output":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
	}
	for _, route := range []string{"chat", "responses"} {
		for _, mode := range []string{"off", "5m", "1h"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				result, wire := forwardCacheWire(t, route, mode, raws[route])
				require.Equal(t, "client_declared", result.ClaudeCacheFallback.Reason)
				require.Equal(t, []string{"messages.2.content.0.cache_control"}, anthropicCacheControlPaths(wire), string(wire))
				require.Equal(t, "tool_result", gjson.GetBytes(wire, "messages.2.content.0.type").String())
				wantTTL := mode
				if mode == "off" {
					wantTTL = "1h"
				}
				require.Equal(t, wantTTL, gjson.GetBytes(wire, "messages.2.content.0.cache_control.ttl").String())
				require.Equal(t, mode != "off", *result.AnthropicCacheTTLApplied)
			})
		}
	}
	t.Run("responses/custom_tool_output_part", func(t *testing.T) {
		raw := `{"model":"claude-sonnet-4-5","stream":true,"tools":[{"type":"custom","name":"apply_patch"}],"input":[{"type":"message","role":"user","content":"go"},{"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"patch"},{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"done","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
		result, wire := forwardCacheWire(t, "responses", "off", raw)
		require.Equal(t, "client_declared", result.ClaudeCacheFallback.Reason)
		require.Equal(t, "1h", gjson.GetBytes(wire, "messages.2.content.0.cache_control.ttl").String(), string(wire))
		require.False(t, gjson.GetBytes(wire, "messages.2.content.0.content.0.cache_control").Exists())
	})
	t.Run("chat/trim_keeps_tail", func(t *testing.T) {
		messages := `{"role":"system","content":"rules","cache_control":{"type":"ephemeral","ttl":"1h"}},{"role":"user","content":"go"}`
		for i := 1; i <= 4; i++ {
			messages += fmt.Sprintf(`,{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":"probe","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_%d","content":"OK","cache_control":{"type":"ephemeral"}}`, i, i)
		}
		raw := `{"model":"claude-sonnet-4-5","stream":true,"cache_control":{"type":"ephemeral"},"messages":[` + messages + `]}`
		result, wire := forwardCacheWire(t, "chat", "off", raw)
		require.Equal(t, "client_declared", result.ClaudeCacheFallback.Reason)
		require.Equal(t, []string{"system.0.cache_control", "messages.6.content.0.cache_control", "messages.8.content.0.cache_control", "cache_control"}, anthropicCacheControlPaths(wire), string(wire))
		requireCacheTTLOrderAccepted(t, wire)
	})
}

// Converted bodies reconcile automatic caching with the tail toward 1h;
// native Messages bodies keep the conflict for the provider to reject.
func TestAnthropicCacheConvertedAutomaticTTLWire(t *testing.T) {
	upward := `{"model":"claude-sonnet-4-5","stream":true,"cache_control":{"type":"ephemeral"},"messages":[{"role":"system","content":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":"go"},` + cacheWireCall + `,{"role":"tool","tool_call_id":"call_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`
	t.Run("chat_upward", func(t *testing.T) {
		_, wire := forwardCacheWire(t, "chat", "off", upward)
		for _, path := range []string{"cache_control", "system.0.cache_control", "messages.2.content.0.cache_control"} {
			require.Equal(t, "1h", gjson.GetBytes(wire, path+".ttl").String(), path+" in "+string(wire))
		}
		require.False(t, anthropicAutomaticTailConflict(wire))
		requireCacheTTLOrderAccepted(t, wire)
	})
	t.Run("responses_reverse", func(t *testing.T) {
		raw := `{"model":"claude-sonnet-4-5","stream":true,"cache_control":{"type":"ephemeral","ttl":"1h"},"input":[{"type":"message","role":"user","content":"go"},` + cacheWireCallItem + `,{"type":"function_call_output","call_id":"call_1","output":"OK","cache_control":{"type":"ephemeral"}}]}`
		_, wire := forwardCacheWire(t, "responses", "off", raw)
		require.Equal(t, "1h", gjson.GetBytes(wire, "messages.2.content.0.cache_control.ttl").String(), string(wire))
		require.Equal(t, "1h", gjson.GetBytes(wire, "cache_control.ttl").String())
		require.False(t, anthropicAutomaticTailConflict(wire))
	})
	t.Run("native_unchanged", func(t *testing.T) {
		raw := `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":8,"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"probe","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
		_, wire := forwardCacheWire(t, "messages", "off", raw)
		require.False(t, gjson.GetBytes(wire, "cache_control.ttl").Exists(), string(wire))
		require.True(t, anthropicAutomaticTailConflict(wire))
	})
	t.Run("chat_upward_forced_5m", func(t *testing.T) {
		result, wire := forwardCacheWire(t, "chat", "5m", upward)
		paths := anthropicCacheControlPaths(wire)
		require.Equal(t, []string{"system.0.cache_control", "messages.2.content.0.cache_control", "cache_control"}, paths, string(wire))
		for _, path := range paths {
			require.Equal(t, "5m", gjson.GetBytes(wire, path+".ttl").String(), path)
		}
		require.True(t, *result.AnthropicCacheTTLApplied)
	})
}

func TestAnthropicCacheConvertedAutomaticTTL(t *testing.T) {
	cases := []struct {
		name, body string
		changed    map[string]string
	}{
		{"upward_omitted", `{"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, map[string]string{"cache_control.ttl": "1h"}},
		{"upward_5m", `{"cache_control":{"type":"ephemeral","ttl":"5m"},"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, map[string]string{"cache_control.ttl": "1h", "system.0.cache_control.ttl": "1h"}},
		{"reverse", `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`, map[string]string{"messages.0.content.0.cache_control.ttl": "1h"}},
		{"thinking_skipped", `{"cache_control":{"type":"ephemeral"},"messages":[{"role":"assistant","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"thinking","thinking":"t","signature":"x"}]}]}`, map[string]string{"cache_control.ttl": "1h"}},
		{"string_tail", `{"cache_control":{"type":"ephemeral","ttl":"5m"},"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`, nil},
		{"invalid_ttl", `{"cache_control":{"type":"ephemeral","ttl":"10m"},"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, nil},
		{"not_ephemeral", `{"cache_control":{"type":"persistent"},"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			out := enforceConvertedCacheControlLimit(body)
			require.Equal(t, string(out), string(enforceConvertedCacheControlLimit(out)), "idempotent")
			if tc.changed == nil {
				require.Equal(t, tc.body, string(out))
				return
			}
			require.Equal(t, tc.body, string(enforceCacheControlLimit(body)), "native bodies keep the conflict")
			for path, ttl := range tc.changed {
				require.Equal(t, ttl, gjson.GetBytes(out, path).String(), path)
			}
			require.False(t, anthropicAutomaticTailConflict(out))
			requireCacheTTLOrderAccepted(t, out)
		})
	}
}
