package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/stretchr/testify/require"
)

// This corpus intentionally compiles on the unmodified pre-P0a baseline.
// Golden generation is permitted only when the configuration has no trace field.
type cacheBillingCorpusCase struct {
	name, wire string
	maxLine    int
	shortReads bool
}

func cacheBillingCorpus(raw bool) []cacheBillingCorpusCase {
	frame := func(payload string) string { return "data: " + payload + "\n\n" }
	usage := `"input_tokens":4096,"output_tokens":21,"input_tokens_details":{"cached_tokens":1024,"cache_write_tokens":128,"image_tokens":8},"output_tokens_details":{"image_tokens":2}`
	delta := frame(`{"type":"response.output_text.delta","delta":"private-output-marker"}`)
	done := frame(`{"type":"response.completed","response":{"id":"resp-corpus","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{` + usage + `}}}`)
	prior := frame(`{"type":"response.output_text.delta","usage":{"input_tokens":2048,"output_tokens":10,"cache_read_input_tokens":512,"cache_creation_input_tokens":64}}`)
	if raw {
		delta = frame(`{"id":"cc-corpus","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","content":"private-output-marker"}}]}`)
		done = frame(`{"id":"cc-corpus","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4096,"completion_tokens":21,"prompt_tokens_details":{"cached_tokens":1024,"cache_write_tokens":128,"image_tokens":8},"completion_tokens_details":{"image_tokens":2}}}`)
		prior = frame(`{"id":"cc-corpus","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":2048,"completion_tokens":10,"cache_read_input_tokens":512,"cache_creation_input_tokens":64}}`)
	}
	oversized := strings.Replace(delta, "private-output-marker", strings.Repeat("x", 2<<20), 1)
	return []cacheBillingCorpusCase{
		{"normal", delta + done + "data: [DONE]\n\n", 0, false},
		{"error_frame", delta + prior + frame(`{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error","code":"server_error","message":"synthetic error"},"usage":{`+usage+`}}}`), 0, false},
		{"bare_error", prior + frame(`{"type":"error","error":{"type":"server_error","message":"synthetic error"}}`), 0, false},
		{"half_frame", prior + delta + `data: {"type":"response.completed","response":{"usage":`, 0, false},
		{"fragmented_reads", delta + done + "data: [DONE]\n\n", 0, true},
		{"multiple_usage", prior + delta + prior + done + "data: [DONE]\n\n", 0, false},
		{"missing_done", delta + done, 0, false},
		{"missing_terminal", prior + delta, 0, false},
		{"non_utf8", strings.Replace(delta, "private-output-marker", "invalid-"+string([]byte{0xff, 0xfe}), 1) + done, 0, false},
		{"oversized_frame", oversized + done + "data: [DONE]\n\n", 0, false},
		{"frame_limit_exceeded", oversized + done, 1 << 20, false},
	}
}

type cacheBillingShortReader struct{ io.Reader }

func (r cacheBillingShortReader) Read(p []byte) (int, error) {
	if len(p) > 7 {
		p = p[:7]
	}
	return r.Reader.Read(p)
}
func cacheBillingJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}
func cacheBillingErr(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T: %s", err, err.Error())
}

