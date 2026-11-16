//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestNativeAnthropicTTLProjection(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		aggregate, five, hour                      int
		aggregatePresent, fivePresent, hourPresent bool
		want                                       float64
	}{
		{"hour", 1000, 0, 1000, true, true, true, 0.01},
		{"explicit_zero_aggregate", 0, 0, 1000, true, false, true, 0},
		{"missing_aggregate", 1000, 0, 1000, false, false, true, 0.01},
		{"partial_buckets", 1000, 0, 500, true, false, true, 0.0075},
		{"missing_buckets", 1000, 0, 0, true, false, false, 0.005},
		{"explicit_zero_buckets", 1000, 0, 0, true, true, true, 0.005},
	} {
		for _, origin := range []string{"catalog", "group", "channel"} {
			t.Run(fmt.Sprintf("%s/%s", tc.name, origin), func(t *testing.T) {
				repo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.cfg.Default.RateMultiplier = 1
				catalog := newStubPricingServiceFromJSON(t, `{"fixture-ttl":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"cache_creation_input_token_cost":0.000005,"cache_creation_input_token_cost_above_1hr":0.00001}}`)
				billing := NewBillingService(&config.Config{}, catalog)
				svc.billingService = billing
				svc.resolver = NewModelPricingResolver(nil, billing)
				group := &Group{ID: 100, Platform: PlatformAnthropic, RateMultiplier: 1}
				want := tc.want
				if origin != "catalog" {
					card := ChannelModelPricing{Platform: PlatformAnthropic, Models: []string{"fixture-ttl"}, CacheWritePrice: new(float64(6e-6)), CacheWrite1hPrice: new(float64(2e-5))}
					if origin == "group" {
						group.ModelPricing = []ChannelModelPricing{card}
					} else {
						billing, svc.resolver = newTokenCostTestEnv(t, PlatformAnthropic, []ChannelModelPricing{card}, catalog)
						svc.billingService = billing
					}
					switch tc.name {
					case "hour", "missing_aggregate":
						want = 0.02
					case "partial_buckets":
						want = 0.013
					case "explicit_zero_aggregate":
						want = 0
					default:
						want = 0.006
					}
				}
				key := &APIKey{ID: 2, GroupID: &group.ID, Group: group}
				result := &OpenAIForwardResult{RequestID: "ttl-fixture", Model: "fixture-ttl", Usage: OpenAIUsage{InputTokens: tc.aggregate, CacheCreationInputTokens: tc.aggregate, CacheCreationInputTokensPresent: tc.aggregatePresent, CacheCreation5mTokens: tc.five, CacheCreation1hTokens: tc.hour, CacheCreation5mTokensPresent: tc.fivePresent, CacheCreation1hTokensPresent: tc.hourPresent}}
				require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: &Account{ID: 3}}))
				require.NotNil(t, repo.lastLog)
				require.InDelta(t, want, repo.lastLog.CacheCreationCost, 1e-12)
				require.Equal(t, tc.five, repo.lastLog.CacheCreation5mTokens)
				require.Equal(t, tc.hour, repo.lastLog.CacheCreation1hTokens)
				generic := &GatewayService{billingService: billing, resolver: svc.resolver}
				comparison := generic.calculateTokenCost(context.Background(), &ForwardResult{Model: "fixture-ttl", Usage: ClaudeUsage{CacheCreationInputTokens: tc.aggregate, CacheCreationInputTokensPresent: tc.aggregatePresent, CacheCreation5mTokens: tc.five, CacheCreation1hTokens: tc.hour, CacheCreation5mTokensPresent: tc.fivePresent, CacheCreation1hTokensPresent: tc.hourPresent}}, key, "fixture-ttl", 1, time.Now())
				require.InDelta(t, comparison.CacheCreationCost, repo.lastLog.CacheCreationCost, 1e-12)

				// B3: native JSON/SSE -> M7's ClaudeUsage conversion -> real RecordUsage.
				rawUsage := map[string]any{"input_tokens": 0, "output_tokens": 0}
				if tc.aggregatePresent {
					rawUsage["cache_creation_input_tokens"] = tc.aggregate
				}
				buckets := map[string]int{}
				if tc.fivePresent {
					buckets["ephemeral_5m_input_tokens"] = tc.five
				}
				if tc.hourPresent {
					buckets["ephemeral_1h_input_tokens"] = tc.hour
				}
				if len(buckets) > 0 {
					rawUsage["cache_creation"] = buckets
				}
				usageJSON, marshalErr := json.Marshal(rawUsage)
				require.NoError(t, marshalErr)
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("native_stream=%t", stream), func(t *testing.T) {
						payload := `{"id":"msg_ttl_b3","type":"message","role":"assistant","model":"fixture-ttl","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":` + string(usageJSON) + `}`
						contentType := "application/json"
						if stream {
							contentType = "text/event-stream"
							payload = intB3BillingStream("anthropic", false)
							payload = strings.ReplaceAll(payload, `{"input_tokens":10,"output_tokens":5}`, string(usageJSON))
							payload = strings.ReplaceAll(payload, `"usage":{"output_tokens":5}`, `"usage":{"output_tokens":0}`)
						}
						upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload))}}
						forwarder := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
						body := []byte(fmt.Sprintf(`{"model":"fixture-ttl","max_tokens":16384,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream))
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
						nativeAccount := nativeAnthropicTestAccount()
						nativeResult, forwardErr := forwarder.forwardAnthropicViaNativeAnthropicEndpoint(context.Background(), c, nativeAccount, body, "")
						require.NoError(t, forwardErr)
						require.NotNil(t, nativeResult)
						require.Len(t, upstream.bodies, 1)
						require.Equal(t, tc.aggregate, nativeResult.Usage.CacheCreationInputTokens)
						require.Equal(t, tc.aggregatePresent, nativeResult.Usage.CacheCreationInputTokensPresent)
						require.Equal(t, tc.fivePresent, nativeResult.Usage.CacheCreation5mTokensPresent)
						require.Equal(t, tc.hourPresent, nativeResult.Usage.CacheCreation1hTokensPresent)
						nativeRepo := &openAIRecordUsageLogRepoStub{inserted: true}
						nativeLedger := newOpenAIRecordUsageServiceForTest(nativeRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
						nativeLedger.cfg.Default.RateMultiplier = 1
						nativeLedger.billingService = billing
						nativeLedger.resolver = svc.resolver
						require.NoError(t, nativeLedger.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: nativeResult, APIKey: key, User: &User{ID: 1}, Account: nativeAccount}))
						require.NotNil(t, nativeRepo.lastLog)
						require.InDelta(t, want, nativeRepo.lastLog.CacheCreationCost, 1e-12)
						require.InDelta(t, comparison.CacheCreationCost, nativeRepo.lastLog.CacheCreationCost, 1e-12)
						require.Equal(t, tc.five, nativeRepo.lastLog.CacheCreation5mTokens)
						require.Equal(t, tc.hour, nativeRepo.lastLog.CacheCreation1hTokens)
					})
				}
			})
		}
	}
}
