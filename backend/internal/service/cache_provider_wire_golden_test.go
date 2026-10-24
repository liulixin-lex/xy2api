//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// These synthetic fixtures are first recorded from unmodified main. The only
// normalized request fields are the time-dependent AWS signature and date:
// signing is repeated at a fixed instant with the same fixture credentials.
type m2bWireGolden struct {
	Method  string
	URL     string
	Headers http.Header
	Body    string
	Usage   ClaudeUsage
	Billing map[string]any
}

func m2bClaudePayload(stream bool) string {
	return fmt.Sprintf("{\"model\":\"claude-sonnet-4-5\",\"stream\":%t,\"max_tokens\":64,\"system\":[{\"type\":\"text\",\"text\":\"fixture system\",\"cache_control\":{\"type\":\"ephemeral\"}}],\"messages\":[{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"fixture user\"}]}]}", stream)
}

func m2bClaudeResponse(stream bool) *http.Response {
	usage := "\"input_tokens\":17,\"output_tokens\":9,\"cache_creation_input_tokens\":5,\"cache_read_input_tokens\":1024,\"cache_creation\":{\"ephemeral_5m_input_tokens\":3,\"ephemeral_1h_input_tokens\":2}"
	body := "{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[{\"type\":\"text\",\"text\":\"fixture reply\"}],\"stop_reason\":\"end_turn\",\"usage\":{" + usage + "}}"
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
		body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{" + usage + "}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture reply\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}, "X-Request-Id": {"fixture-request"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func m2bBillingGolden(t *testing.T, usage ClaudeUsage, model string, account *Account) map[string]any {
	t.Helper()
	repo := &openAIRecordUsageLogRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	result := &ForwardResult{RequestID: "fixture-billing", Usage: usage, Model: model, Duration: time.Second}
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: &APIKey{ID: 501}, User: &User{ID: 601}, Account: account})
	require.NoError(t, err)
	require.NotNil(t, repo.lastLog)
	u := repo.lastLog
	return map[string]any{"input": u.InputTokens, "output": u.OutputTokens, "create": u.CacheCreationTokens, "read": u.CacheReadTokens, "input_cost": u.InputCost, "output_cost": u.OutputCost, "create_cost": u.CacheCreationCost, "read_cost": u.CacheReadCost, "total_cost": u.TotalCost, "actual_cost": u.ActualCost}
}