func cacheBillingRun(t *testing.T, tc cacheBillingCorpusCase, protocol, kind string, stream, enabled bool) map[string]any {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
	svc.cfg.JWT.Secret = "synthetic-corpus-secret"
	svc.cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	svc.cfg.Gateway.MaxLineSize = tc.maxLine
	trace := reflect.ValueOf(&svc.cfg.Gateway).Elem().FieldByName("CacheTrace")
	if trace.IsValid() {
		trace.FieldByName("Enabled").SetBool(enabled)
		trace.FieldByName("ChainPercent").SetInt(100)
	} else {
		require.False(t, enabled)
	}
	var upstreamBody io.Reader = strings.NewReader(tc.wire)
	if tc.shortReads {
		upstreamBody = cacheBillingShortReader{upstreamBody}
	}
	svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"rid-corpus"}}, Body: io.NopCloser(upstreamBody)}}
	field, path := "input", "/v1/responses"
	if protocol != "responses" {
		field, path = "messages", "/v1/chat/completions"
	}
	body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","%s":[{"role":"user","content":"private-input-marker"}],"stream":%t}`, field, stream))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Session_id", "private-session-marker")
	api := openAIRecordUsageAPIKeyWithGroup(svc, 1000, false)
	groupID := int64(1)
	api.GroupID = &groupID
	api.Group.Platform = PlatformOpenAI
	api.Group.RateMultiplier = 1
	user := &User{ID: 2000}
	api.User = user
	c.Set("api_key", api)
	account := &Account{ID: 3000, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic-token"}, Extra: map[string]any{"openai_responses_supported": protocol != "rawcc"}}
	if kind == "oauth" {
		account.Type = AccountTypeOAuth
		account.Credentials = map[string]any{"access_token": "synthetic-token", "chatgpt_account_id": "synthetic-account"}
	}
	svc.GenerateSessionHash(c, body)
	var result *OpenAIForwardResult
	var err error
	if protocol == "responses" {
		result, err = svc.Forward(c.Request.Context(), c, account, body)
	} else {
		result, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", "")
	}
	out := map[string]any{"forward_error": cacheBillingErr(err), "status": c.Writer.Status(), "settlement_calls": 0, "settlement_error": "", "usage": nil, "billing_command": nil, "usage_log": nil}
	// The unchanged HTTP handler bills any non-nil result, including partial
	// usage on an error; failover returns nil. Use its real RecordUsage entry.
	if result != nil {
		usageFields := map[string]any{}
		v := reflect.ValueOf(result.Usage)
		for i := 0; i < v.NumField(); i++ {
			usageFields[v.Type().Field(i).Name] = v.Field(i).Interface()
		}
		out["usage"] = usageFields
		// Freeze only nondeterministic wall durations before settlement. Every
		// token/source/model/tier/error and billing argument remains compared.
		result.Duration = time.Second
		if result.FirstTokenMs != nil {
			first := 17
			result.FirstTokenMs = &first
		}
		input := &OpenAIRecordUsageInput{Result: result, APIKey: api, User: user, Account: account, InboundEndpoint: path, UpstreamEndpoint: result.UpstreamEndpoint, SessionID: "synthetic-session", QuotaPlatform: PlatformOpenAI, PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
		params, marshalErr := json.Marshal(input)
		require.NoError(t, marshalErr)
		hash := sha256.Sum256(params)
		out["settlement_parameters_sha256"] = hex.EncodeToString(hash[:])
		out["forward_result"] = cacheBillingJSON(t, result)
		out["settlement_calls"] = 1
		out["settlement_error"] = cacheBillingErr(svc.RecordUsage(context.Background(), input))
	}
	// Repository-owned timestamps are not deterministic output of the parser.
	// These are the sole persistence-field normalization, recorded in the notes.
	if usageRepo.lastLog != nil {
		usageRepo.lastLog.CreatedAt = time.Time{}
	}
	if billingRepo.lastCmd != nil && billingRepo.lastCmd.Usage != nil {
		billingRepo.lastCmd.Usage.CreatedAt = time.Time{}
	}
	out["billing_command"] = cacheBillingJSON(t, billingRepo.lastCmd)
	out["billing_apply_calls"] = billingRepo.calls
	out["usage_log"] = cacheBillingJSON(t, usageRepo.lastLog)
	out["usage_create_calls"] = usageRepo.calls
	out["balance_deduct_calls"] = userRepo.deductCalls
	out["subscription_calls"] = subRepo.incrementCalls
	decoded, ok := cacheBillingJSON(t, out).(map[string]any)
	require.True(t, ok, "normalized billing result must be an object")
	return decoded
}

func TestCacheTraceBillingCorpusGolden(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	previousLog, previousSlog := log.Writer(), slog.Default()
	log.SetOutput(io.Discard)
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	defer func() { log.SetOutput(previousLog); slog.SetDefault(previousSlog) }()
	output := os.Getenv("P0A_CACHE_BILLING_BASELINE_OUT")
	var expected map[string]map[string]any
	if output == "" {
		raw, err := os.ReadFile("testdata/cache_trace_billing_golden.json")
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &expected))
	} else {
		_, hasTrace := reflect.TypeOf(config.GatewayConfig{}).FieldByName("CacheTrace")
		require.False(t, hasTrace, "golden must be generated from the pure baseline")
	}
	generated := map[string]map[string]any{}
	for _, protocol := range []string{"responses", "chat", "rawcc"} {
		for _, kind := range []string{"apikey", "oauth"} {
			if protocol == "rawcc" && kind == "oauth" {
				continue
			}
			for _, stream := range []bool{false, true} {
				for _, tc := range cacheBillingCorpus(protocol == "rawcc") {
					name := fmt.Sprintf("%s/%s/stream=%t/%s", protocol, kind, stream, tc.name)
					t.Run(name, func(t *testing.T) {
						off := cacheBillingRun(t, tc, protocol, kind, stream, false)
						if output != "" {
							generated[name] = off
							return
						}
						require.Equal(t, expected[name], off, "disabled billing differs from pure baseline")
						on := cacheBillingRun(t, tc, protocol, kind, stream, true)
						require.Equal(t, expected[name], on, "enabled billing differs from pure baseline")
					})
				}
			}
		}
	}
	if output != "" {
		raw, err := json.MarshalIndent(generated, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(output, raw, 0600))
	}
}
