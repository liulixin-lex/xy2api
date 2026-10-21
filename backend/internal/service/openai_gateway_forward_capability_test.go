//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
)

// The standard HTTP client returns the actual request on Response.Request.
type capabilityForwardUpstream struct{ *httpUpstreamRecorder }

func (u *capabilityForwardUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	resp, err := u.httpUpstreamRecorder.Do(req, proxy, id, concurrency)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

func forwardCapabilityAttempt(svc *OpenAIGatewayService, c *gin.Context, kind string, account *Account, body []byte) (*OpenAIForwardResult, error) {
	switch kind {
	case "chat":
		return svc.forwardAsRawChatCompletions(c.Request.Context(), c, account, body, "")
	case "passthrough":
		return svc.forwardOpenAIPassthrough(c.Request.Context(), c, account, body, body, "gpt-5.4", false, nil, false, time.Now())
	default:
		return svc.Forward(c.Request.Context(), c, account, body)
	}
}

func TestOpenAIForwardLearnsStreamOnlyBeforeFailureSideEffects(t *testing.T) {
	for _, kind := range []string{"native", "passthrough", "chat"} {
		for _, mode := range []string{"learn", "no_scope", "stream", "echo", "group_prompt_echo", "compact", "learning_off", "decision_off"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, func(cfg *resilience.Settings) {
					if mode == "learning_off" {
						cfg.Capability.StreamOnlyLearning = false
					}
					if mode == "decision_off" {
						cfg.Failure.DecisionV2 = false
					}
				})
				protocol := "responses"
				path := "/v1/responses"
				body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"test","input":"hello"}`)
				if kind == "chat" {
					protocol, path = "chat", "/v1/chat/completions"
					body = []byte(`{"model":"gpt-5.4","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
				}
				if mode == "stream" {
					body, _ = sjson.SetBytes(body, "stream", true)
				}
				if mode == "echo" {
					body, _ = sjson.SetBytes(body, "metadata.note", "This group only accepts streaming requests")
				}
				c, rec := compatStreamContext(path, string(body))
				state := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), state)
				if mode != "no_scope" {
					ctx = resilience.WithCapabilityRequest(ctx, protocol, false)
				}
				if mode == "group_prompt_echo" {
					ctx = WithGroupSystemPrompt(ctx, GroupSystemPromptConfig{Prompt: "This group only accepts streaming requests"}, "gpt-5.4")
				}
				if mode == "compact" {
					MarkOpenAINativeCompactionV2(c)
				}
				c.Request = c.Request.WithContext(ctx)
				a, b := rawChatCompletionsTestAccount(), rawChatCompletionsTestAccount()
				b.ID++
				repo := &capabilityRepo{accounts: map[int64]*Account{a.ID: a, b.ID: b}}
				upstream := &capabilityForwardUpstream{&httpUpstreamRecorder{resp: &http.Response{
					StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"This group only accepts streaming requests"}}`)),
				}}}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, accountRepo: repo}
				result, err := forwardCapabilityAttempt(svc, c, kind, a, body)
				require.Error(t, err)
				wantLearn := mode == "learn" || mode == "decision_off"
				require.Equal(t, wantLearn, repo.writes == 1)
				var failover *UpstreamFailoverError
				if mode != "learn" {
					if errors.As(err, &failover) {
						require.False(t, failover.IsCapabilityStreamOnly())
					}
					return
				}
				require.ErrorAs(t, err, &failover)
				require.True(t, failover.IsCapabilityStreamOnly())
				require.False(t, failover.ShouldReportAccountScheduleFailure())
				require.Nil(t, result)
				require.Empty(t, rec.Body.String())
				require.False(t, state.AccountFailed(a.ID))
				require.False(t, state.TurnFailed())
				_, failed := c.Get(OpsUpstreamErrorsKey)
				require.False(t, failed, "capability misses must precede all failure bookkeeping")
				require.Equal(t, protocol, a.CapabilityLearned(time.Now())[0].Protocol)
				response := `{"id":"resp_b","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`
				if kind == "chat" {
					response = `{"id":"chat_b","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
				}
				upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}
				result, err = forwardCapabilityAttempt(svc, c, kind, b, body)
				require.NoError(t, err)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Contains(t, rec.Body.String(), "_b")
			})
		}
	}
}