func m2bRunProviderWire(t *testing.T, route string, stream bool) m2bWireGolden {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(m2bClaudePayload(stream))
	path := "/v1/messages"
	account := newAnthropicAPIKeyAccountForTest()
	account.Extra = map[string]any{}
	if route == "chat" {
		path = "/v1/chat/completions"
		body = []byte(fmt.Sprintf("{\"model\":\"claude-sonnet-4-5\",\"stream\":%t,\"max_tokens\":64,\"messages\":[{\"role\":\"system\",\"content\":\"fixture system\"},{\"role\":\"user\",\"content\":\"fixture user\"}]}", stream))
	}
	if route == "responses" {
		path = "/v1/responses"
		body = []byte(fmt.Sprintf("{\"model\":\"claude-sonnet-4-5\",\"stream\":%t,\"max_output_tokens\":64,\"instructions\":\"fixture system\",\"input\":[{\"role\":\"user\",\"content\":\"fixture user\"}]}", stream))
	}
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("User-Agent", "anthropic-sdk-fixture/1.0")
	c.Request.Header.Set("X-Client-Request-Id", "00000000-0000-4000-8000-000000000001")
	groupID := int64(401)
	c.Set("api_key", &APIKey{ID: 501, GroupID: &groupID, Group: &Group{ID: 401, Platform: PlatformAnthropic}})
	upstream := &anthropicHTTPUpstreamRecorder{resp: m2bClaudeResponse(stream || route == "chat" || route == "responses")}
	svc := newForwardPartialUsageServiceForTest(upstream)
	svc.cfg = cfg
	var req *http.Request
	var result *ForwardResult
	var err error
	var usage ClaudeUsage
	switch route {
	case "native", "passthrough", "chat", "responses":
		if route == "passthrough" {
			account.Extra["anthropic_passthrough"] = true
		}
		parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, parseErr)
		switch route {
		case "chat":
			result, err = svc.ForwardAsChatCompletions(ctx, c, account, body, parsed)
		case "responses":
			result, err = svc.ForwardAsResponses(ctx, c, account, body, parsed)
		default:
			result, err = svc.Forward(ctx, c, account, parsed)
		}
		require.NoError(t, err)
		require.NotNil(t, result)
		usage = result.Usage
		req = upstream.lastReq
	case "vertex", "bedrock", "bedrock_sigv4", "oauth_builder":
		switch route {
		case "vertex":
			account.Type = AccountTypeServiceAccount
			account.Credentials = map[string]any{"project_id": "fixture-project", "location": "us-east5"}
			req, _, err = svc.buildUpstreamRequest(ctx, c, account, body, "fixture-token", "service_account", "claude-sonnet-4-5@20250929", stream, false)
		case "oauth_builder":
			account = newAnthropicOAuthAccountForPartialUsageTest()
			req, _, err = svc.buildUpstreamRequest(ctx, c, account, body, "fixture-token", "oauth", "claude-sonnet-4-5", stream, false)
		default:
			prepared, prepareErr := PrepareBedrockRequestBody(body, "us.anthropic.claude-sonnet-4-5-v1", "")
			require.NoError(t, prepareErr)
			if route == "bedrock" {
				req, err = svc.buildUpstreamRequestBedrockAPIKey(ctx, prepared, "us.anthropic.claude-sonnet-4-5-v1", "us-east-1", stream, "fixture-key")
			} else {
				signer := NewBedrockSigner("fixture-access", "fixture-secret", "", "us-east-1")
				req, err = svc.buildUpstreamRequestBedrock(ctx, prepared, "us.anthropic.claude-sonnet-4-5-v1", "us-east-1", stream, signer)
				require.NoError(t, err)
				payload, readErr := io.ReadAll(req.Body)
				require.NoError(t, readErr)
				require.NoError(t, req.Body.Close())
				req.Body = io.NopCloser(bytes.NewReader(payload))
				req.Header.Del("Authorization")
				req.Header.Del("X-Amz-Date")
				err = signer.signer.SignHTTP(ctx, signer.credentials, req, sha256Hash(payload), "bedrock", "us-east-1", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
			}
		}
		require.NoError(t, err)
		if stream {
			// The builder golden and actual stream parser golden are separate; no
			// synthetic AWS event framing is substituted for the provider transport.
			svc.parseSSEUsage("{\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":17,\"cache_creation_input_tokens\":5,\"cache_read_input_tokens\":1024}}}", &usage)
			svc.parseSSEUsage("{\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}", &usage)
		} else {
			var parsedUsage *ClaudeUsage
			if strings.HasPrefix(route, "bedrock") {
				parsedUsage, err = svc.handleBedrockNonStreamingResponse(ctx, m2bClaudeResponse(false), c, account)
			} else {
				parsedUsage, err = svc.handleNonStreamingResponse(ctx, m2bClaudeResponse(false), c, account, "claude-sonnet-4-5", "claude-sonnet-4-5")
			}
			require.NoError(t, err)
			require.NotNil(t, parsedUsage)
			usage = *parsedUsage
		}
	case "gemini_compat", "gemini_native":
		account = &Account{ID: 703, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "fixture-key"}}
		response := "{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"fixture reply\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1046,\"cachedContentTokenCount\":1024,\"candidatesTokenCount\":9,\"totalTokenCount\":1055}}"
		contentType := "application/json"
		if stream {
			response = "data: " + response + "\n\n"
			contentType = "text/event-stream"
		}
		upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}, "X-Request-Id": {"fixture-gemini"}}, Body: io.NopCloser(strings.NewReader(response))}
		gemini := &GeminiMessagesCompatService{cfg: cfg, httpUpstream: upstream}
		if route == "gemini_compat" {
			body = []byte(fmt.Sprintf("{\"model\":\"gemini-2.5-flash\",\"stream\":%t,\"max_tokens\":64,\"system\":\"fixture system\",\"messages\":[{\"role\":\"user\",\"content\":\"fixture user\"}]}", stream))
			result, err = gemini.Forward(ctx, c, account, body)
		} else {
			action := "generateContent"
			if stream {
				action = "streamGenerateContent"
			}
			body = []byte("{\"systemInstruction\":{\"parts\":[{\"text\":\"fixture system\"}]},\"contents\":[{\"role\":\"user\",\"parts\":[{\"text\":\"fixture user\"}]}]}")
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:"+action, bytes.NewReader(body))
			result, err = gemini.ForwardNative(ctx, c, account, "gemini-2.5-flash", action, stream, body)
		}
		require.NoError(t, err)
		require.NotNil(t, result)
		usage = result.Usage
		req = upstream.lastReq
	default:
		t.Fatalf("unknown route %s", route)
	}
	require.NotNil(t, req)
	payload, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())
	model := "claude-sonnet-4-5"
	if account.Platform == PlatformGemini {
		model = "gemini-2.5-flash"
	}
	return m2bWireGolden{Method: req.Method, URL: req.URL.String(), Headers: req.Header.Clone(), Body: string(payload), Usage: usage, Billing: m2bBillingGolden(t, usage, model, account)}
}

