//go:build unit

package service

import (
	"context"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func m1EffortContext(body []byte) context.Context {
	state := resilience.NewRequestState()
	state.EnableGatewayRequest(time.Now())
	ctx := resilience.WithRequestState(context.Background(), state)
	if requested := CanonicalRequestedReasoningEffort(body); requested != nil {
		ctx = WithRequestedReasoningEffort(ctx, *requested)
	}
	return WithFinalReasoningEffortRequest(ctx, body)
}
func TestM1FinalEffortPolicy(t *testing.T) {
	for _, tc := range []struct{ model, effort, want string }{
		{"gpt-6.1-sol", "none", "low"}, {"gpt-6.1-sol-none", "none", "low"}, {"gpt-6.1-sol", "minimal", "low"}, {"gpt-6.1-sol", "max", "max"},
		{"gpt-6.1-terra", "minimal", "none"}, {"gpt-6.1-terra", "none", "none"},
		{"gpt-6-sol", "none", "none"}, {"gpt-6-luna", "none", "none"},
		{"grok-4.7", "none", "none"}, {"grok-4.7", "minimal", "low"},
		{"claude-sonnet-5-5", "none", "none"}, {"claude-opus-4-6", "xhigh", "high"},
		{"unknown", "none", "none"}, {"gpt-6.1-terra", "ultra", "ultra"},
	} {
		t.Run(tc.model+tc.effort, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"model": tc.model, "reasoning": map[string]string{"effort": tc.effort}})
			account := newOpenAIRejectedFieldTestAccount()
			reasoning := true
			account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{tc.model: {ID: tc.model, Reasoning: &reasoning, SupportedReasoningLevels: []string{"low", "high"}}}})
			ctx := m1EffortContext(body)
			got, _, err := applyFinalReasoningEffortForAccount(ctx, nil, account, body, tc.model, "responses")
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.GetBytes(got, "reasoning.effort").String())
			require.NoError(t, validateGPT61SolCompatRequest(got, tc.model))
			if tc.effort != "ultra" {
				require.Equal(t, tc.effort, *RequestedReasoningEffortFromContext(ctx))
			}
			if tc.want != tc.effort {
				require.Contains(t, resilience.SnapshotConversionNotices(ctx), apicompat.ConversionNotice{Field: "reasoning.effort", Action: "map"})
			}
		})
	}
}
func TestM1ResolvedTargetPolicy(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	original := []byte(`{"model":"alias","reasoning_effort":"low","metadata":{"secret":1}}`)
	ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(original), "", []ReasoningEffortMapping{{From: "low", To: "medium", Model: "gpt-6.1-sol", MatchType: "exact"}, {From: "medium", To: "high"}}, "")
	first, _, err := applyFinalReasoningEffortForAccount(ctx, nil, account, original, "gpt-6.1-sol", "chat")
	require.NoError(t, err)
	require.Equal(t, "medium", gjson.GetBytes(first, "reasoning_effort").String())
	repaired, err := sjson.DeleteBytes(first, "metadata")
	require.NoError(t, err)
	repaired, err = sjson.SetBytes(repaired, "input", "repair survives")
	require.NoError(t, err)
	second, _, err := applyFinalReasoningEffortForAccount(ctx, nil, account, repaired, "gpt-6.1-sol", "responses")
	require.NoError(t, err)
	require.Equal(t, "medium", gjson.GetBytes(second, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(second, "metadata").Exists())
	require.Equal(t, "repair survives", gjson.GetBytes(second, "input").String())
	account2 := *account
	account2.ID++
	third, _, err := applyFinalReasoningEffortForAccount(ctx, nil, &account2, second, "gpt-6.1-terra", "responses")
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(third, "reasoning.effort").String())
	for _, protocol := range []string{"chat", "responses", "anthropic"} {
		body := []byte(`{"max_tokens":8192,"reasoning_effort":"none"}`)
		for _, model := range []string{"claude-sonnet-5-5", "claude-haiku-5-5"} {
			got, _, err := applyFinalReasoningEffortForAccount(m1EffortContext(body), nil, account, body, model, protocol)
			require.NoError(t, err)
			if protocol == "anthropic" {
				want := "between_tools"
				if model == "claude-haiku-5-5" {
					want = "disabled"
				}
				require.Equal(t, want, gjson.GetBytes(got, "thinking.type").String())
				require.Equal(t, "low", gjson.GetBytes(got, "output_config.effort").String())
			}
		}
	}
	modeBody := []byte(`{"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}},"thinking":{"type":"disabled"}}`)
	modeOut, _, modeErr := applyFinalReasoningEffortForAccount(m1EffortContext(modeBody), nil, account, modeBody, "claude-haiku-5-5", "anthropic")
	require.NoError(t, modeErr)
	require.JSONEq(t, string(modeBody), string(modeOut))
	for _, original := range []string{`{}`, `{"reasoning":null}`} {
		synthesized := []byte(`{"reasoning":{"effort":"medium"}}`)
		got, changed, err := applyFinalReasoningEffortForAccount(m1EffortContext([]byte(original)), nil, account, synthesized, "gpt-6.1-sol", "responses")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, synthesized, got)
	}
	for _, deny := range []bool{false, true} {
		body := []byte(`{"reasoning":{"effort":"max"}}`)
		mode := ""
		if deny {
			mode = ReasoningEffortOverLimitDeny
		}
		ctx := WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "medium", nil, mode)
		got, _, err := applyFinalReasoningEffortForAccount(ctx, nil, account, body, "gpt-6.1-sol", "responses")
		if deny {
			require.True(t, IsReasoningEffortPolicyDenied(err))
		} else {
			require.NoError(t, err)
			require.Equal(t, "medium", gjson.GetBytes(got, "reasoning.effort").String())
		}
	}
	body := []byte(`{"reasoning_effort":"low"}`)
	ctx = WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "", []ReasoningEffortMapping{{From: "low", To: "deny"}}, "")
	_, _, err = applyFinalReasoningEffortForAccount(ctx, nil, account, body, "gpt-6.1-sol", "responses")
	require.True(t, IsReasoningEffortPolicyDenied(err))
	ctx = WithOpenAIReasoningEffortPolicy(m1EffortContext(body), "minimal", nil, "")
	_, _, err = applyFinalReasoningEffortForAccount(ctx, nil, account, body, "gpt-6.1-sol", "responses")
	require.True(t, IsReasoningEffortPolicyDenied(err))
}
func TestM1ResponsesFinalEffortWire(t *testing.T) {
	for _, effort := range []string{"none", "minimal", "max"} {
		body := []byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"` + effort + `"},"input":"hi"}`)
		upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(200, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)}}
		account := newOpenAIRejectedFieldTestAccount()
		ctx := m1EffortContext(body)
		result, err := newOpenAIRejectedFieldTestService(upstream).Forward(ctx, newOpenAIRejectedFieldTestContext(body), account, body)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, upstream.bodies, 1)
		want := effort
		if effort == "none" || effort == "minimal" {
			want = "low"
		}
		require.Equal(t, want, gjson.GetBytes(upstream.bodies[0], "reasoning.effort").String())
	}
}

func TestM1NativeMessagesFinalEffortWire(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":8192,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`)
		account := newAnthropicAPIKeyAccountForTest()
		account.Extra["anthropic_passthrough"] = passthrough
		upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}}
		ctx := m1EffortContext(body)
		c := newOpenAIRejectedFieldTestContext(body)
		c.Request = c.Request.WithContext(ctx)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, err)
		result, err := newForwardPartialUsageServiceForTest(upstream).Forward(ctx, c, account, parsed)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, "between_tools", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
		require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "output_config.effort").String())
		require.Equal(t, "none", *RequestedReasoningEffortFromContext(ctx))
		if !passthrough {
			require.NotNil(t, result.ReasoningEffort)
			require.Equal(t, "low", *result.ReasoningEffort)
		}
	}
}
