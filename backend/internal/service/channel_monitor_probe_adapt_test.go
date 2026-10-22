//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProbeCorrection(t *testing.T) {
	injected := probeBodyInfo{Stream: true, InjectedReasoning: true, HasMaxTokens: true}
	plain := probeBodyInfo{Stream: true, HasMaxTokens: true}
	for _, tc := range []struct {
		name   string
		info   probeBodyInfo
		status int
		body   string
		want   probeShapeFix
		ok     bool
	}{
		{"openai max_tokens", injected, 400,
			`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}}`,
			probeShapeFix{UseMaxCompletionTokens: true}, true},
		{"openai reasoning_effort", injected, 400,
			`{"error":{"message":"Unsupported parameter: 'reasoning_effort' is not supported with this model."}}`,
			probeShapeFix{DropReasoning: true}, true},
		{"xai camel case", injected, 400, `{"error":"Model grok-4 does not support parameter reasoningEffort."}`,
			probeShapeFix{DropReasoning: true}, true},
		{"gemini thinking budget", injected, 400,
			`{"error":{"code":400,"message":"Budget 0 is invalid. This model only works in thinking mode.","status":"INVALID_ARGUMENT"}}`,
			probeShapeFix{DropReasoning: true}, true},
		{"stream value rejected", plain, 400,
			`{"error":{"message":"Unsupported value: 'stream' does not support true with this model."}}`,
			probeShapeFix{NoStream: true}, true},
		{"streaming not supported", plain, 422, `{"detail":"Streaming is not supported for this model"}`,
			probeShapeFix{NoStream: true}, true},
		{"generic unknown parameter", injected, 400, `{"error":{"message":"Unrecognized request argument supplied"}}`,
			probeShapeFix{DropReasoning: true}, true},
		{"pydantic extra inputs", injected, 422, `{"detail":[{"msg":"Extra inputs are not permitted"}]}`,
			probeShapeFix{DropReasoning: true}, true},
		{"generic without injected reasoning", plain, 400, `{"error":{"message":"Unrecognized request argument supplied"}}`,
			probeShapeFix{}, false},
		{"upstream is not stream", plain, 400, `{"error":{"message":"invalid upstream response"}}`, probeShapeFix{}, false},
		{"model not found", injected, 400, `{"error":{"message":"model gpt-9 does not exist"}}`, probeShapeFix{}, false},
		{"rate limit mentioning reasoning", injected, 429, `{"error":{"message":"reasoning quota exceeded"}}`, probeShapeFix{}, false},
		{"server error", injected, 500, `{"error":{"message":"unsupported parameter reasoning_effort"}}`, probeShapeFix{}, false},
		{"replace mode", probeBodyInfo{Stream: true, Replace: true}, 400, `{"error":{"message":"stream is not supported"}}`, probeShapeFix{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := probeCorrection(&probeRequest{probeBodyInfo: tc.info}, tc.status, tc.body)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// rejectingUpstream 拒绝某种请求形状（400），其余按协议正常回答。
func rejectingUpstream(t *testing.T, protocol probeProtocol, reject func(body map[string]any) string) (*probeUpstream, string) {
	t.Helper()
	return newProbeUpstream(t, func(w http.ResponseWriter, _ *http.Request, body map[string]any, _ int) {
		if msg := reject(body); msg != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":{"message":%q}}`, msg)
			return
		}
		answer := probeTestAnswer(body)
		if protocol != probeProtocolGemini && body["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":"%s"}}]}`, answer)
			return
		}
		writeProbeSSE(w, probeAnswerEvents(protocol, answer)...)
	})
}

func TestRunProbe_AdaptsRejectedParametersAndRemembers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		model    string
		protocol probeProtocol
		reject   func(body map[string]any) string
		check    func(t *testing.T, second probeSeen)
	}{
		{"reasoning effort", MonitorProviderOpenAI, "gpt-5.5", probeProtocolChat, func(body map[string]any) string {
			if _, ok := body["reasoning_effort"]; ok {
				return "Unsupported parameter: 'reasoning_effort' is not supported with this model."
			}
			return ""
		}, func(t *testing.T, second probeSeen) {
			require.NotContains(t, second.Body, "reasoning_effort")
			require.Equal(t, true, second.Body["stream"])
		}},
		{"max_tokens", MonitorProviderOpenAI, "gpt-4o", probeProtocolChat, func(body map[string]any) string {
			if _, ok := body["max_tokens"]; ok {
				return "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."
			}
			return ""
		}, func(t *testing.T, second probeSeen) {
			require.NotContains(t, second.Body, "max_tokens")
			require.Equal(t, float64(monitorChallengeMaxTokens), second.Body["max_completion_tokens"])
		}},
		{"stream", MonitorProviderKimi, "kimi-k2.5", probeProtocolChat, func(body map[string]any) string {
			if body["stream"] == true {
				return "stream is not supported for this deployment"
			}
			return ""
		}, func(t *testing.T, second probeSeen) {
			require.Equal(t, false, second.Body["stream"])
			require.Equal(t, "application/json", second.Header.Get("Accept"))
		}},
		{"gemini thinking budget", MonitorProviderGemini, "gemini-3.6-flash", probeProtocolGemini, func(body map[string]any) string {
			gen, _ := body["generationConfig"].(map[string]any)
			if _, ok := gen["thinkingConfig"]; ok {
				return "Budget 0 is invalid. This model only works in thinking mode."
			}
			return ""
		}, func(t *testing.T, second probeSeen) {
			require.Equal(t, map[string]any{"maxOutputTokens": float64(monitorChallengeReasoningMaxTokens)}, second.Body["generationConfig"])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, endpoint := rejectingUpstream(t, tc.protocol, tc.reject)
			res := runProbe(context.Background(), tc.provider, endpoint, "key", tc.model, nil, fastProbeLimits())
			require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
			reqs := up.requests()
			require.Len(t, reqs, 2, "one rejected request, one corrected")
			tc.check(t, reqs[1])

			// 下一轮直接用修正后的形状。
			res = runProbe(context.Background(), tc.provider, endpoint, "key", tc.model, nil, fastProbeLimits())
			require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
			reqs = up.requests()
			require.Len(t, reqs, 3)
			tc.check(t, reqs[2])
		})
	}
}

// TestRunProbe_CorrectionsShareTheTwoRequestCap 两个参数先后被拒：本轮第二次也失败（每轮最多 2 次请求），
// 但两次修正都记住，下一轮一次成功。
func TestRunProbe_CorrectionsShareTheTwoRequestCap(t *testing.T) {
	up, endpoint := rejectingUpstream(t, probeProtocolChat, func(body map[string]any) string {
		if _, ok := body["reasoning_effort"]; ok {
			return "Unsupported parameter: 'reasoning_effort'"
		}
		if _, ok := body["max_tokens"]; ok {
			return "Use 'max_completion_tokens' instead."
		}
		return ""
	})
	res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "o3", nil, fastProbeLimits())
	require.Equal(t, MonitorStatusError, res.Status)
	require.Contains(t, res.Message, "upstream HTTP 400")
	require.Contains(t, res.Message, "max_completion_tokens")
	require.Len(t, up.requests(), 2)

	res = runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "o3", nil, fastProbeLimits())
	require.Equal(t, MonitorStatusOperational, res.Status, res.Message)
	reqs := up.requests()
	require.Len(t, reqs, 3)
	require.NotContains(t, reqs[2].Body, "reasoning_effort")
	require.NotContains(t, reqs[2].Body, "max_tokens")
}

func TestRunProbe_OtherBadRequestsAreNotResent(t *testing.T) {
	up, endpoint := rejectingUpstream(t, probeProtocolChat, func(map[string]any) string {
		return "model gpt-5.5 is not available in this group"
	})
	res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-5.5", nil, fastProbeLimits())
	require.Equal(t, MonitorStatusError, res.Status)
	require.True(t, strings.HasPrefix(res.Message, "upstream HTTP 400"), res.Message)
	require.Len(t, up.requests(), 1)
}

// TestRunProbe_MergeModeUserReasoningIsNeverDropped 用户在 merge 模板里写的推理参数以用户为准，不做修正。
func TestRunProbe_MergeModeUserReasoningIsNeverDropped(t *testing.T) {
	up, endpoint := rejectingUpstream(t, probeProtocolChat, func(body map[string]any) string {
		if _, ok := body["reasoning_effort"]; ok {
			return "Unsupported parameter: 'reasoning_effort'"
		}
		return ""
	})
	res := runProbe(context.Background(), MonitorProviderOpenAI, endpoint, "key", "gpt-5.5", &CheckOptions{
		BodyOverrideMode: MonitorBodyOverrideModeMerge, BodyOverride: map[string]any{"reasoning_effort": "high"},
	}, fastProbeLimits())
	require.Equal(t, MonitorStatusError, res.Status)
	require.Len(t, up.requests(), 1)
}

func TestProbeShapeFixCache_ConcurrentUse(t *testing.T) {
	cache := &probeShapeFixCache{fixes: map[string]probeShapeFix{}}
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 200; i++ {
				key := probeShapeKey("p", "e", "m", fmt.Sprint(i%7))
				cache.learn(key, probeShapeFix{DropReasoning: g%2 == 0, NoStream: g%2 == 1})
				_ = cache.get(key)
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	require.Equal(t, probeShapeFix{DropReasoning: true, NoStream: true}, cache.get(probeShapeKey("p", "e", "m", "3")))
}
