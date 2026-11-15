//go:build unit

package service

import (
	"bytes"
	"context"
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
	"github.com/tidwall/gjson"
)

func TestReasoningBillingUsesFinalWire(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name, fields, want string
		multiplier         float64
	}{
		{"max", `,"reasoning":{"effort":"max"}`, "max", 3},
		{"xhigh", `,"reasoning":{"effort":"xhigh"}`, "xhigh", 2},
		{"thinking_only", `,"thinking":{"type":"enabled","budget_tokens":4096}`, "", 1},
		{"missing", "", "", 1},
		{"unknown", `,"reasoning":{"effort":"unrecognized"}`, "", 1},
		{"ultra", `,"reasoning":{"effort":"ultra"}`, "ultra", 1},
	} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", tc.name, partial), func(t *testing.T) {
				body := []byte(`{"model":"fixture-wire","input":"hello","stream":true` + tc.fields + `}`)
				terminal := `{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`
				if partial {
					terminal = `{"type":"response.failed","response":{"id":"resp_fixture","status":"failed","error":{"code":"server_error","message":"fixture"},"usage":{"input_tokens":10,"output_tokens":5}}}`
				}
				stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: " + terminal + "\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.invalid"}}
				result, err := svc.Forward(context.Background(), c, account, body)
				if partial {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 1)
				if tc.want == "" {
					require.Nil(t, result.ReasoningEffort)
				} else {
					require.Equal(t, tc.want, *result.ReasoningEffort)
					require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
				}
				require.Equal(t, extractOpenAIReasoningEffortFromRequest(upstream.lastReq), result.ReasoningEffort)
				repo := &openAIRecordUsageLogRepoStub{inserted: true}
				recorder := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				recorder.cfg.Default.RateMultiplier = 1
				billing := NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, `{"fixture-wire":{"input_cost_per_token":0.001,"output_cost_per_token":0.002}}`))
				recorder.billingService = billing
				recorder.resolver = NewModelPricingResolver(nil, billing)
				group := &Group{ID: 4, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{{Models: []string{"fixture-wire"}, ReasoningEffortMultipliers: map[string]float64{"max": 3, "xhigh": 2, "high": 7}}}}
				key := &APIKey{ID: 2, GroupID: &group.ID, Group: group}
				result.RequestedReasoningEffort = new("high")
				require.NoError(t, recorder.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: &User{ID: 1}, Account: account}))
				require.Equal(t, result.ReasoningEffort, repo.lastLog.ReasoningEffort)
				require.Equal(t, "high", *repo.lastLog.RequestedReasoningEffort)
				require.InDelta(t, 0.02*tc.multiplier, repo.lastLog.TotalCost, 1e-12)
				generic := &GatewayService{billingService: billing, resolver: recorder.resolver}
				genericResult := &ForwardResult{Model: result.Model, ReasoningEffort: result.ReasoningEffort, Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 5}}
				genericCost := generic.calculateTokenCost(context.Background(), genericResult, key, result.Model, 1, time.Now())
				require.InDelta(t, repo.lastLog.TotalCost, genericCost.TotalCost, 1e-12)
				snapshot := BillingUsageSnapshot(repo.lastLog)
				require.Equal(t, result.ReasoningEffort, snapshot.ReasoningEffort)
			})
		}
	}
	// A final builder snapshot with the field removed must not recover the inbound value.
	req, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader(`{"model":"fixture-wire","reasoning":{}}`))
	require.NoError(t, err)
	require.Nil(t, extractOpenAIReasoningEffortFromRequest(req))
	require.Equal(t, 1.0, reasoningEffortBillingMultiplier(optionalStringValue(extractOpenAIReasoningEffortFromRequest(req)), map[string]float64{"high": 7}))
	remaining, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"fixture-wire","reasoning":{}}`, string(remaining))
}