func TestCacheProviderWireAndBillingGolden(t *testing.T) {
	records := map[string]m2bWireGolden{}
	for _, route := range []string{"native", "passthrough", "chat", "responses", "vertex", "bedrock", "bedrock_sigv4", "oauth_builder", "gemini_compat", "gemini_native"} {
		for _, stream := range []bool{false, true} {
			name := fmt.Sprintf("%s/stream=%t", route, stream)
			t.Run(name, func(t *testing.T) { records[name] = m2bRunProviderWire(t, route, stream) })
		}
	}
	if t.Failed() {
		return
	}
	encoded, err := json.MarshalIndent(records, "", "  ")
	require.NoError(t, err)
	if target := os.Getenv("M2B_RECORD_GOLDEN"); target != "" {
		require.NoError(t, os.WriteFile(target, append(encoded, '\n'), 0600))
		return
	}
	golden, err := os.ReadFile("testdata/cache_provider_wire_main.json")
	require.NoError(t, err)
	var want map[string]m2bWireGolden
	require.NoError(t, json.Unmarshal(golden, &want))
	// Decode both sides so interface-valued billing integers share JSON types.
	var got map[string]m2bWireGolden
	require.NoError(t, json.Unmarshal(encoded, &got))
	require.Equal(t, want, got)
}

func TestCacheProviderUsageNormalizationGolden(t *testing.T) {
	cases := map[string]string{
		"canonical":         "{\"input_tokens\":17,\"output_tokens\":9,\"cache_creation_input_tokens\":5,\"cache_read_input_tokens\":1024}",
		"cached_alias":      "{\"input_tokens\":1041,\"output_tokens\":9,\"cached_tokens\":1024}",
		"explicit_zero":     "{\"input_tokens\":17,\"output_tokens\":9,\"cache_read_input_tokens\":0,\"cached_tokens\":1024}",
		"missing":           "{\"input_tokens\":17,\"output_tokens\":9}",
		"split":             "{\"input_tokens\":17,\"output_tokens\":9,\"cache_creation_input_tokens\":5,\"cache_read_input_tokens\":1024,\"cache_creation\":{\"ephemeral_5m_input_tokens\":3,\"ephemeral_1h_input_tokens\":2}}",
		"kimi_total":        "{\"input_tokens\":1041,\"prompt_tokens\":1041,\"prompt_cache_hit_tokens\":1024,\"prompt_cache_miss_tokens\":17,\"output_tokens\":9}",
		"kimi_uncached":     "{\"input_tokens\":17,\"prompt_tokens\":1041,\"prompt_cache_hit_tokens\":1024,\"output_tokens\":9}",
		"hit_miss":          "{\"input_tokens\":1041,\"prompt_cache_hit_tokens\":1024,\"prompt_cache_miss_tokens\":17,\"output_tokens\":9}",
		"zero_prompt_alias": "{\"input_tokens\":17,\"prompt_tokens\":0,\"prompt_cache_hit_tokens\":0,\"output_tokens\":9}",
		"nested_alias":      "{\"input_tokens\":1041,\"output_tokens\":9,\"input_tokens_details\":{\"cached_tokens\":1024}}",
	}
	records := map[string]ClaudeUsage{}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			event := "{\"type\":\"message_start\",\"message\":{\"usage\":" + raw + "}}"
			var normal, passthrough ClaudeUsage
			svc := newForwardPartialUsageServiceForTest(&anthropicHTTPUpstreamRecorder{})
			svc.parseSSEUsage(event, &normal)
			parseSSEUsagePassthrough(event, &passthrough)
			records[name+"/stream"] = normal
			records[name+"/passthrough_stream"] = passthrough
			body := "{\"id\":\"msg_fixture\",\"type\":\"message\",\"model\":\"claude-sonnet-4-5\",\"content\":[{\"type\":\"text\",\"text\":\"fixture\"}],\"usage\":" + raw + "}"
			records[name+"/passthrough_buffered"] = *parseClaudeUsageFromResponseBody([]byte(body))
			for _, bedrock := range []bool{false, true} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
				var usage *ClaudeUsage
				var err error
				account := newAnthropicAPIKeyAccountForTest()
				if bedrock {
					usage, err = svc.handleBedrockNonStreamingResponse(context.Background(), response, c, account)
				} else {
					usage, err = svc.handleNonStreamingResponse(context.Background(), response, c, account, "claude-sonnet-4-5", "claude-sonnet-4-5")
				}
				require.NoError(t, err)
				require.NotNil(t, usage)
				records[fmt.Sprintf("%s/buffered_bedrock=%t", name, bedrock)] = *usage
			}
		})
	}
	if t.Failed() {
		return
	}
	type usageGolden struct {
		Usage   ClaudeUsage
		Billing map[string]any
	}
	billed := map[string]usageGolden{}
	for name, usage := range records {
		billed[name] = usageGolden{Usage: usage, Billing: m2bBillingGolden(t, usage, "claude-sonnet-4-5", newAnthropicAPIKeyAccountForTest())}
	}
	raw, err := json.MarshalIndent(billed, "", "  ")
	require.NoError(t, err)
	if target := os.Getenv("M2B_RECORD_USAGE_GOLDEN"); target != "" {
		require.NoError(t, os.WriteFile(target, append(raw, '\n'), 0600))
		return
	}
	expected, err := os.ReadFile("testdata/cache_provider_usage_main.json")
	require.NoError(t, err)
	var want, got map[string]usageGolden
	require.NoError(t, json.Unmarshal(expected, &want))
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, want, got)
}
